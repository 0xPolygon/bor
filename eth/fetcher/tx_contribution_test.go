package fetcher

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestTransactionContributionAdmission(t *testing.T) {
	for _, tc := range []struct {
		name     string
		results  []error
		accepted uint64
		complete bool
	}{
		{"accepted", []error{nil, nil}, 2, true},
		{"mixed", []error{nil, txpool.ErrAlreadyKnown}, 1, true},
		{"already known", []error{txpool.ErrAlreadyKnown, txpool.ErrAlreadyKnown}, 0, true},
		{"pool policy", []error{txpool.ErrUnderpriced, txpool.ErrTxGasPriceTooLow}, 0, true},
		{"context rejection", []error{core.ErrNonceTooLow, core.ErrInsufficientFunds}, 0, true},
		{"short results", []error{nil}, 0, false},
		{"missing results", nil, 0, false},
		{"excess results", []error{nil, nil, nil}, 0, false},
		{"processing interrupted", []error{txpool.ErrKZGVerificationError, nil}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NewTxFetcher(func(common.Hash) bool { return false }, func([]*types.Transaction) []error {
				return tc.results
			}, func(string, uint64, []common.Hash) error { return nil }, func(string) {})
			f.validationObserver = func(string, DeliveryProvenance, uint64, uint64, bool, uint64, bool, mclock.AbsTime) {}
			f.cleanup = make(chan *txDelivery, 1)
			if err := f.Enqueue("peer", testTxs[:2], false, 0); err != nil {
				t.Fatal(err)
			}
			if got := (<-f.cleanup).observation; got.accepted != tc.accepted || got.complete != tc.complete {
				t.Fatalf("admission = %+v, want accepted=%d complete=%v", got, tc.accepted, tc.complete)
			}
		})
	}
}

func TestTransactionContributionDuplicates(t *testing.T) {
	seen := make(map[common.Hash]bool)
	f := NewTxFetcher(func(common.Hash) bool { return false }, func(txs []*types.Transaction) []error {
		errs := make([]error, len(txs))
		for i, tx := range txs {
			if seen[tx.Hash()] {
				errs[i] = txpool.ErrAlreadyKnown
			}
			seen[tx.Hash()] = true
		}
		return errs
	}, func(string, uint64, []common.Hash) error { return nil }, func(string) {})
	observations := make(chan uint64, 1)
	f.SetValidationObserver(func(_ string, _ DeliveryProvenance, _, _ uint64, _ bool, accepted uint64, complete bool, receivedAt mclock.AbsTime) {
		if !complete {
			accepted = 255
		}
		observations <- accepted
	})
	f.Start()
	defer f.Stop()
	for _, want := range []uint64{1, 0} {
		if err := f.Enqueue("peer", []*types.Transaction{testTxs[0], testTxs[0]}, false, 0); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-observations:
			if got != want {
				t.Fatalf("accepted = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("missing admission observation")
		}
	}
}

func TestTransactionContributionIncompleteBatch(t *testing.T) {
	txs := make([]*types.Transaction, addTxsBatchSize+1)
	for i := range txs {
		txs[i] = testTxs[0]
	}
	f := NewTxFetcher(func(common.Hash) bool { return false }, func(batch []*types.Transaction) []error {
		if len(batch) == 1 {
			return nil
		}
		return make([]error, len(batch))
	}, func(string, uint64, []common.Hash) error { return nil }, func(string) {})
	f.validationObserver = func(string, DeliveryProvenance, uint64, uint64, bool, uint64, bool, mclock.AbsTime) {}
	f.cleanup = make(chan *txDelivery, 1)
	if err := f.Enqueue("peer", txs, false, 0); err != nil {
		t.Fatal(err)
	}
	if got := (<-f.cleanup).observation; got.accepted != 0 || got.complete {
		t.Fatalf("incomplete batch admission = %+v", got)
	}
}
