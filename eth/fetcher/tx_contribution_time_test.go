package fetcher

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/types"
)

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
