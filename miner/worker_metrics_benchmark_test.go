package miner

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/metrics"
)

var (
	metricBenchEnableOnce sync.Once
	durationBenchSink     time.Duration
	boolBenchSink         bool
)

func enableMetricsForBenchmark() {
	metricBenchEnableOnce.Do(metrics.Enable)
}

func newCustomTimerWithReservoir(reservoirSize int) *metrics.Timer {
	return metrics.NewCustomTimer(
		metrics.NewHistogram(metrics.NewExpDecaySample(reservoirSize, 0.015)),
		metrics.NewMeter(),
	)
}

func benchmarkTxTimingLoop(b *testing.B, txPerBlock int, timer *metrics.Timer, slowTxThreshold time.Duration) {
	b.Helper()
	b.ReportAllocs()
	b.ReportMetric(float64(txPerBlock), "tx/block")
	b.ResetTimer()

	for block := 0; block < b.N; block++ {
		for tx := 0; tx < txPerBlock; tx++ {
			start := time.Now()
			d := time.Since(start)

			if slowTxThreshold >= 0 {
				boolBenchSink = d > slowTxThreshold
			}
			if timer != nil {
				timer.Update(d)
			}

			durationBenchSink = d
		}
	}
}

// BenchmarkTxTimingAndMetricsImpact measures CPU/allocation overhead of:
// 1) taking tx execution duration (time.Now/time.Since),
// 2) threshold comparison for slow-tx detection,
// 3) updating go-ethereum metrics.Timer per tx.
//
// Note: b.N is the number of simulated blocks; each block processes tx/block
// txs to mirror production ranges like 200-800 tx per block.
func BenchmarkTxTimingAndMetricsImpact(b *testing.B) {
	for _, txPerBlock := range []int{200, 800, 2000} {
		txPerBlock := txPerBlock

		b.Run(fmt.Sprintf("TimingOnly/%dtx", txPerBlock), func(b *testing.B) {
			benchmarkTxTimingLoop(b, txPerBlock, nil, -1)
		})

		b.Run(fmt.Sprintf("TimingPlusThreshold/%dtx", txPerBlock), func(b *testing.B) {
			benchmarkTxTimingLoop(b, txPerBlock, nil, 500*time.Millisecond)
		})
	}

	// Run all disabled cases first because metrics.Enable() is a one-way global switch.
	for _, txPerBlock := range []int{200, 800, 2000} {
		txPerBlock := txPerBlock

		b.Run(fmt.Sprintf("TimingPlusTimerUpdateDisabledRes1028/%dtx", txPerBlock), func(b *testing.B) {
			timer := metrics.NewTimer()
			b.Cleanup(timer.Stop)
			benchmarkTxTimingLoop(b, txPerBlock, timer, -1)
		})

		b.Run(fmt.Sprintf("TimingPlusTimerUpdateDisabledRes8192/%dtx", txPerBlock), func(b *testing.B) {
			timer := newCustomTimerWithReservoir(8192)
			b.Cleanup(timer.Stop)
			benchmarkTxTimingLoop(b, txPerBlock, timer, -1)
		})
	}

	// Enable metrics once, then run enabled cases.
	enableMetricsForBenchmark()
	for _, txPerBlock := range []int{200, 800, 2000} {
		txPerBlock := txPerBlock

		b.Run(fmt.Sprintf("TimingPlusTimerUpdateEnabledRes1028/%dtx", txPerBlock), func(b *testing.B) {
			timer := metrics.NewTimer()
			b.Cleanup(timer.Stop)
			benchmarkTxTimingLoop(b, txPerBlock, timer, -1)
		})

		b.Run(fmt.Sprintf("TimingPlusTimerUpdateEnabledRes8192/%dtx", txPerBlock), func(b *testing.B) {
			timer := newCustomTimerWithReservoir(8192)
			b.Cleanup(timer.Stop)
			benchmarkTxTimingLoop(b, txPerBlock, timer, -1)
		})
	}
}

// BenchmarkTxTimingAndMetricsAlloc reports total allocated bytes per tx and
// per block while recording duration + metrics update.
func BenchmarkTxTimingAndMetricsAlloc(b *testing.B) {
	enableMetricsForBenchmark()

	for _, tc := range []struct {
		name       string
		txPerBlock int
		makeTimer  func() *metrics.Timer
	}{
		{name: "Res1028/800tx", txPerBlock: 800, makeTimer: metrics.NewTimer},
		{name: "Res8192/800tx", txPerBlock: 800, makeTimer: func() *metrics.Timer {
			return newCustomTimerWithReservoir(8192)
		}},
		{name: "Res1028/2000tx", txPerBlock: 2000, makeTimer: metrics.NewTimer},
		{name: "Res8192/2000tx", txPerBlock: 2000, makeTimer: func() *metrics.Timer {
			return newCustomTimerWithReservoir(8192)
		}},
	} {
		tc := tc

		b.Run(tc.name, func(b *testing.B) {
			timer := tc.makeTimer()
			b.Cleanup(timer.Stop)

			runtime.GC()

			var before runtime.MemStats
			runtime.ReadMemStats(&before)

			b.ReportAllocs()
			b.ReportMetric(float64(tc.txPerBlock), "tx/block")
			b.ResetTimer()

			for block := 0; block < b.N; block++ {
				for tx := 0; tx < tc.txPerBlock; tx++ {
					start := time.Now()
					d := time.Since(start)
					timer.Update(d)
					durationBenchSink = d
				}
			}

			b.StopTimer()
			runtime.GC()

			var after runtime.MemStats
			runtime.ReadMemStats(&after)

			totalAllocDelta := float64(after.TotalAlloc - before.TotalAlloc)
			totalTx := float64(b.N * tc.txPerBlock)

			if b.N > 0 {
				b.ReportMetric(totalAllocDelta/float64(b.N), "B/block_totalalloc")
			}
			if totalTx > 0 {
				b.ReportMetric(totalAllocDelta/totalTx, "B/tx_totalalloc")
			}
		})
	}
}
