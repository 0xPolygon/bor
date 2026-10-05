package fetcher

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

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
	if got := f.validationSummary(testTxs, true); got != (txObservation{}) {
		t.Fatal("disabled observation retained evidence")
	}
	f.observeDelivery(nil)
	f.requests = map[string]*txRequest{"peer": {requestID: 7, hashes: testTxsHashes[:2]}}
	calls := 0
	f.SetValidationObserver(func(id string, solicited bool, count, bytes uint64, invalid bool) {
		calls++
		if id != "peer" || !solicited || count != 2 || bytes != testTxs[0].Size()+testTxs[1].Size() || !invalid {
			t.Fatal("incorrect transaction observation")
		}
	})
	f.observeDelivery(&txDelivery{origin: "peer", direct: true, requestID: 7, hashes: testTxsHashes[:2], observation: f.validationSummary(testTxs[:2], true)})
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
		want    bool
	}{
		{"matching", request, true, 7, testTxsHashes[:2], true},
		{"partial", request, true, 7, testTxsHashes[1:2], true},
		{"empty", request, true, 7, nil, true},
		{"broadcast", request, false, 7, testTxsHashes[:2], false},
		{"no request", nil, true, 7, testTxsHashes[:2], false},
		{"stale id", request, true, 8, testTxsHashes[:2], false},
		{"expired", &txRequest{requestID: 7}, true, 7, testTxsHashes[:2], false},
		{"extra transaction", request, true, 7, testTxsHashes[:3], false},
		{"different transaction", request, true, 7, testTxsHashes[2:3], false},
		{"duplicate", request, true, 7, []common.Hash{testTxsHashes[0], testTxsHashes[0]}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delivery := &txDelivery{direct: tc.direct, requestID: tc.id, hashes: tc.hashes}
			if got := requestedDelivery(tc.request, delivery); got != tc.want {
				t.Fatalf("solicited = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPeerPolicyTransactionReplyCorrelation(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			f := NewTxFetcher(func(common.Hash) bool { return false }, func(txs []*types.Transaction) []error {
				errs := make([]error, len(txs))
				if invalid {
					errs[0] = txpool.ErrInvalidSender
				}
				return errs
			}, func(string, uint64, []common.Hash) error { return nil }, func(string) {})
			f.requests["peer"] = &txRequest{requestID: 7, hashes: testTxsHashes[:1]}
			observations := make(chan bool, 2)
			f.SetValidationObserver(func(id string, solicited bool, items, bytes uint64, bad bool) {
				if id != "peer" || items != 1 || bytes != testTxs[0].Size() || bad != invalid {
					observations <- false
					return
				}
				observations <- solicited
			})
			f.Start()
			defer f.Stop()
			for _, want := range []bool{true, false} {
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
