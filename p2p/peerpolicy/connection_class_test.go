package peerpolicy

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyLiveClassRefreshWithoutTraffic(t *testing.T) {
	for _, firstRead := range []string{"snapshot", "metric"} {
		t.Run(firstRead, func(t *testing.T) {
			tracker := New(new(mclock.Simulated))
			var protected atomic.Bool
			tracker.SetClassResolver(func(string) (bool, bool) { return protected.Load(), true })
			tracker.Connected("peer")
			tracker.Observe("peer", Evidence{Reason: InvalidBlock, Activity: ActivityEvent{ReceivedMessages: 1}})
			registry := metrics.NewRegistry()
			require.NoError(t, tracker.RegisterPeerMetrics(registry))
			defer tracker.UnregisterPeerMetrics()
			require.Contains(t, scrapePeerMetrics(registry), `eth_peerpolicy_peer_risk{peer_id="peer"} 60`)
			require.Contains(t, scrapePeerMetrics(registry), `eth_peerpolicy_peer_protected{peer_id="peer"} 0`)

			protected.Store(true)
			protectedScrape := func() {
				t.Helper()
				got := scrapePeerMetrics(registry)
				require.Contains(t, got, `eth_peerpolicy_peer_risk{peer_id="peer"} 0`)
				require.Contains(t, got, `eth_peerpolicy_peer_protected{peer_id="peer"} 1`)
				require.Contains(t, got, `eth_peerpolicy_connected_risk_count{class="protected"} 1`)
			}
			if firstRead == "metric" {
				protectedScrape()
			}
			got := tracker.Snapshot("peer")
			require.True(t, got.Contribution.Protected)
			require.Zero(t, got.Risk)
			require.Equal(t, uint64(1), got.Contribution.ReceivedMessages)
			protectedScrape()

			protected.Store(false)
			if firstRead == "metric" {
				require.Contains(t, scrapePeerMetrics(registry), `eth_peerpolicy_peer_protected{peer_id="peer"} 0`)
			}
			got = tracker.Snapshot("peer")
			require.False(t, got.Contribution.Protected)
			require.Zero(t, got.Risk, "cleared evidence must not return after trust removal")
			require.Equal(t, uint64(1), got.Contribution.ReceivedMessages)
		})
	}
}

func TestPeerPolicyLiveClassPrecedesStaleEvidenceClass(t *testing.T) {
	for _, path := range []string{"observe", "class", "session", "set-exempt"} {
		t.Run(path, func(t *testing.T) {
			tracker := New(new(mclock.Simulated))
			var protected atomic.Bool
			tracker.SetClassResolver(func(string) (bool, bool) { return protected.Load(), true })
			startedAt := tracker.Connected("peer")
			tracker.Observe("peer", Evidence{Reason: InvalidBlock})
			apply := func(staleClass bool) {
				switch path {
				case "observe":
					tracker.Observe("peer", Evidence{Reason: RateLimited})
				case "class":
					tracker.ObserveWithClass("peer", staleClass, Evidence{Reason: RateLimited})
				case "session":
					require.True(t, tracker.ObserveSession("peer", staleClass, startedAt, Evidence{Reason: RateLimited}))
				case "set-exempt":
					tracker.SetExempt("peer", staleClass)
				}
			}
			apply(true)
			got := tracker.Snapshot("peer")
			require.False(t, got.Contribution.Protected)
			require.Equal(t, uint64(60), got.Risk, "stale exempt=true must not clear ordinary-peer evidence")
			require.Equal(t, uint64(1), got.Windows[InvalidBlock.String()])
			protected.Store(true)
			apply(false)
			got = tracker.Snapshot("peer")
			require.True(t, got.Contribution.Protected)
			require.Zero(t, got.Risk, "stale exempt=false must not score a now-trusted peer")
		})
	}
}

func TestPeerPolicyLiveClassExemptsEvidenceBeforeMetrics(t *testing.T) {
	metrics.Enable()
	for _, path := range []string{"observe", "class", "session"} {
		t.Run(path, func(t *testing.T) {
			tracker := New(new(mclock.Simulated))
			var protected atomic.Bool
			tracker.SetClassResolver(func(string) (bool, bool) { return protected.Load(), true })
			startedAt := tracker.Connected("peer")
			tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1}})
			reasonsBefore := tracker.metrics.reasons[InvalidBlock].Snapshot().Count()
			windowsBefore := tracker.metrics.windows[InvalidBlock].Snapshot().Count()
			bytesBefore := tracker.metrics.bytes[Blocks].Snapshot().Count()
			throttleBefore := tracker.metrics.throttle.Snapshot().Count()

			protected.Store(true)
			event := Evidence{Reason: InvalidBlock, Family: Blocks, Items: 1, Bytes: 512,
				Activity: ActivityEvent{ReceivedMessages: 1, ReceivedBytes: 512}}
			switch path {
			case "observe":
				tracker.Observe("peer", event)
			case "class":
				tracker.ObserveWithClass("peer", false, event)
			case "session":
				require.True(t, tracker.ObserveSession("peer", false, startedAt, event))
			}
			// Check cumulative metrics before any snapshot can refresh the class.
			require.Equal(t, reasonsBefore, tracker.metrics.reasons[InvalidBlock].Snapshot().Count())
			require.Equal(t, windowsBefore, tracker.metrics.windows[InvalidBlock].Snapshot().Count())
			require.Equal(t, bytesBefore, tracker.metrics.bytes[Blocks].Snapshot().Count())
			require.Equal(t, throttleBefore, tracker.metrics.throttle.Snapshot().Count())
			got := tracker.Snapshot("peer")
			require.True(t, got.Contribution.Protected)
			require.Zero(t, got.Risk)
			require.Equal(t, uint64(2), got.Contribution.ReceivedMessages)
			require.Equal(t, uint64(512), got.Contribution.ReceivedBytes)
		})
	}
}

func TestPeerPolicyLiveClassRetainedAfterDisconnect(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	connected, protected := true, false
	tracker.SetClassResolver(func(string) (bool, bool) { return protected, connected })
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	protected = true
	tracker.RefreshClass("peer")
	connected = false
	protected = false // An absent lookup's class value has no meaning.
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	got := tracker.Snapshot("peer")
	require.True(t, got.Contribution.Protected)
	require.Zero(t, got.Risk)

	tracker.SetClassResolver(nil)
	tracker.ObserveWithClass("peer", false, Evidence{Reason: RateLimited})
	got = tracker.Snapshot("peer")
	require.False(t, got.Contribution.Protected)
	require.Equal(t, uint64(20), got.Risk, "trackers without a resolver must honor the caller's class")
}

func TestPeerPolicyLiveClassDelayedDisconnectedEvidence(t *testing.T) {
	for _, path := range []string{"class", "session"} {
		t.Run(path, func(t *testing.T) {
			tracker := New(new(mclock.Simulated))
			connected, protected := true, false
			tracker.SetClassResolver(func(string) (bool, bool) { return protected, connected })
			startedAt := tracker.Connected("peer")
			tracker.Observe("peer", Evidence{Reason: InvalidBlock})
			protected = true
			tracker.RefreshClass("peer")
			connected = false
			if path == "class" {
				tracker.ObserveWithClass("peer", false, Evidence{Reason: InvalidBlock})
			} else {
				require.True(t, tracker.ObserveSession("peer", false, startedAt, Evidence{Reason: InvalidBlock}))
			}
			got := tracker.Snapshot("peer")
			require.True(t, got.Contribution.Protected)
			require.Zero(t, got.Risk, "delayed ordinary class must not undo final trust")
		})
	}
}

func TestPeerPolicyLiveClassBeforeRegistrationAndAfterEviction(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	tracker.SetClassResolver(func(string) (bool, bool) { return false, false })
	startedAt := tracker.Connected("peer")
	tracker.SetExempt("peer", true) // Authenticated class before peerset registration.
	require.True(t, tracker.ObserveSession("peer", false, startedAt, Evidence{Reason: InvalidBlock}))
	require.True(t, tracker.Snapshot("peer").Contribution.Protected)
	require.Zero(t, tracker.Snapshot("peer").Risk)
	for i := range maxPeers {
		tracker.Observe(fmt.Sprint(i), Evidence{})
	}
	require.False(t, tracker.ObserveSession("peer", true, startedAt, Evidence{Reason: InvalidBlock}))
	tracker.ObserveWithClass("peer", true, Evidence{Reason: InvalidBlock})
	got := tracker.Snapshot("peer")
	require.False(t, got.Contribution.Protected, "a stale connection flag cannot grant privilege after eviction")
	require.False(t, got.Contribution.ConnectionKnown)
	require.Equal(t, uint64(60), got.Risk)
}

func TestPeerPolicyLiveClassReadsPreserveRetention(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	for i := range maxPeers {
		tracker.Observe(fmt.Sprint(i), Evidence{Reason: RateLimited})
	}
	tracker.SetClassResolver(func(string) (bool, bool) { return true, true })
	tracker.Snapshot("missing")
	tracker.RefreshClass("missing")
	require.Equal(t, maxPeers, tracker.peers.Len())
	_, exists := tracker.peers.Peek("missing")
	require.False(t, exists, "administrative reads must not create records")
	tracker.Snapshot("0")
	tracker.RefreshClass("0")
	require.Empty(t, tracker.peerRisks())
	oldest, _, exists := tracker.peers.GetOldest()
	require.True(t, exists)
	require.Equal(t, "0", oldest, "class refresh must not promote LRU records")
	tracker.Observe("new", Evidence{Reason: InvalidBlock})
	_, exists = tracker.peers.Peek("0")
	require.False(t, exists)
}

func TestPeerPolicyLiveClassConcurrentChanges(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	var protected atomic.Bool
	tracker.SetClassResolver(func(string) (bool, bool) { return protected.Load(), true })
	startedAt := tracker.Connected("peer")
	var wg sync.WaitGroup
	for worker := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				switch worker {
				case 0:
					protected.Store(i%2 == 0)
				case 1:
					tracker.ObserveSession("peer", false, startedAt, Evidence{Reason: InvalidBlock})
				case 2:
					tracker.Snapshot("peer")
					tracker.peerRisks()
				}
			}
		}()
	}
	wg.Wait()
	protected.Store(true)
	require.Equal(t, map[string]int64{"peer": 0}, tracker.peerRisks())
	require.True(t, tracker.Snapshot("peer").Contribution.Protected)
}
