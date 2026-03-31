package metrics

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
// snapshot retrieval. It maintains cumulative count and sum separately so that
// Prometheus _count counters remain monotonically increasing across scrapes.
type resettingSample struct {
	Sample
	count int64
	sum   int64
}

// Snapshot returns a read-only copy of the sample with the original reset.
// Count and Sum are cumulative for Prometheus counter semantics.
// Values (used for percentiles) are from the current interval only.
func (rs *resettingSample) Snapshot() *sampleSnapshot {
	s := rs.Sample.Snapshot()
	rs.count += s.Count()
	rs.sum += s.Sum()
	rs.Sample.Clear()

	return newSampleSnapshotPrecalculated(
		rs.count, s.Values(), s.Min(), s.Max(), rs.sum,
	)
}
