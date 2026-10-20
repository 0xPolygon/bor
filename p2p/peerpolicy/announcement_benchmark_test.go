package peerpolicy

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
)

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
