package fetcher

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestPeerPolicyTransactionClassification(t *testing.T) {
	for _, test := range []struct {
		err     error
		invalid bool
	}{
		{nil, false},
		{txpool.ErrInvalidSender, true},
		{txpool.ErrKZGVerificationError, true},
		{fmt.Errorf("wrapped: %w", txpool.ErrInvalidSender), true},
		{core.ErrNonceTooLow, false},
		{core.ErrInsufficientFunds, false},
		{txpool.ErrAlreadyKnown, false},
		{txpool.ErrUnderpriced, false},
		{txpool.ErrReplaceUnderpriced, false},
		{txpool.ErrTxGasPriceTooLow, false},
		{txpool.ErrAccountLimitExceeded, false},
	} {
		if got := invalidTransaction(test.err); got != test.invalid {
			t.Errorf("%v: invalid=%v", test.err, got)
		}
	}
}

func TestPeerPolicyTransactionObserver(t *testing.T) {
	f := &TxFetcher{}
	if got := f.validationSummary(testTxs, true, 1, true, 123); got != (txObservation{}) {
		t.Fatal("disabled observation retained evidence")
	}
	f.observeDelivery(nil)
	f.requests = map[string]*txRequest{"peer": {requestID: 7, hashes: testTxsHashes[:2]}}
	calls := 0
	f.SetValidationObserver(func(id string, solicited DeliveryProvenance, count, bytes uint64, invalid bool, accepted uint64, complete bool, receivedAt mclock.AbsTime) {
		calls++
		if id != "peer" || solicited != RequestedDelivery || count != 2 || bytes != testTxs[0].Size()+testTxs[1].Size() || !invalid || accepted != 1 || !complete || receivedAt != 123 {
			t.Fatal("incorrect transaction observation")
		}
	})
	f.observeDelivery(&txDelivery{origin: "peer", direct: true, requestID: 7, hashes: testTxsHashes[:2], observation: f.validationSummary(testTxs[:2], true, 1, true, 123)})
	if calls != 1 {
		t.Fatal("missing observation")
	}
}

func TestPeerPolicyRequestedTransactionDelivery(t *testing.T) {
	request := &txRequest{requestID: 7, hashes: testTxsHashes[:2]}
	for _, tc := range []struct {
		name    string
		request *txRequest
		direct  bool
		id      uint64
		hashes  []common.Hash
		want    DeliveryProvenance
	}{
		{"matching", request, true, 7, testTxsHashes[:2], RequestedDelivery},
		{"partial", request, true, 7, testTxsHashes[1:2], RequestedDelivery},
		{"empty", request, true, 7, nil, RequestedDelivery},
		{"broadcast", request, false, 7, testTxsHashes[:2], UnsolicitedDelivery},
		{"no request", nil, true, 7, testTxsHashes[:2], UnknownDelivery},
		{"stale id", request, true, 8, testTxsHashes[:2], UnknownDelivery},
		{"expired", &txRequest{requestID: 7}, true, 7, testTxsHashes[:2], UnknownDelivery},
		{"extra transaction", request, true, 7, testTxsHashes[:3], UnsolicitedDelivery},
		{"different transaction", request, true, 7, testTxsHashes[2:3], UnsolicitedDelivery},
		{"duplicate", request, true, 7, []common.Hash{testTxsHashes[0], testTxsHashes[0]}, UnsolicitedDelivery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delivery := &txDelivery{direct: tc.direct, requestID: tc.id, hashes: tc.hashes, observation: txObservation{items: uint64(len(tc.hashes))}}
			if got := requestedDelivery(tc.request, delivery); got != tc.want {
				t.Fatalf("solicited = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPeerPolicyTransactionReplyCorrelation(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			wantAccepted := uint64(1)
			if invalid {
				wantAccepted = 0
			}
			f := NewTxFetcher(func(common.Hash) bool { return false }, func(txs []*types.Transaction) []error {
				errs := make([]error, len(txs))
				if invalid {
					errs[0] = txpool.ErrInvalidSender
				}
				return errs
			}, func(string, uint64, []common.Hash) error { return nil }, func(string) {})
			f.requests["peer"] = &txRequest{requestID: 7, hashes: testTxsHashes[:1]}
			observations := make(chan DeliveryProvenance, 2)
			f.SetValidationObserver(func(id string, solicited DeliveryProvenance, items, bytes uint64, bad bool, accepted uint64, complete bool, receivedAt mclock.AbsTime) {
				if id != "peer" || items != 1 || bytes != testTxs[0].Size() || bad != invalid || accepted != wantAccepted || !complete {
					observations <- DeliveryProvenance(255)
					return
				}
				observations <- solicited
			})
			f.Start()
			defer f.Stop()
			for _, want := range []DeliveryProvenance{RequestedDelivery, UnknownDelivery} {
				if err := f.Enqueue("peer", testTxs[:1], true, 7); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-observations:
					if got != want {
						t.Fatalf("solicited = %v, want %v", got, want)
					}
				case <-time.After(time.Second):
					t.Fatal("missing delivery observation")
				}
			}
		})
	}
}

func TestPeerPolicyDisabledPoolCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		requested, delivered int
		want                 DeliveryProvenance
	}{
		{"matching single", 1, 1, RequestedDelivery},
		{"partial reply", 2, 1, RequestedDelivery},
		{"incomplete matching summary", 2, 2, UnknownDelivery},
		{"incomplete mixed summary", 1, 2, UnknownDelivery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pool *txpool.TxPool
			f := NewTxFetcher(pool.Has, func(txs []*types.Transaction) []error {
				return pool.Add(txs, false)
			}, func(string, uint64, []common.Hash) error { return nil }, func(string) {})
			f.requests["peer"] = &txRequest{requestID: 7, hashes: testTxsHashes[:tc.requested]}
			observations := make(chan DeliveryProvenance, 2)
			f.SetValidationObserver(func(_ string, provenance DeliveryProvenance, items, bytes uint64, invalid bool, accepted uint64, complete bool, _ mclock.AbsTime) {
				if items != uint64(tc.delivered) || bytes == 0 || invalid || accepted != 0 || complete != (tc.delivered == 1) {
					provenance = DeliveryProvenance(255)
				}
				observations <- provenance
			})
			f.Start()
			defer f.Stop()
			for _, want := range []DeliveryProvenance{tc.want, UnknownDelivery} {
				if err := f.Enqueue("peer", testTxs[:tc.delivered], true, 7); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-observations:
					if got != want {
						t.Fatalf("delivery provenance = %v, want %v", got, want)
					}
				case <-time.After(time.Second):
					t.Fatal("missing delivery observation")
				}
			}
		})
	}
}

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
	f.SetValidationObserver(func(_ string, _ DeliveryProvenance, _, _ uint64, _ bool, accepted uint64, complete bool, _ mclock.AbsTime) {
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

func TestTransactionContributionPreservesArrivalTime(t *testing.T) {
	clock := new(mclock.Simulated)
	clock.Run(time.Second)
	started, release := make(chan struct{}), make(chan struct{})
	f := NewTxFetcher(func(common.Hash) bool { return false }, func(txs []*types.Transaction) []error {
		close(started)
		<-release
		return make([]error, len(txs))
	}, func(string, uint64, []common.Hash) error { return nil }, func(string) {})
	f.clock = clock
	observations := make(chan mclock.AbsTime, 1)
	f.SetValidationObserver(func(_ string, _ DeliveryProvenance, _, _ uint64, _ bool, _ uint64, _ bool, receivedAt mclock.AbsTime) {
		observations <- receivedAt
	})
	f.Start()
	defer f.Stop()
	arrival := clock.Now()
	result := make(chan error, 1)
	go func() { result <- f.Enqueue("peer", testTxs[:1], false, 0) }()
	<-started
	// A new same-ID connection can begin while pool admission is still running.
	clock.Run(2 * time.Second)
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-observations:
		if got != arrival || got >= clock.Now() {
			t.Fatalf("receivedAt = %v, want original arrival %v before %v", got, arrival, clock.Now())
		}
	case <-time.After(time.Second):
		t.Fatal("missing contribution timestamp")
	}
}
