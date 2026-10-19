package peerpolicy

import (
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
)

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
