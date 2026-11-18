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
		connection.ObserveRequest(clock.Now())
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, MessageAt: clock.Now()}})
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
	connection.Observe(clock.Now(), false)
	if tracker.RetireRequestOnly("peer", connection, last) {
		t.Fatal("ignored traffic arriving after selection")
	}
	if _, eligible := tracker.RequestOnlySince("peer", connection); eligible {
		t.Fatal("peer was eligible while a wire message was still unclassified")
	}
	clock.Run(time.Nanosecond)
	connection.ObserveRequest(clock.Now())
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1, MessageAt: clock.Now()}})
	last, eligible = tracker.RequestOnlySince("peer", connection)
	if !eligible {
		t.Fatal("a new request reset contribution eligibility")
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
		connection.ObserveRequest(clock.Now())
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, MessageAt: clock.Now()}})
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
			connection.ObserveRequest(clock.Now())
			tracker.Observe("peer", Evidence{Activity: ActivityEvent{
				ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, MessageAt: clock.Now(),
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
	connection.ObserveRequest(clock.Now())
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, MessageAt: clock.Now()}})
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
		connection.ObserveRequest(clock.Now())
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{
			ReceivedMessages: 1, ServedResponses: 1, MessageAt: clock.Now(),
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
		connection.ObserveRequest(clock.Now())
		tracker.Observe("peer", Evidence{Reason: ServingVolume, Activity: ActivityEvent{
			ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, MessageAt: clock.Now(),
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
	connection.ObserveRequest(clock.Now())
	if !tracker.RetireRequestOnly("peer", connection, last) {
		t.Fatal("a pending request prevented rotation")
	}
}

func TestRequestOnlyPendingDataAtHandledTimestamp(t *testing.T) {
	tracker, connection, clock := requestOnlyFixture()
	connection.ObserveRequest(clock.Now())
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1, MessageAt: clock.Now()}})
	last, eligible := tracker.RequestOnlySince("peer", connection)
	if !eligible {
		t.Fatal("fixture is not eligible")
	}
	connection.Observe(clock.Now(), false)
	if tracker.RetireRequestOnly("peer", connection, last) {
		t.Fatal("retired a peer with pending data at the same clock tick")
	}
}
