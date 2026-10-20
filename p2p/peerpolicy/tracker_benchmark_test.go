package peerpolicy

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
)

func BenchmarkPeerPolicyObserveParallel(b *testing.B) {
	metrics.Enable()
	for _, peers := range []int{32, 256, maxPeers} {
		b.Run(fmt.Sprintf("peers=%d", peers), func(b *testing.B) {
			tracker := New(new(mclock.Simulated))
			ids := make([]string, peers)
			for i := range ids {
				ids[i] = fmt.Sprint(i)
				tracker.SetExempt(ids[i], false)
			}
			event := Evidence{Family: TransactionAnnouncements, Items: 32, Bytes: 1024, Hashes: make([]common.Hash, 32)}
			for i := range event.Hashes {
				event.Hashes[i][0] = byte(i)
			}
			var next atomic.Uint64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := int(next.Add(1)) % peers
				for pb.Next() {
					tracker.ObserveWithClass(ids[i], false, event)
					i = (i + 1) % peers
				}
			})
			b.ReportMetric(float64(reflect.TypeOf(peerRecord{}).Size()), "record-B")
		})
	}
}

// Compare the sampling cap with a large hash list that fits one ~10 MiB packet.
var announcementSizes = []int{4096, 260000}

func announcementHashes(count int) []common.Hash {
	hashes := make([]common.Hash, count)
	for i := range hashes {
		binary.BigEndian.PutUint64(hashes[i][:8], uint64(i))
	}

	return hashes
}

func announcementEvent(count int) Evidence {
	hashes := announcementHashes(count)

	return Evidence{Family: TransactionAnnouncements, Hashes: hashes, Items: uint64(count), Bytes: uint64(count) * 40}
}

func BenchmarkPeerPolicyAnnouncements(b *testing.B) {
	metrics.Enable()

	for _, count := range announcementSizes {
		b.Run(fmt.Sprintf("hashes=%d", count), func(b *testing.B) {
			tracker := New(new(mclock.Simulated))
			event := announcementEvent(count)
			tracker.Observe("peer", event)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				tracker.Observe("peer", event)
			}
		})
	}
}

// This measures mixed throughput, not announcement latency or mutex wait time.
func BenchmarkPeerPolicyAnnouncementsParallelMixed(b *testing.B) {
	metrics.Enable()

	for _, count := range announcementSizes {
		b.Run(fmt.Sprintf("hashes=%d", count), func(b *testing.B) {
			benchmarkMixedAnnouncementTraffic(b, count)
		})
	}
}

func benchmarkMixedAnnouncementTraffic(b *testing.B, count int) {
	const peers = 64

	tracker := New(new(mclock.Simulated))
	tracker.SetReady(true)

	ids := make([]string, peers)
	sessions := make([]mclock.AbsTime, peers)

	for i := range ids {
		ids[i] = fmt.Sprint(i)
		sessions[i] = tracker.Connected(ids[i])
	}

	announcement := announcementEvent(count)
	activity := Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ReceivedBytes: 1024}}

	var nextWorker atomic.Uint64

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		worker := int(nextWorker.Add(1) - 1)
		i := worker % peers
		// One worker announces, the rest run cheap activity-only observations.
		for pb.Next() {
			if worker == 0 {
				tracker.ObserveWithClass(ids[i], false, announcement)
			} else {
				tracker.ObserveSession(ids[i], false, sessions[i], activity)
			}

			i = (i + 1) % peers
		}
	})
}

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

func BenchmarkPeerContributionObserve(b *testing.B) {
	tracker := New(new(mclock.Simulated))
	started := tracker.Connected("peer")
	tracker.SetReady(true)
	event := Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ReceivedBytes: 1024, ServedResponses: 1, ServedBytes: 65536}}
	tracker.ObserveSession("peer", false, started, event)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tracker.ObserveSession("peer", false, started, event)
	}
}

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
