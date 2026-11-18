package fetcher

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func TestTransactionContributionBeforeCompletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		direct bool
		result error
	}{
		{"broadcast", false, nil},
		{"requested", true, nil},
		{"known", false, txpool.ErrAlreadyKnown},
		{"underpriced", true, txpool.ErrUnderpriced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tracker, connection, clock := transactionContributionFixture(t)
				started, release := make(chan struct{}), make(chan struct{})
				f := NewTxFetcher(func(common.Hash) bool { return false }, func([]*types.Transaction) []error { return []error{tc.result} }, nil, nil)
				f.clock = clock
				f.requests["peer"] = &txRequest{requestID: 7, hashes: testTxsHashes[:1]}
				f.SetValidationObserver(func(_ string, _ DeliveryProvenance, _, _ uint64, _ bool, accepted uint64, _ bool, _ mclock.AbsTime) {
					close(started)
					<-release
					tracker.Observe("peer", peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{AcceptedTransactions: accepted}})
				})
				f.Start()
				defer f.Stop()
				sequence := connection.ObserveData(clock.Now())
				done := make(chan error, 1)
				go func() {
					err := f.Enqueue("peer", testTxs[:1], tc.direct, 7)
					tracker.Observe("peer", peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{ReceivedMessages: 1, MessageSequence: sequence}})
					done <- err
				}()
				<-started
				synctest.Wait()
				if _, eligible := tracker.RequestOnlySince("peer", connection); eligible {
					t.Error("peer became eligible before its admission outcome was observed")
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if _, eligible := tracker.RequestOnlySince("peer", connection); eligible != (tc.result != nil) {
					t.Fatalf("eligible after admission = %v, result = %v", eligible, tc.result)
				}
			})
		})
	}
}

func transactionContributionFixture(t *testing.T) (*peerpolicy.Tracker, *peerpolicy.ConnectionActivity, *mclock.Simulated) {
	t.Helper()
	clock := new(mclock.Simulated)
	tracker := peerpolicy.New(clock)
	tracker.SetReady(true)
	connection := new(peerpolicy.ConnectionActivity)
	connection.Start(clock.Now())
	tracker.BindConnection("peer", tracker.Connected("peer"), connection)
	for range 10 {
		connection.Observe(clock.Now(), false)
		tracker.Observe("peer", peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1}})
		clock.Run(time.Minute)
	}
	tracker.Observe("peer", peerpolicy.Evidence{Reason: peerpolicy.ServingVolume})
	if _, eligible := tracker.RequestOnlySince("peer", connection); !eligible {
		t.Fatal("fixture is not eligible")
	}
	return tracker, connection, clock
}

func TestTransactionContributionStopDuringObservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		f := NewTxFetcher(func(common.Hash) bool { return false }, func([]*types.Transaction) []error { return []error{nil} }, nil, nil)
		f.SetValidationObserver(func(string, DeliveryProvenance, uint64, uint64, bool, uint64, bool, mclock.AbsTime) {
			close(started)
			<-release
		})
		f.Start()
		done := make(chan error, 1)
		go func() { done <- f.Enqueue("peer", testTxs[:1], false, 0) }()
		<-started
		synctest.Wait()
		f.Stop()
		if err := <-done; !errors.Is(err, errTerminated) {
			t.Errorf("enqueue during shutdown = %v, want termination", err)
		}
		close(release)
	})
}
