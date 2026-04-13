package metrics

import "sync/atomic"

// ResettingSample converts an ordinary sample into one that resets whenever its
// snapshot is retrieved. This will break for multi-monitor systems, but when only
// a single metric is being pushed out, this ensure that low-frequency events don't
// skew th charts indefinitely.
func ResettingSample(sample Sample) Sample {
	return &resettingSample{
		Sample: sample,
	}
}

// resettingSample is a simple wrapper around a sample that resets it upon the
// snapshot retrieval. It maintains cumulative count separately so that
// Prometheus _count counters remain monotonically increasing across scrapes.
type resettingSample struct {
	Sample
	count atomic.Int64
}

// Clear resets both the underlying sample and the cumulative count.
func (rs *resettingSample) Clear() {
	rs.Sample.Clear()
	rs.count.Store(0)
}

// Snapshot returns a read-only copy of the sample with the original reset.
// Count is cumulative for Prometheus counter semantics.
// Values, Sum, Min, Max are from the current interval only.
func (rs *resettingSample) Snapshot() *sampleSnapshot {
	s := rs.Sample.Snapshot()
	count := rs.count.Add(s.Count())
	rs.Sample.Clear()

	return newSampleSnapshotPrecalculated(
		count, s.values, s.Min(), s.Max(), s.Sum(),
	)
}
