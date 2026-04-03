package metrics

import "testing"

// testSample is a minimal Sample implementation with deterministic Clear behavior.
type testSample struct {
	values []int64
}

func (s *testSample) Update(v int64) { s.values = append(s.values, v) }

func (s *testSample) Clear() { s.values = s.values[:0] }

func (s *testSample) Snapshot() *sampleSnapshot {
	return newSampleSnapshot(int64(len(s.values)), append([]int64(nil), s.values...))
}

func TestResettingSampleCountMonotonicallyIncreases(t *testing.T) {
	s := ResettingSample(&testSample{})

	s.Update(10)
	s.Update(20)
	s.Update(30)
	snap1 := s.Snapshot()

	if snap1.Count() != 3 {
		t.Errorf("snap1.Count(): got %d, want 3", snap1.Count())
	}

	s.Update(40)
	s.Update(50)
	snap2 := s.Snapshot()

	if snap2.Count() != 5 {
		t.Errorf("snap2.Count(): got %d, want 5 (cumulative)", snap2.Count())
	}

	if snap2.Count() < snap1.Count() {
		t.Errorf("count must not decrease: %d -> %d", snap1.Count(), snap2.Count())
	}
}

func TestResettingSampleMeanIsPerInterval(t *testing.T) {
	s := ResettingSample(&testSample{})

	// First interval: mean should be (10+20+30)/3 = 20
	s.Update(10)
	s.Update(20)
	s.Update(30)
	snap1 := s.Snapshot()

	if snap1.Mean() != 20.0 {
		t.Errorf("snap1.Mean(): got %.2f, want 20.00", snap1.Mean())
	}

	// Second interval: mean should be (40+50)/2 = 45, not polluted by cumulative sum
	s.Update(40)
	s.Update(50)
	snap2 := s.Snapshot()

	if snap2.Mean() != 45.0 {
		t.Errorf("snap2.Mean(): got %.2f, want 45.00", snap2.Mean())
	}
}

func TestResettingSampleValuesResetPerInterval(t *testing.T) {
	s := ResettingSample(&testSample{})

	s.Update(10)
	s.Update(20)
	s.Snapshot()

	s.Update(30)
	snap := s.Snapshot()

	values := snap.Values()
	if len(values) != 1 || values[0] != 30 {
		t.Errorf("values should be [30] from current interval, got %v", values)
	}
}

func TestResettingSampleEmptyInterval(t *testing.T) {
	s := ResettingSample(&testSample{})

	s.Update(10)
	snap1 := s.Snapshot()

	// Empty interval — no updates
	snap2 := s.Snapshot()

	if snap2.Count() != snap1.Count() {
		t.Errorf("count should stay %d on empty interval, got %d", snap1.Count(), snap2.Count())
	}

	if len(snap2.Values()) != 0 {
		t.Errorf("values should be empty on empty interval, got %v", snap2.Values())
	}

	if snap2.Sum() != 0 {
		t.Errorf("sum should be 0 on empty interval, got %d", snap2.Sum())
	}

	if snap2.Mean() != 0 {
		t.Errorf("mean should be 0 on empty interval, got %.2f", snap2.Mean())
	}
}

func TestResettingSampleClearResetsCumulativeCount(t *testing.T) {
	s := ResettingSample(&testSample{})

	s.Update(10)
	s.Update(20)
	snap1 := s.Snapshot()

	if snap1.Count() != 2 {
		t.Fatalf("snap1.Count(): got %d, want 2", snap1.Count())
	}

	// External Clear should reset cumulative count
	s.Clear()

	s.Update(30)
	snap2 := s.Snapshot()

	// After Clear, count should restart from 1, not continue from 2
	if snap2.Count() != 1 {
		t.Errorf("after Clear, snap2.Count(): got %d, want 1", snap2.Count())
	}
}

func TestResettingSampleMultipleEmptyIntervals(t *testing.T) {
	s := ResettingSample(&testSample{})

	s.Update(10)
	snap1 := s.Snapshot()

	// Multiple consecutive empty intervals
	snap2 := s.Snapshot()
	snap3 := s.Snapshot()

	if snap2.Count() != snap1.Count() || snap3.Count() != snap1.Count() {
		t.Errorf("count must stay stable across empty intervals: %d -> %d -> %d",
			snap1.Count(), snap2.Count(), snap3.Count())
	}
}

func TestResettingSampleMinMaxPerInterval(t *testing.T) {
	s := ResettingSample(&testSample{})

	s.Update(100)
	s.Update(200)
	snap1 := s.Snapshot()

	if snap1.Min() != 100 || snap1.Max() != 200 {
		t.Errorf("snap1: min=%d max=%d, want min=100 max=200", snap1.Min(), snap1.Max())
	}

	// Second interval with different range
	s.Update(5)
	s.Update(10)
	snap2 := s.Snapshot()

	if snap2.Min() != 5 || snap2.Max() != 10 {
		t.Errorf("snap2: min=%d max=%d, want min=5 max=10", snap2.Min(), snap2.Max())
	}
}
