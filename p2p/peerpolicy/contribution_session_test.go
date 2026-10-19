package peerpolicy

import (
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
)

func TestContributionStaleSessionPreservesClass(t *testing.T) {
	for _, protected := range []bool{false, true} {
		tracker, clock := newContributionTracker()
		tracker.SetExempt("peer", !protected)
		clock.Run(time.Second)
		started := tracker.Connected("peer")
		tracker.SetExempt("peer", protected)
		tracker.Observe("peer", Evidence{Reason: InvalidBlock})
		if !tracker.ObserveSession("peer", !protected, 0, Evidence{Reason: LegacyJail}) {
			t.Fatal("stale diagnostic not consumed")
		}
		if !tracker.ObserveSession("peer", !protected, 0, Evidence{Reason: InvalidBlock,
			Activity: ActivityEvent{AcceptedTransactions: 1, ServedResponses: 1}}) {
			t.Fatal("stale activity not consumed")
		}
		got := tracker.Snapshot("peer")
		wantRisk := uint64(60)
		if protected {
			wantRisk = 0
		}
		if got.Risk != wantRisk || got.Contribution.Protected != protected {
			t.Fatalf("stale session changed class/risk: %+v, contribution=%+v", got, got.Contribution)
		}
		if got.Contribution.AcceptedTransactions != 0 || got.Contribution.ServedResponses != 0 {
			t.Fatalf("stale session credited contribution: %+v", got.Contribution)
		}
		if !tracker.ObserveSession("peer", protected, started, Evidence{Activity: ActivityEvent{ServedResponses: 1}}) {
			t.Fatal("current session not consumed")
		}
		if tracker.Snapshot("peer").Contribution.ServedResponses != 1 {
			t.Fatal("current session was not timestamped")
		}
	}
}

func TestContributionCurrentSessionUpdatesClass(t *testing.T) {
	tracker, _ := newContributionTracker()
	tracker.ObserveSession("peer", true, 0, Evidence{Reason: InvalidBlock, Activity: ActivityEvent{ServedResponses: 1}})
	got := tracker.Snapshot("peer")
	if got.Risk != 0 || !got.Contribution.Protected || got.Contribution.ServedResponses != 1 {
		t.Fatalf("current session class not applied: %+v, contribution=%+v", got, got.Contribution)
	}
	tracker.ObserveSession("peer", false, 0, Evidence{Reason: InvalidBlock})
	got = tracker.Snapshot("peer")
	if got.Risk != 60 || got.Contribution.Protected {
		t.Fatalf("current session class not updated: %+v, contribution=%+v", got, got.Contribution)
	}
}

func TestContributionUnregisteredSessionRetainsClass(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	tracker.SetExempt("peer", true)
	if tracker.ObserveSession("peer", false, 0, Evidence{Reason: InvalidBlock}) {
		t.Fatal("unregistered session consumed instead of requesting current class")
	}
	got := tracker.Snapshot("peer")
	if got.Risk != 0 || !got.Contribution.Protected || got.Contribution.Status != "unknown" {
		t.Fatalf("unregistered session replaced retained class: %+v, contribution=%+v", got, got.Contribution)
	}
}

func TestContributionMissingSessionIsNotRecorded(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	if tracker.ObserveSession("peer", true, 0, Evidence{Reason: InvalidBlock, Activity: ActivityEvent{ServedResponses: 1}}) {
		t.Fatal("missing session consumed")
	}
	if tracker.peers.Len() != 0 {
		t.Fatal("missing session created a record")
	}
	tracker.Observe("peer", Evidence{Reason: RateLimited})
	tracker.Observe("other", Evidence{})
	if tracker.ObserveSession("peer", true, 0, Evidence{Reason: InvalidBlock, Activity: ActivityEvent{ServedResponses: 1}}) {
		t.Fatal("unregistered session consumed")
	}
	got := tracker.Snapshot("peer")
	if got.Risk != 20 || got.Contribution.ServedResponses != 0 || got.Contribution.Protected {
		t.Fatalf("unregistered session was modified: %+v, contribution=%+v", got, got.Contribution)
	}
	if oldest, _, ok := tracker.peers.GetOldest(); !ok || oldest != "peer" {
		t.Fatal("unregistered session changed retention order")
	}
}

func TestContributionSessionRejectsInvalidEvidence(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	for _, test := range []struct {
		id    string
		event Evidence
	}{
		{"", Evidence{}}, {strings.Repeat("a", 129), Evidence{}},
		{"peer", Evidence{Reason: reasonCount}}, {"peer", Evidence{Family: familyCount}},
	} {
		if !tracker.ObserveSession(test.id, false, 0, test.event) {
			t.Fatal("invalid evidence requested fallback")
		}
	}
	if tracker.peers.Len() != 0 {
		t.Fatal("invalid session evidence retained")
	}
	tracker.Connected(strings.Repeat("a", 128))
	if !tracker.ObserveSession(strings.Repeat("a", 128), false, 0, Evidence{Reason: InvalidBlock}) {
		t.Fatal("maximum valid ID rejected")
	}
	if tracker.peers.Len() != 1 {
		t.Fatal("maximum valid ID rejected")
	}
	if tracker.Snapshot(strings.Repeat("a", 128)).Risk != 60 {
		t.Fatal("maximum valid ID did not retain its evidence")
	}
}

func TestContributionConnectionKnowledge(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	if got := tracker.Snapshot("peer").Contribution; got.ConnectionKnown || got.ConnectionSeconds != 0 {
		t.Fatalf("missing peer has known connection: %+v", got)
	}
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1}})
	if tracker.Snapshot("peer").Contribution.ConnectionKnown {
		t.Fatal("activity alone registered a connection")
	}
	tracker.Connected("peer")
	got := tracker.Snapshot("peer").Contribution
	if !got.ConnectionKnown || got.ConnectionSeconds != 0 {
		t.Fatalf("new connection age is unknown: %+v", got)
	}
	clock.Run(time.Minute)
	got = tracker.Snapshot("peer").Contribution
	if !got.ConnectionKnown || got.ConnectionSeconds != 60 {
		t.Fatalf("registered connection age lost: %+v", got)
	}
}
