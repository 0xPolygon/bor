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
