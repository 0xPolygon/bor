package eth

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/fetcher"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
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
				h.observeTransactionDelivery("peer", provenance, count, bytes, false)
				if got := h.peerPolicy.Snapshot("peer"); got.Risk != 0 {
					t.Fatalf("at allowance: %+v", got)
				}
				count, bytes = 0, 1
				if items {
					count, bytes = 1, 0
				}
				h.observeTransactionDelivery("peer", provenance, count, bytes, false)
				want := uint64(20)
				if provenance == fetcher.RequestedDelivery {
					want = 0
				}
				got := h.peerPolicy.Snapshot("peer")
				if got.Risk != want || got.Windows[peerpolicy.TransactionVolume.String()] != want/20 {
					t.Fatalf("above allowance: %+v", got)
				}
				h.observeTransactionDelivery("invalid", provenance, 32769, 64<<20+1, true)
				if got := h.peerPolicy.Snapshot("invalid"); got.Risk != 60 || got.Windows[peerpolicy.TransactionVolume.String()] != 0 {
					t.Fatalf("invalid-data classification: %+v", got)
				}
			})
		}
	}
}

func TestPeerPolicyTransactionReplyReplay(t *testing.T) {
	h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
	requests := make(chan uint64, 1)
	observed := make(chan fetcher.DeliveryProvenance, 1)
	f := fetcher.NewTxFetcher(func(common.Hash) bool { return false }, func(txs []*types.Transaction) []error { return make([]error, len(txs)) }, func(_ string, id uint64, _ []common.Hash) error { requests <- id; return nil }, func(string) {})
	f.SetValidationObserver(func(id string, p fetcher.DeliveryProvenance, items, bytes uint64, invalid bool) {
		h.observeTransactionDelivery(id, p, items, bytes, invalid)
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
		h.observeTransactionDelivery("peer", want, 0, 1, false)
		expected := uint64(0)
		if want == fetcher.UnknownDelivery {
			expected = 20
		}
		if got := h.peerPolicy.Snapshot("peer"); got.Risk != expected {
			t.Fatalf("above boundary: %+v", got)
		}
		h.peerPolicy = peerpolicy.New(new(mclock.Simulated))
	}
}
