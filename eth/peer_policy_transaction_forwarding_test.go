package eth

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/fetcher"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

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
	f.SetValidationObserver(func(id string, provenance fetcher.DeliveryProvenance, items, bytes uint64, invalid bool) {
		h.observeTransactionDelivery(id, provenance, items, bytes, invalid)
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
	h.txFetcher.SetValidationObserver(func(id string, provenance fetcher.DeliveryProvenance, items, bytes uint64, invalid bool) {
		h.observeTransactionDelivery(id, provenance, items, bytes, invalid)
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
