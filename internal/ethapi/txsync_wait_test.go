package ethapi

import (
	"context"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

func newTxSyncBackend(t *testing.T) *testBackend {
	t.Helper()

	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{}}

	return newTestBackend(t, 0, genesis, ethash.NewFaker(), nil)
}

func TestSendRawTransactionSyncWaiterCap(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)
	b.syncMaxConcurrent = 1

	api := NewTransactionAPI(b, new(AddrLocker))
	raw, _ := makeSelfSignedRaw(t, api, b.acc.Address)

	parked := make(chan struct{})

	go func() {
		defer close(parked)

		timeout := hexutil.Uint64(800)
		_, _ = api.SendRawTransactionSync(context.Background(), raw, &timeout)
	}()

	waitForTxSyncWaiters(t, api, 1)

	timeout := hexutil.Uint64(100)

	receipt, err := api.SendRawTransactionSync(context.Background(), raw, &timeout)
	if receipt != nil {
		t.Fatalf("receipt = %#v, want nil", receipt)
	}
	if !errors.Is(err, errTxSyncBusy) {
		t.Fatalf("err = %v, want %v", err, errTxSyncBusy)
	}

	<-parked

	// The seat is handed back, so the next caller waits rather than being refused.
	if _, err := api.SendRawTransactionSync(context.Background(), raw, &timeout); errors.Is(err, errTxSyncBusy) {
		t.Fatal("still refusing callers after the waiter left")
	}
}

func TestSendRawTransactionSyncUncapped(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)
	b.syncMaxConcurrent = 0

	api := NewTransactionAPI(b, new(AddrLocker))
	if api.txSyncWaiters != nil {
		t.Fatal("waiter admission built for an uncapped node")
	}

	if !api.enterTxSyncWait() {
		t.Fatal("uncapped node refused a waiter")
	}

	api.leaveTxSyncWait()
}

func TestPollReceipt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		preconf        bool
		canonical      bool
		checkCanonical bool
		wantReceipt    bool
		wantPreconf    bool
	}{
		{name: "nothing landed"},
		{name: "preconf only", preconf: true, wantReceipt: true, wantPreconf: true},
		{name: "preconf prefers canonical", preconf: true, canonical: true, wantReceipt: true},
		{name: "canonical is not read on a preconf tick", canonical: true},
		{name: "canonical is read on the backstop tick", canonical: true, checkCanonical: true, wantReceipt: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newTxSyncBackend(t)
			api := NewTransactionAPI(b, new(AddrLocker))
			_, tx := makeSelfSignedRaw(t, api, b.acc.Address)

			if tt.canonical {
				b.autoMine = true
				b.sentTx, b.sentTxHash = tx, tx.Hash()
			}

			if tt.preconf {
				b.preconfEnabled = true
				b.preconf.tx = tx
				b.preconf.receipt = &types.Receipt{
					Type:              tx.Type(),
					Status:            types.ReceiptStatusSuccessful,
					CumulativeGasUsed: tx.Gas(),
					GasUsed:           tx.Gas(),
					EffectiveGasPrice: tx.GasPrice(),
					TxHash:            tx.Hash(),
					BlockNumber:       new(big.Int).Add(b.CurrentBlock().Number, common.Big1),
				}
			}

			receipt := api.pollReceipt(context.Background(), tx.Hash(), tt.checkCanonical)

			if (receipt != nil) != tt.wantReceipt {
				t.Fatalf("receipt = %#v, want receipt: %v", receipt, tt.wantReceipt)
			}
			if receipt == nil {
				return
			}
			if got := receipt["preconfirmation"] == true; got != tt.wantPreconf {
				t.Fatalf("preconfirmation = %v, want %v (receipt %#v)", got, tt.wantPreconf, receipt)
			}
		})
	}
}

func waitForTxSyncWaiters(t *testing.T, api *TransactionAPI, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(api.txSyncWaiters) == want {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("waiters = %d, want %d", len(api.txSyncWaiters), want)
}

func TestTxSyncTimeout(t *testing.T) {
	t.Parallel()

	ms := func(v uint64) *hexutil.Uint64 {
		h := hexutil.Uint64(v)

		return &h
	}

	tests := []struct {
		name    string
		request *hexutil.Uint64
		want    time.Duration
	}{
		{name: "unset falls back to default", request: nil, want: 2 * time.Second},
		{name: "zero falls back to default", request: ms(0), want: 2 * time.Second},
		{name: "under the ceiling is honoured", request: ms(1500), want: 1500 * time.Millisecond},
		{name: "at the ceiling", request: ms(300_000), want: 5 * time.Minute},
		{name: "over the ceiling is clamped", request: ms(600_000), want: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := NewTransactionAPI(newTxSyncBackend(t), new(AddrLocker))
			if got := api.txSyncTimeout(tt.request); got != tt.want {
				t.Fatalf("timeout = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTxSyncWaitClosed(t *testing.T) {
	t.Parallel()

	hash := common.HexToHash("0xabc")

	t.Run("deadline names the pooled transaction", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()

		<-ctx.Done()

		err := txSyncWaitClosed(ctx, hash, 3*time.Second)

		var timeoutErr *txSyncTimeoutError
		if !errors.As(err, &timeoutErr) {
			t.Fatalf("err = %T %v, want *txSyncTimeoutError", err, err)
		}
		if timeoutErr.ErrorCode() != errCodeTxSyncTimeout {
			t.Fatalf("code = %d, want %d", timeoutErr.ErrorCode(), errCodeTxSyncTimeout)
		}
		if timeoutErr.ErrorData() != hash.Hex() {
			t.Fatalf("data = %v, want %s", timeoutErr.ErrorData(), hash.Hex())
		}
		if !strings.Contains(timeoutErr.Error(), "3s") {
			t.Fatalf("message %q does not name the wait window", timeoutErr.Error())
		}
	})

	t.Run("caller cancellation is passed through", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := txSyncWaitClosed(ctx, hash, time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want %v", err, context.Canceled)
		}
	})
}

func TestSubscriptionFailure(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("feed broke")

	tests := []struct {
		name string
		err  error
		open bool
		want error
	}{
		{name: "closed channel", err: nil, open: false, want: errSubClosed},
		{name: "live error", err: sentinel, open: true, want: sentinel},
		{name: "live without error", err: nil, open: true, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := subscriptionFailure(tt.err, tt.open); !errors.Is(got, tt.want) {
				t.Fatalf("err = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReceiptFromChainEvent(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)
	api := NewTransactionAPI(b, new(AddrLocker))
	_, tx := makeSelfSignedRaw(t, api, b.acc.Address)

	blockHash := common.HexToHash("0xfeed")

	sealed := &types.Receipt{
		Type:              tx.Type(),
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		GasUsed:           21000,
		EffectiveGasPrice: big.NewInt(1),
		TxHash:            tx.Hash(),
		BlockHash:         blockHash,
		BlockNumber:       big.NewInt(7),
		TransactionIndex:  0,
	}
	unsealed := &types.Receipt{TxHash: tx.Hash()}
	other := &types.Receipt{TxHash: common.HexToHash("0xdead"), BlockHash: blockHash, BlockNumber: big.NewInt(7)}

	tests := []struct {
		name      string
		open      bool
		event     core.ChainEvent
		wantDone  bool
		wantErr   error
		wantBlock interface{}
	}{
		{
			name:     "closed feed ends the wait",
			open:     false,
			wantDone: true,
			wantErr:  errSubClosed,
		},
		{
			name:  "empty event keeps waiting",
			open:  true,
			event: core.ChainEvent{},
		},
		{
			name:  "mismatched lengths keep waiting",
			open:  true,
			event: core.ChainEvent{Receipts: types.Receipts{sealed}, Transactions: types.Transactions{}},
		},
		{
			name:  "other transactions keep waiting",
			open:  true,
			event: core.ChainEvent{Receipts: types.Receipts{other}, Transactions: types.Transactions{tx}},
		},
		{
			name:      "sealed receipt is marshalled from the event",
			open:      true,
			event:     core.ChainEvent{Receipts: types.Receipts{sealed}, Transactions: types.Transactions{tx}},
			wantDone:  true,
			wantBlock: blockHash,
		},
		{
			name:     "receipt without a block falls back to lookup",
			open:     true,
			event:    core.ChainEvent{Receipts: types.Receipts{unsealed}, Transactions: types.Transactions{tx}},
			wantDone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			receipt, done, err := api.receiptFromChainEvent(context.Background(), tt.event, tt.open, tx.Hash())

			if done != tt.wantDone {
				t.Fatalf("done = %v, want %v", done, tt.wantDone)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantBlock == nil {
				return
			}
			if got := receipt["blockHash"]; got != tt.wantBlock {
				t.Fatalf("blockHash = %v, want %v", got, tt.wantBlock)
			}
			if got := receipt["transactionHash"]; got != tx.Hash() {
				t.Fatalf("transactionHash = %v, want %v", got, tx.Hash())
			}
		})
	}
}

func TestDecodeRawTransaction(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)
	api := NewTransactionAPI(b, new(AddrLocker))
	raw, tx := makeSelfSignedRaw(t, api, b.acc.Address)

	t.Run("round trips a signed transaction", func(t *testing.T) {
		t.Parallel()

		got, err := api.decodeRawTransaction(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Hash() != tx.Hash() {
			t.Fatalf("hash = %s, want %s", got.Hash(), tx.Hash())
		}
	})

	t.Run("rejects garbage", func(t *testing.T) {
		t.Parallel()

		if _, err := api.decodeRawTransaction(hexutil.Bytes{0x01, 0x02, 0x03}); err == nil {
			t.Fatal("expected a decode error")
		}
	})
}

func TestSendRawTransactionSyncBusyDoesNotSubmit(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)
	b.syncMaxConcurrent = 1

	api := NewTransactionAPI(b, new(AddrLocker))
	raw, _ := makeSelfSignedRaw(t, api, b.acc.Address)

	if !api.enterTxSyncWait() {
		t.Fatal("could not take the only seat")
	}

	if _, err := api.SendRawTransactionSync(context.Background(), raw, nil); !errors.Is(err, errTxSyncBusy) {
		t.Fatalf("err = %v, want %v", err, errTxSyncBusy)
	}
	if b.sentTx != nil {
		t.Fatalf("refused call still submitted %s", b.sentTx.Hash())
	}
}

// TestParkedSyncCallDoesNotBlockTheServer drives the real handler over HTTP
// against a single-slot execution pool. The parked sync call owns that slot
// when it enters the wait, so a second call can only get through if the wait
// hands the slot back.
func TestParkedSyncCallDoesNotBlockTheServer(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)
	b.autoMine = false // nothing ever mines, so the sync call waits out its window
	b.syncMaxConcurrent = 8

	api := NewTransactionAPI(b, new(AddrLocker))
	raw, _ := makeSelfSignedRaw(t, api, b.acc.Address)

	server := rpc.NewServer("test", 1, 0)
	defer server.Stop()

	if err := server.RegisterName("eth", api); err != nil {
		t.Fatal(err)
	}

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	post := func(body string, timeout time.Duration) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL, strings.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		out, err := io.ReadAll(resp.Body)

		return string(out), err
	}

	go func() {
		_, _ = post(`{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransactionSync","params":["`+raw.String()+`","0x1388"]}`, 30*time.Second)
	}()

	// A registered waiter means the call is past admission and holding the
	// pool's only slot.
	waitForTxSyncWaiters(t, api, 1)

	got, err := post(`{"jsonrpc":"2.0","id":2,"method":"eth_getTransactionCount","params":["`+b.acc.Address.Hex()+`","pending"]}`, 2*time.Second)
	if err != nil {
		t.Fatalf("second call blocked behind the parked sync call: %v", err)
	}
	if !strings.Contains(got, `"result"`) {
		t.Fatalf("unexpected response: %s", got)
	}
}

func TestSendRawTransactionRejectsGarbage(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)
	api := NewTransactionAPI(b, new(AddrLocker))
	garbage := hexutil.Bytes{0x01, 0x02, 0x03}

	t.Run("async", func(t *testing.T) {
		t.Parallel()

		if _, err := api.SendRawTransaction(context.Background(), garbage); err == nil {
			t.Fatal("expected a decode error")
		}
	})

	t.Run("sync", func(t *testing.T) {
		t.Parallel()

		if _, err := api.SendRawTransactionSync(context.Background(), garbage, nil); err == nil {
			t.Fatal("expected a decode error")
		}
	})

	if b.sentTx != nil {
		t.Fatalf("undecodable input reached the pool as %s", b.sentTx.Hash())
	}
}

// TestPreconfTickSkipsCanonicalLookup pins the point of reading only the
// pending view on a preconf tick: the on-disk lookup costs two point-misses
// per waiter per tick, and canonical arrival comes in as a chain event.
func TestPreconfTickSkipsCanonicalLookup(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)
	b.canonicalLookups = new(atomic.Int64)

	api := NewTransactionAPI(b, new(AddrLocker))
	_, tx := makeSelfSignedRaw(t, api, b.acc.Address)

	if got := api.pollReceipt(context.Background(), tx.Hash(), false); got != nil {
		t.Fatalf("receipt = %#v, want nil", got)
	}
	if got := b.canonicalLookups.Load(); got != 0 {
		t.Fatalf("preconf tick made %d canonical lookups, want 0", got)
	}

	if got := api.pollReceipt(context.Background(), tx.Hash(), true); got != nil {
		t.Fatalf("receipt = %#v, want nil", got)
	}
	if got := b.canonicalLookups.Load(); got == 0 {
		t.Fatal("backstop tick made no canonical lookup")
	}
}

// TestBackstopFindsReceiptWithoutChainEvent covers the gap the backstop exists
// for: the transaction goes canonical but no chain event names it, so the only
// way out of the wait is the periodic on-disk lookup.
func TestBackstopFindsReceiptWithoutChainEvent(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)

	api := NewTransactionAPI(b, new(AddrLocker))
	raw, tx := makeSelfSignedRaw(t, api, b.acc.Address)

	b.autoMine = true
	b.suppressChainEvent = true
	b.sentTx, b.sentTxHash = tx, tx.Hash()
	// Not readable on the fast path, so only a later backstop tick can find it.
	b.canonicalAfter = time.Now().Add(200 * time.Millisecond)

	timeout := hexutil.Uint64(10_000)

	start := time.Now()

	receipt, err := api.SendRawTransactionSync(context.Background(), raw, &timeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receipt == nil || receipt["transactionHash"] != tx.Hash() {
		t.Fatalf("receipt = %#v", receipt)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("backstop took %v, want it inside a few ticks", elapsed)
	}
}

// TestWaitLoopPollsPendingViewOnly checks the tick wiring, not just pollReceipt
// in isolation: across a whole wait window the loop must not be issuing an
// on-disk lookup per 100ms tick.
func TestWaitLoopPollsPendingViewOnly(t *testing.T) {
	t.Parallel()

	b := newTxSyncBackend(t)
	b.autoMine = false
	b.preconfEnabled = true // switches the 100ms preconf tick on
	b.canonicalLookups = new(atomic.Int64)

	api := NewTransactionAPI(b, new(AddrLocker))
	raw, _ := makeSelfSignedRaw(t, api, b.acc.Address)

	// Shorter than the 1s canonical backstop, so every lookup beyond the fast
	// path would have to come from a preconf tick.
	timeout := hexutil.Uint64(700)

	if _, err := api.SendRawTransactionSync(context.Background(), raw, &timeout); err == nil {
		t.Fatal("expected the wait window to elapse")
	}

	// One for the fast path, and no backstop tick inside 700ms.
	if got := b.canonicalLookups.Load(); got > 3 {
		t.Fatalf("%d canonical lookups over a 700ms wait, want the ticks to stay off disk", got)
	}
}
