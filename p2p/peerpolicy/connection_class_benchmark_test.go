package peerpolicy

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
)

func BenchmarkPeerPolicyClassResolver(b *testing.B) {
	metrics.Enable()
	for _, peers := range []int{32, 256, maxPeers} {
		for _, path := range []string{"activity-only", "risk-and-activity"} {
			for _, live := range []bool{false, true} {
				b.Run(fmt.Sprintf("peers=%d/%s/live=%t", peers, path, live), func(b *testing.B) {
					benchmarkClassResolver(b, peers, path, live)
				})
			}
		}
	}
}

func benchmarkClassResolver(b *testing.B, peers int, path string, live bool) {
	tracker := New(new(mclock.Simulated))
	var peersMu sync.RWMutex
	classes := make(map[string]*atomic.Uint32, peers)
	ids := make([]string, peers)
	sessions := make([]mclock.AbsTime, peers)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
		classes[ids[i]] = new(atomic.Uint32)
		sessions[i] = tracker.Connected(ids[i])
	}
	if live {
		// Match the handler's peerset lookup followed by atomic Trusted/Static reads.
		tracker.SetClassResolver(func(id string) (bool, bool) {
			peersMu.RLock()
			flags := classes[id]
			peersMu.RUnlock()
			if flags == nil {
				return false, false
			}
			return flags.Load()&1 != 0 || flags.Load()&2 != 0, true
		})
	}
	event := contributionBenchmarkEvent(path)
	for i := range ids {
		for range 4 {
			tracker.ObserveSession(ids[i], false, sessions[i], event)
		}
	}
	var nextWorker atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := int(nextWorker.Add(1)-1) % peers
		for pb.Next() {
			tracker.ObserveSession(ids[i], false, sessions[i], event)
			i = (i + 1) % peers
		}
	})
}
