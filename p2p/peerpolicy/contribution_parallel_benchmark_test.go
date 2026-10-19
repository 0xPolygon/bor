package peerpolicy

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
)

func BenchmarkPeerContributionParallel(b *testing.B) {
	metrics.Enable()
	for _, peers := range []int{32, 256, maxPeers} {
		for _, path := range []string{"activity-only", "risk-only", "risk-and-activity"} {
			b.Run(fmt.Sprintf("peers=%d/%s", peers, path), func(b *testing.B) {
				benchmarkContributionPath(b, peers, path)
			})
		}
	}
}

func benchmarkContributionPath(b *testing.B, peers int, path string) {
	tracker := New(new(mclock.Simulated))
	tracker.SetReady(true)
	event := contributionBenchmarkEvent(path)
	ids := make([]string, peers)
	sessions := make([]mclock.AbsTime, peers)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
		sessions[i] = tracker.Connected(ids[i])
		for range 4 {
			tracker.ObserveSession(ids[i], false, sessions[i], event)
		}
	}
	var nextWorker atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Only worker startup shares an atomic; observations use local round-robin.
		i := int(nextWorker.Add(1)-1) % peers
		if path == "risk-only" {
			for pb.Next() {
				tracker.ObserveWithClass(ids[i], false, event)
				i = (i + 1) % peers
			}
			return
		}
		for pb.Next() {
			tracker.ObserveSession(ids[i], false, sessions[i], event)
			i = (i + 1) % peers
		}
	})
}

func contributionBenchmarkEvent(path string) Evidence {
	var event Evidence
	if path != "risk-only" {
		event.Activity = ActivityEvent{ReceivedMessages: 1, ReceivedBytes: 1024, ServedResponses: 1, ServedBytes: 65536}
	}
	if path != "activity-only" {
		event.Family, event.Items, event.Bytes = TransactionAnnouncements, 32, 1024
		event.Hashes = make([]common.Hash, 32)
		for i := range event.Hashes {
			event.Hashes[i][0] = byte(i)
		}
	}
	return event
}
