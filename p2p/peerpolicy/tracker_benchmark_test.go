package peerpolicy

import (
	"fmt"
	"reflect"
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
