package eth

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/eth/fetcher"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestPeerPolicyTransactionAllowanceBoundary(t *testing.T) {
	for _, provenance := range []fetcher.DeliveryProvenance{fetcher.UnknownDelivery, fetcher.UnsolicitedDelivery, fetcher.RequestedDelivery} {
		for _, items := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/items=%v", provenance, items), func(t *testing.T) {
				h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
				count, bytes := uint64(0), uint64(64<<20)
				if items {
					count, bytes = 32768, 0
				}
				h.observeTransactionDelivery("peer", provenance, count, bytes, false, 0, true, 0)
				if got := h.peerPolicy.Snapshot("peer"); got.Risk != 0 {
					t.Fatalf("at allowance: %+v", got)
				}
				count, bytes = 0, 1
				if items {
					count, bytes = 1, 0
				}
				h.observeTransactionDelivery("peer", provenance, count, bytes, false, 0, true, 0)
				want := uint64(20)
				if provenance == fetcher.RequestedDelivery {
					want = 0
				}
				got := h.peerPolicy.Snapshot("peer")
				if got.Risk != want || got.Windows[peerpolicy.TransactionVolume.String()] != want/20 {
					t.Fatalf("above allowance: %+v", got)
				}
				h.observeTransactionDelivery("invalid", provenance, 32769, 64<<20+1, true, 0, true, 0)
				if got := h.peerPolicy.Snapshot("invalid"); got.Risk != 60 || got.Windows[peerpolicy.TransactionVolume.String()] != 0 {
					t.Fatalf("invalid-data classification: %+v", got)
				}
			})
		}
	}
}

func TestPeerPolicyTransactionReplyReplay(t *testing.T) {
	clock := new(mclock.Simulated)
	h := &handler{peerPolicy: peerpolicy.New(clock)}
	requests := make(chan uint64, 1)
	observed := make(chan fetcher.DeliveryProvenance, 1)
	f := fetcher.NewTxFetcher(func(common.Hash) bool { return false }, func(txs []*types.Transaction) []error { return make([]error, len(txs)) }, func(_ string, id uint64, _ []common.Hash) error { requests <- id; return nil }, func(string) {})
	f.SetValidationObserver(func(id string, p fetcher.DeliveryProvenance, items, bytes uint64, invalid bool, accepted uint64, complete bool, receivedAt mclock.AbsTime) {
		h.observeTransactionDelivery(id, p, items, bytes, invalid, accepted, complete, receivedAt)
		observed <- p
	})
	f.Start()
	defer f.Stop()
	tx := types.NewTx(&types.LegacyTx{Data: []byte{1}})
	if err := f.Notify("peer", []byte{types.LegacyTxType}, []uint32{uint32(tx.Size())}, []common.Hash{tx.Hash()}); err != nil {
		t.Fatal(err)
	}
	var requestID uint64
	select {
	case requestID = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("no transaction request")
	}
	for _, want := range []fetcher.DeliveryProvenance{fetcher.RequestedDelivery, fetcher.UnknownDelivery} {
		// Leave exactly one transaction below the volume boundary.
		h.peerPolicy.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.Transactions, Bytes: (64 << 20) - tx.Size()})
		if err := f.Enqueue("peer", []*types.Transaction{tx}, true, requestID); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-observed:
			if got != want {
				t.Fatalf("provenance=%v want=%v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no delivery observation")
		}
		if got := h.peerPolicy.Snapshot("peer"); got.Risk != 0 {
			t.Fatalf("at boundary: %+v", got)
		}
		h.observeTransactionDelivery("peer", want, 0, 1, false, 0, true, 0)
		expected := uint64(0)
		if want == fetcher.UnknownDelivery {
			expected = 20
		}
		if got := h.peerPolicy.Snapshot("peer"); got.Risk != expected {
			t.Fatalf("above boundary: %+v", got)
		}
		clock.Run(time.Minute)
	}
}

type policyTransactionReader struct{ message *p2p.Msg }

func (r *policyTransactionReader) ReadMsg() (p2p.Msg, error) {
	if r.message == nil {
		return p2p.Msg{}, io.EOF
	}
	message := *r.message
	r.message = nil
	return message, nil
}

func (*policyTransactionReader) WriteMsg(p2p.Msg) error { return errors.New("unexpected write") }

func handlePolicyTransaction(t *testing.T, h *handler, packet eth.Packet) (string, error) {
	t.Helper()
	encoded, err := rlp.EncodeToBytes(packet)
	if err != nil {
		t.Fatal(err)
	}
	rw := &policyTransactionReader{message: &p2p.Msg{Code: uint64(packet.Kind()), Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}}
	peer := eth.NewPeer(eth.ETH69, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
	defer peer.Close()
	h.observeProtocolPeer(peer)
	h.synced.Store(true)
	return peer.ID(), eth.Handle((*ethHandler)(h), peer)
}

func newPolicyTransactionFetcher(h *handler, delivered chan<- struct{}) *fetcher.TxFetcher {
	f := fetcher.NewTxFetcher(func(common.Hash) bool { return false }, func(txs []*types.Transaction) []error {
		return make([]error, len(txs))
	}, nil, nil)
	f.SetValidationObserver(func(id string, provenance fetcher.DeliveryProvenance, items, bytes uint64, invalid bool, accepted uint64, complete bool, receivedAt mclock.AbsTime) {
		h.observeTransactionDelivery(id, provenance, items, bytes, invalid, accepted, complete, receivedAt)
		delivered <- struct{}{}
	})
	return f
}

func TestPeerPolicyForwardedTransactionTraffic(t *testing.T) {
	txs := []*types.Transaction{types.NewTx(&types.LegacyTx{Nonce: 1}), types.NewTx(&types.LegacyTx{Nonce: 2})}
	for _, packet := range []eth.Packet{(*eth.TransactionsPacket)(&txs), &eth.PooledTransactionsPacket{RequestId: 42, PooledTransactionsResponse: txs}} {
		t.Run(packet.Name(), func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
			delivered := make(chan struct{}, 1)
			h.txFetcher = newPolicyTransactionFetcher(h, delivered)
			h.txFetcher.Start()
			defer h.txFetcher.Stop()
			before := policyTrafficCounters()
			id, err := handlePolicyTransaction(t, h, packet)
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			select {
			case <-delivered:
			case <-time.After(time.Second):
				t.Fatal("fetcher did not observe the delivered transactions")
			}
			after := policyTrafficCounters()
			want := [4]int64{0, 0, 2, int64(txs[0].Size() + txs[1].Size())}
			for i := range want {
				if got := after[i] - before[i]; got != want[i] {
					t.Errorf("counter %d: got %d, want %d", i, got, want[i])
				}
			}
			if got := h.peerPolicy.Snapshot(id); got.Risk != 0 {
				t.Fatalf("valid delivery scored: %+v", got)
			}
		})
	}
}

func policyTrafficCounters() [4]int64 {
	var values [4]int64
	for i, name := range []string{"items/other", "bytes/other", "items/transactions", "bytes/transactions"} {
		values[i] = metrics.GetOrRegisterCounter("eth/peerpolicy/"+name, nil).Snapshot().Count()
	}
	return values
}

func TestPeerPolicyTransactionLocalFailure(t *testing.T) {
	txs := []*types.Transaction{types.NewTx(&types.LegacyTx{})}
	for _, packet := range []eth.Packet{(*eth.TransactionsPacket)(&txs), &eth.PooledTransactionsPacket{RequestId: 42, PooledTransactionsResponse: txs}} {
		t.Run(packet.Name(), func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
			h.txFetcher = newPolicyTransactionFetcher(h, nil)
			h.txFetcher.Stop()
			before := policyTrafficCounters()
			id, err := handlePolicyTransaction(t, h, packet)
			if err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("missing fetcher termination error: %v", err)
			}
			if got := h.peerPolicy.Snapshot(id); got.Risk != 0 || len(got.Windows) != 0 {
				t.Fatalf("local failure scored as peer fault: %+v", got)
			}
			after := policyTrafficCounters()
			encoded, err := rlp.EncodeToBytes(packet)
			if err != nil {
				t.Fatal(err)
			}
			if after[0]-before[0] != 1 || after[1]-before[1] != int64(len(encoded)) || after[2] != before[2] || after[3] != before[3] {
				t.Fatalf("local failure lost or duplicated traffic: before=%v after=%v", before, after)
			}
		})
	}
}

func TestPeerPolicyRequestedTransactionTraffic(t *testing.T) {
	h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
	delivered, requests := make(chan struct{}, 1), make(chan uint64, 1)
	h.txFetcher = fetcher.NewTxFetcher(func(common.Hash) bool { return false }, func(txs []*types.Transaction) []error {
		return make([]error, len(txs))
	}, func(_ string, id uint64, _ []common.Hash) error {
		requests <- id
		return nil
	}, nil)
	h.txFetcher.SetValidationObserver(func(id string, provenance fetcher.DeliveryProvenance, items, bytes uint64, invalid bool, accepted uint64, complete bool, receivedAt mclock.AbsTime) {
		h.observeTransactionDelivery(id, provenance, items, bytes, invalid, accepted, complete, receivedAt)
		delivered <- struct{}{}
	})
	h.txFetcher.Start()
	defer h.txFetcher.Stop()
	tx := types.NewTx(&types.LegacyTx{})
	if err := h.txFetcher.Notify((enode.ID{1}).String(), []byte{tx.Type()}, []uint32{uint32(tx.Size())}, []common.Hash{tx.Hash()}); err != nil {
		t.Fatal(err)
	}
	var requestID uint64
	select {
	case requestID = <-requests:
	case <-time.After(3 * time.Second):
		t.Fatal("fetcher did not request the announcement")
	}
	before := policyTrafficCounters()
	packet := &eth.PooledTransactionsPacket{RequestId: requestID, PooledTransactionsResponse: []*types.Transaction{tx}}
	if _, err := handlePolicyTransaction(t, h, packet); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("fetcher did not observe the response")
	}
	after := policyTrafficCounters()
	if after[0]-before[0] != 1 || after[1]-before[1] != int64(tx.Size()) || after[2] != before[2] || after[3] != before[3] {
		t.Fatalf("requested reply was not counted exactly once: before=%v after=%v", before, after)
	}
}

func TestPeerPolicyBlobValidationEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blob    types.BlobTx
		pooled  bool
		wantErr string
	}{
		{name: "broadcast", wantErr: "disallowed broadcast blob transaction"},
		{name: "missing sidecar", pooled: true, wantErr: "received sidecar-less blob transaction"},
		{name: "commitment count", pooled: true, blob: types.BlobTx{BlobHashes: []common.Hash{{1}}, Sidecar: &types.BlobTxSidecar{}}, wantErr: "invalid number"},
		{name: "commitment hash", pooled: true, blob: types.BlobTx{BlobHashes: []common.Hash{{1}}, Sidecar: &types.BlobTxSidecar{Commitments: []kzg4844.Commitment{{1}}}}, wantErr: "mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
			txs := []*types.Transaction{types.NewTx(&tc.blob)}
			var packet eth.Packet = (*eth.TransactionsPacket)(&txs)
			if tc.pooled {
				packet = &eth.PooledTransactionsPacket{RequestId: 42, PooledTransactionsResponse: txs}
			}
			id, err := handlePolicyTransaction(t, h, packet)
			if err == nil || errors.Is(err, io.EOF) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("unexpected rejection: %v", err)
			}
			got := h.peerPolicy.Snapshot(id)
			if got.Risk != 60 || got.Windows[peerpolicy.InvalidTransaction.String()] != 1 {
				t.Fatalf("missing invalid transaction evidence: %+v", got)
			}
		})
	}
}
