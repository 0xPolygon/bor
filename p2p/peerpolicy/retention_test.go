package peerpolicy

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyQuietLiveSessionSurvivesChurn(t *testing.T) {
	tracker, clock := newContributionTracker()
	live := map[string]bool{"peer": true}
	tracker.SetClassResolver(func(id string) (bool, bool) { return false, live[id] })
	original, _ := tracker.peers.Peek("peer")
	for i := range 2 * maxPeers {
		id := fmt.Sprint(i)
		live[id] = true
		tracker.Connected(id)
		delete(live, id)
	}
	clock.Run(activityGrace)
	got := contributionStatus(t, tracker, "idle")
	require.Equal(t, uint64(600), got.ConnectionSeconds)
	require.Equal(t, uint64(600), got.ObservedSeconds)
	require.Equal(t, uint64(behaviorRisk), got.Risk)
	retained, _ := tracker.peers.Peek("peer")
	require.Same(t, original, retained)
	require.Equal(t, maxPeers, tracker.peers.Len())
	require.False(t, tracker.peers.Contains("0"))
	require.True(t, tracker.ObserveSession("peer", false, 0, Evidence{Activity: ActivityEvent{ReceivedMessages: 1}}))
	require.Equal(t, uint64(1), tracker.Snapshot("peer").Contribution.ReceivedMessages)
}

func TestPeerPolicyAllLiveCapacity(t *testing.T) {
	for _, entrypoint := range []string{"observe", "class", "set-exempt", "connected", "restore"} {
		t.Run(entrypoint, func(t *testing.T) {
			tracker := New(new(mclock.Simulated))
			live := make(map[string]bool, maxPeers)
			tracker.SetClassResolver(func(id string) (bool, bool) { return false, live[id] })
			for i := range maxPeers {
				id := fmt.Sprint(i)
				live[id] = true
				tracker.Connected(id)
			}
			before := tracker.metrics.evictions.Snapshot().Count()
			switch entrypoint {
			case "observe":
				tracker.Observe("new", Evidence{Reason: InvalidBlock})
			case "class":
				tracker.ObserveWithClass("new", false, Evidence{Reason: InvalidBlock})
			case "set-exempt":
				tracker.SetExempt("new", true)
			case "connected":
				tracker.Connected("new")
			case "restore":
				tracker.RestoreSession("new", 0, func() bool { return true })
			}
			require.False(t, tracker.peers.Contains("new"))
			require.Equal(t, maxPeers, tracker.peers.Len())
			require.Equal(t, before, tracker.metrics.evictions.Snapshot().Count())
			oldest, _, _ := tracker.peers.GetOldest()
			require.Equal(t, "0", oldest)
			live["0"] = false
			tracker.Connected("new")
			require.True(t, tracker.Snapshot("new").Contribution.ConnectionKnown)
			require.False(t, tracker.peers.Contains("0"))
			require.Equal(t, maxPeers, tracker.peers.Len())
		})
	}
}

func TestPeerPolicyRestoreSessionCoverage(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			clock := new(mclock.Simulated)
			tracker := New(clock)
			tracker.SetReady(true)
			if existing {
				tracker.Observe("peer", Evidence{Reason: InvalidBlock, Activity: ActivityEvent{ReceivedMessages: 7}})
			}
			clock.Run(activityGrace)
			tracker.RestoreSession("peer", 0, func() bool { return true })
			got := contributionStatus(t, tracker, "warming-up")
			require.Equal(t, uint64(600), got.ConnectionSeconds)
			require.Zero(t, got.ObservedSeconds)
			require.Zero(t, got.ReceivedMessages)
			if existing {
				peer, _ := tracker.peers.Peek("peer")
				require.Equal(t, uint64(60), peer.risk(0, nil))
			}
			require.True(t, tracker.ObserveSession("peer", false, 0,
				Evidence{Activity: ActivityEvent{ReceivedMessages: 1, AcceptedTransactions: 1}}))
			got = contributionStatus(t, tracker, "warming-up")
			require.Equal(t, uint64(1), got.ReceivedMessages)
			require.Zero(t, got.AcceptedTransactions)
			require.Equal(t, uint64(1), got.UnclassifiedMessages)
		})
	}
}

func TestPeerPolicyRestoreRequiresCurrentSession(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(time.Second)
	tracker.Connected("peer")
	before := tracker.Snapshot("peer")
	tracker.RestoreSession("peer", 0, func() bool { return true })
	require.Equal(t, before, tracker.Snapshot("peer"))
	for _, id := range []string{"missing", "peer"} {
		tracker.RestoreSession(id, 0, func() bool { return false })
	}
	require.False(t, tracker.peers.Contains("missing"))
	require.Equal(t, before, tracker.Snapshot("peer"))
}

func TestPeerPolicyRestoreSessionInputBounds(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	for _, id := range []string{"", strings.Repeat("a", 129)} {
		tracker.RestoreSession(id, 0, func() bool { t.Fatal("invalid ID resolved"); return true })
	}
	tracker.RestoreSession("peer", 0, nil)
	require.Zero(t, tracker.peers.Len())
	tracker.RestoreSession(strings.Repeat("a", 128), 0, func() bool { return true })
	require.Equal(t, 1, tracker.peers.Len())
}
