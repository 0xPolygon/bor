package miner

import (
	"fmt"
	"testing"
	"time"
)

var (
	durationBenchSink time.Duration
	intBenchSink      int
)

type trackerBenchMode int

const (
	trackerBenchBaseline trackerBenchMode = iota
	trackerBenchMostlyNoInsert
	trackerBenchMixedInsert
	trackerBenchAlwaysInsert
)

func benchmarkDuration(mode trackerBenchMode, tx int) time.Duration {
	switch mode {
	case trackerBenchMostlyNoInsert:
		if tx < slowTxTopKSize {
			return time.Duration(20_000+tx) * time.Microsecond
		}
		return time.Duration(100+tx%7) * time.Microsecond
	case trackerBenchMixedInsert:
		if tx%20 == 0 {
			return time.Duration(5_000+tx) * time.Microsecond
		}
		return time.Duration(200+tx%11) * time.Microsecond
	case trackerBenchAlwaysInsert:
		// Monotonically increasing durations force replace-after-full behavior.
		return time.Duration(1_000+tx) * time.Microsecond
	default:
		return time.Duration(250+tx%5) * time.Microsecond
	}
}

func benchmarkSlowTxTopWindowTrackerImpact(b *testing.B, txPerBlock int, mode trackerBenchMode) {
	b.Helper()
	b.ReportAllocs()
	b.ReportMetric(float64(txPerBlock), "tx/block")

	tracker := newSlowTxTopTracker()
	b.ResetTimer()

	for block := 0; block < b.N; block++ {
		tracker.Reset()

		for tx := 0; tx < txPerBlock; tx++ {
			d := benchmarkDuration(mode, tx)
			durationBenchSink = d

			if mode != trackerBenchBaseline {
				tracker.Add(txTimingEntry{duration: d})
			}
		}

		intBenchSink += len(tracker.data)
	}
}

// BenchmarkSlowTxTopWindowTrackerImpact measures overhead of top-10 slow-tx
// tracking compared to baseline timing-only behavior.
func BenchmarkSlowTxTopWindowTrackerImpact(b *testing.B) {
	for _, txPerBlock := range []int{200, 800, 2000} {
		b.Run(fmt.Sprintf("Baseline/%dtx", txPerBlock), func(b *testing.B) {
			benchmarkSlowTxTopWindowTrackerImpact(b, txPerBlock, trackerBenchBaseline)
		})

		b.Run(fmt.Sprintf("TrackerMostlyNoInsert/%dtx", txPerBlock), func(b *testing.B) {
			benchmarkSlowTxTopWindowTrackerImpact(b, txPerBlock, trackerBenchMostlyNoInsert)
		})

		b.Run(fmt.Sprintf("TrackerMixedInsert/%dtx", txPerBlock), func(b *testing.B) {
			benchmarkSlowTxTopWindowTrackerImpact(b, txPerBlock, trackerBenchMixedInsert)
		})

		b.Run(fmt.Sprintf("TrackerAlwaysInsert/%dtx", txPerBlock), func(b *testing.B) {
			benchmarkSlowTxTopWindowTrackerImpact(b, txPerBlock, trackerBenchAlwaysInsert)
		})
	}
}
