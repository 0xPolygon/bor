package peerpolicy

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
)

func requestOnlyFixture() (*Tracker, *ConnectionActivity, *mclock.Simulated) {
	tracker, clock := newContributionTracker()
	connection := new(ConnectionActivity)
	connection.Start(clock.Now())
	tracker.BindConnection("peer", 0, connection)
	for range 10 {
		connection.Observe(clock.Now(), false)
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1}})
		clock.Run(time.Minute)
	}
	tracker.Observe("peer", Evidence{Reason: ServingVolume})
	return tracker, connection, clock
}

func TestRequestOnlyGraceAndFreshEvidence(t *testing.T) {
	tracker, connection, clock := requestOnlyFixture()
	last, eligible := tracker.RequestOnlySince("peer", connection)
	if !eligible {
		t.Fatal("request-only peer did not qualify")
	}
	sequence := connection.ObserveData(clock.Now())
	if tracker.RetireRequestOnly("peer", connection, last) {
		t.Fatal("ignored traffic arriving after selection")
	}
	if _, eligible := tracker.RequestOnlySince("peer", connection); eligible {
		t.Fatal("peer was eligible while a wire message was still unclassified")
	}
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, MessageSequence: sequence}})
	last, eligible = tracker.RequestOnlySince("peer", connection)
	if !eligible {
		t.Fatal("handled data kept the peer ineligible without contribution")
	}
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, UsefulBlock: true}})
	if tracker.RetireRequestOnly("peer", connection, last) {
		t.Fatal("ignored data arriving after selection")
	}
}

func TestRequestOnlyRechecksSessionAndReadiness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Tracker, *ConnectionActivity, *mclock.Simulated)
	}{
		{"syncing", func(t *Tracker, _ *ConnectionActivity, _ *mclock.Simulated) { t.SetReady(false) }},
		{"protected", func(t *Tracker, _ *ConnectionActivity, _ *mclock.Simulated) { t.SetExempt("peer", true) }},
		{"auxiliary", func(_ *Tracker, c *ConnectionActivity, clock *mclock.Simulated) { c.Observe(clock.Now(), true) }},
		{"replaced", func(t *Tracker, _ *ConnectionActivity, clock *mclock.Simulated) {
			newConnection := new(ConnectionActivity)
			newConnection.Start(clock.Now())
			t.BindConnection("peer", t.Connected("peer"), newConnection)
		}},
		{"contributing", func(t *Tracker, _ *ConnectionActivity, _ *mclock.Simulated) {
			t.Observe("peer", Evidence{Activity: ActivityEvent{AcceptedTransactions: 1}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker, connection, clock := requestOnlyFixture()
			last, eligible := tracker.RequestOnlySince("peer", connection)
			if !eligible {
				t.Fatal("fixture is not eligible")
			}
			tc.change(tracker, connection, clock)
			if tracker.RetireRequestOnly("peer", connection, last) {
				t.Fatal("retired a peer after eligibility changed")
			}
		})
	}
}

func TestRequestOnlyMinimumObservationAndServing(t *testing.T) {
	tracker, clock := newContributionTracker()
	var connection ConnectionActivity
	connection.Start(clock.Now())
	tracker.BindConnection("peer", 0, &connection)
	for range 10 {
		if _, ok := tracker.RequestOnlySince("peer", &connection); ok {
			t.Fatal("removed before grace")
		}
		connection.Observe(clock.Now(), false)
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1}})
		clock.Run(time.Minute)
	}
	tracker.Observe("peer", Evidence{Reason: ServingVolume})
	last, ok := tracker.RequestOnlySince("peer", &connection)
	if !ok || !tracker.RetireRequestOnly("peer", &connection, last) {
		t.Fatal("eligible peer could not be retired")
	}
}

func TestRequestOnlyServedMinutesAfterGrace(t *testing.T) {
	tracker, clock := newContributionTracker()
	var connection ConnectionActivity
	connection.Start(clock.Now())
	tracker.BindConnection("peer", 0, &connection)
	clock.Run(10 * time.Minute)

	for minute := 1; minute <= 10; minute++ {
		for range 2 {
			connection.Observe(clock.Now(), false)
			tracker.Observe("peer", Evidence{Activity: ActivityEvent{
				ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1,
			}})
		}
		tracker.Observe("peer", Evidence{Reason: ServingVolume})
		_, eligible := tracker.RequestOnlySince("peer", &connection)
		if want := minute == 10; eligible != want {
			t.Fatalf("eligible after %d served minute buckets = %v, want %v", minute, eligible, want)
		}
		clock.Run(time.Minute)
	}
}

func TestRequestOnlyBusyTracker(t *testing.T) {
	tracker, connection, _ := requestOnlyFixture()
	last, eligible := tracker.RequestOnlySince("peer", connection)
	if !eligible {
		t.Fatal("fixture is not eligible")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if _, ok := tracker.RequestOnlySince("peer", connection); ok {
		t.Fatal("selected a peer without its activity record")
	}
	if tracker.RetireRequestOnly("peer", connection, last) {
		t.Fatal("retired a peer without its activity record")
	}
}

func TestRequestOnlyResumedObservation(t *testing.T) {
	tracker, connection, clock := requestOnlyFixture()
	tracker.SetReady(false)
	clock.Run(2 * time.Minute)
	tracker.SetReady(true)
	if _, ok := tracker.RequestOnlySince("peer", connection); ok {
		t.Fatal("did not restart grace after a long sync gap")
	}
}

func TestRequestOnlyNeedsLiveServingEvidence(t *testing.T) {
	tracker, connection, clock := requestOnlyFixture()
	clock.Run(windowCount * windowWidth)
	connection.Observe(clock.Now(), false)
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1}})
	for _, reason := range []Reason{None, ServingRefused, DownloaderFailure, AnnouncementVolume} {
		tracker.Observe("peer", Evidence{Reason: reason})
		if _, ok := tracker.RequestOnlySince("peer", connection); ok {
			t.Fatalf("selected a syncing peer with only %v evidence", reason)
		}
	}
	for range 3 {
		tracker.Observe("peer", Evidence{Refused: true})
	}
	last, ok := tracker.RequestOnlySince("peer", connection)
	if !ok {
		t.Fatal("an announcement masked repeated serving refusals")
	}
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{UsefulBlock: true}})
	if tracker.RetireRequestOnly("peer", connection, last) {
		t.Fatal("retired a peer that supplied a useful block after selection")
	}
}

func TestRequestOnlyNeedsObservedRequest(t *testing.T) {
	tracker, clock := newContributionTracker()
	var connection ConnectionActivity
	connection.Start(clock.Now())
	tracker.BindConnection("peer", 0, &connection)
	for range 10 {
		clock.Run(time.Minute)
		connection.Observe(clock.Now(), false)
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{
			ReceivedMessages: 1, ServedResponses: 1,
		}})
	}
	tracker.Observe("peer", Evidence{Reason: ServingVolume})
	if _, eligible := tracker.RequestOnlySince("peer", &connection); eligible {
		t.Fatal("selected a peer without observing a request")
	}
}

func TestRequestOnlyRechecksServingLimits(t *testing.T) {
	for _, reason := range []Reason{RequestVolume, ServingVolume, ServingRepetition} {
		t.Run(reason.String(), func(t *testing.T) {
			tracker, connection, clock := requestOnlyFixture()
			clock.Run(windowCount * windowWidth)
			tracker.Observe("peer", Evidence{Reason: reason})
			last, eligible := tracker.RequestOnlySince("peer", connection)
			if !eligible {
				t.Fatal("missed recent serving evidence")
			}
			clock.Run(windowCount * windowWidth)
			if tracker.RetireRequestOnly("peer", connection, last) {
				t.Fatal("retired a peer using expired serving evidence")
			}
		})
	}
}

func TestRequestOnlyContributionWindow(t *testing.T) {
	tracker, connection, clock := requestOnlyFixture()
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{UsefulBlock: true}})
	for minute := 0; minute <= activityCount; minute++ {
		connection.Observe(clock.Now(), false)
		tracker.Observe("peer", Evidence{Reason: ServingVolume, Activity: ActivityEvent{
			ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1,
		}})
		if _, eligible := tracker.RequestOnlySince("peer", connection); eligible != (minute == activityCount) {
			t.Fatalf("eligibility at minute %d = %v", minute, eligible)
		}
		clock.Run(time.Minute)
	}
}

func TestRequestOnlyRetireWithPendingRequest(t *testing.T) {
	tracker, connection, clock := requestOnlyFixture()
	last, eligible := tracker.RequestOnlySince("peer", connection)
	if !eligible {
		t.Fatal("fixture is not eligible")
	}
	clock.Run(time.Second)
	connection.Observe(clock.Now(), false)
	if !tracker.RetireRequestOnly("peer", connection, last) {
		t.Fatal("a pending request prevented rotation")
	}
}

func TestRequestOnlyPendingDataAtSameClockTick(t *testing.T) {
	tracker, connection, clock := requestOnlyFixture()
	connection.Observe(clock.Now(), false)
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1}})
	last, eligible := tracker.RequestOnlySince("peer", connection)
	if !eligible {
		t.Fatal("fixture is not eligible")
	}
	connection.ObserveData(clock.Now())
	if tracker.RetireRequestOnly("peer", connection, last) {
		t.Fatal("retired a peer with pending data at the same clock tick")
	}
}

func TestRequestOnlyDataCompletionOrder(t *testing.T) {
	for _, useful := range []bool{false, true} {
		tracker, connection, clock := requestOnlyFixture()
		first := connection.ObserveData(clock.Now())
		second := connection.ObserveData(clock.Now())
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, MessageSequence: first}})
		if _, eligible := tracker.RequestOnlySince("peer", connection); eligible {
			t.Fatal("handling the first delivery cleared a newer pending delivery")
		}
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, MessageSequence: second, UsefulBlock: useful}})
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, MessageSequence: first}})
		last, eligible := tracker.RequestOnlySince("peer", connection)
		if eligible == useful {
			t.Fatalf("eligibility after completion = %v, useful = %v", eligible, useful)
		}
		if tracker.RetireRequestOnly("peer", connection, last) == useful {
			t.Fatal("retirement ignored the completed validation result")
		}
	}
}

func TestRequestOnlyIgnoresPreviousSessionCompletion(t *testing.T) {
	tracker, _, clock := requestOnlyFixture()
	clock.Run(time.Nanosecond)
	startedAt := tracker.Connected("peer")
	var connection ConnectionActivity
	connection.Start(clock.Now())
	tracker.BindConnection("peer", startedAt, &connection)
	for range 10 {
		sequence := connection.Observe(clock.Now(), false)
		tracker.ObserveSession("peer", false, startedAt, Evidence{Activity: ActivityEvent{
			ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, MessageSequence: sequence,
		}})
		clock.Run(time.Minute)
	}
	tracker.Observe("peer", Evidence{Reason: ServingVolume})
	sequence := connection.ObserveData(clock.Now())
	tracker.ObserveSession("peer", false, 0, Evidence{Activity: ActivityEvent{ReceivedMessages: 1, MessageSequence: sequence + 1}})
	if _, eligible := tracker.RequestOnlySince("peer", &connection); eligible {
		t.Fatal("a stale completion cleared the new session's pending data")
	}
	if !tracker.ObserveSession("peer", false, startedAt, Evidence{Activity: ActivityEvent{ReceivedMessages: 1, MessageSequence: sequence}}) {
		t.Fatal("rejected completion from the current session")
	}
	if _, eligible := tracker.RequestOnlySince("peer", &connection); !eligible {
		t.Fatal("current completion did not release the pending data guard")
	}
}
