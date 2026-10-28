package peerpolicy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyAdmittedObservation(t *testing.T) {
	metrics.Enable()
	tracker := New(new(mclock.Simulated))
	for _, id := range []string{"", strings.Repeat("x", 129), "unknown"} {
		tracker.ObserveAdmitted(id, InvalidBlock)
		require.Empty(t, tracker.Snapshot(id).Windows)
	}
	tracker.Observe("unadmitted", Evidence{Reason: RateLimited})
	tracker.ObserveAdmitted("unadmitted", InvalidBlock)
	require.Equal(t, uint64(20), tracker.Snapshot("unadmitted").Risk)
	longID := strings.Repeat("x", 128)
	tracker.Connected(longID)
	tracker.ObserveAdmitted(longID, InvalidBlock)
	require.Equal(t, uint64(60), tracker.Snapshot(longID).Risk)
	tracker.Connected("admitted")
	tracker.ObserveAdmitted("admitted", reasonCount)
	require.Empty(t, tracker.Snapshot("admitted").Windows)
	tracker.ObserveAdmitted("admitted", InvalidBlock)
	require.Equal(t, uint64(60), tracker.Snapshot("admitted").Risk)
	tracker.SetClassResolver(func(string) (bool, bool) { return true, true })
	before := tracker.metrics.reasons[InvalidBlock].Snapshot().Count()
	tracker.ObserveAdmitted("admitted", InvalidBlock)
	require.Equal(t, before, tracker.metrics.reasons[InvalidBlock].Snapshot().Count())
	require.Zero(t, tracker.Snapshot("admitted").Risk)
}

func TestPeerPolicyAdmittedObservationRetainsRecentHistory(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	tracker.Connected("peer")
	for i := range maxPeers - 1 {
		tracker.Observe(fmt.Sprint(i), Evidence{Reason: RateLimited})
	}
	tracker.ObserveAdmitted("peer", InvalidBlock)
	tracker.Observe("new", Evidence{Reason: RateLimited})
	require.Equal(t, uint64(60), tracker.Snapshot("peer").Risk)
	require.Zero(t, tracker.Snapshot("0").Risk)
}
