package blockstm

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestWriteBudgetCharge(t *testing.T) {
	for _, tc := range []struct {
		name          string
		limit         int64
		charges       []int64
		wantExhausted bool
		wantCalls     int
		wantRemaining int64
	}{
		{"under limit", 10, []int64{3, 4}, false, 0, 3},
		{"at limit", 10, []int64{6, 4}, false, 0, 0},
		{"over limit", 10, []int64{6, 5}, true, 1, -1},
		{"charges after exhaustion", 10, []int64{11, 1, 1}, true, 1, -3},
		{"zero limit", 0, []int64{1}, true, 1, -1},
		{"zero charge at limit", 0, []int64{0}, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := NewWriteBudget(tc.limit, func() { calls++ })
			for _, n := range tc.charges {
				b.Charge(n)
			}
			if got := b.Exhausted(); got != tc.wantExhausted {
				t.Errorf("Exhausted() = %v, want %v", got, tc.wantExhausted)
			}
			if calls != tc.wantCalls {
				t.Errorf("onExhaust ran %d times, want %d", calls, tc.wantCalls)
			}
			if got := b.Remaining(); got != tc.wantRemaining {
				t.Errorf("Remaining() = %d, want %d", got, tc.wantRemaining)
			}
		})
	}
}

func TestWriteBudgetExhaustsOnceUnderConcurrentCharges(t *testing.T) {
	const (
		goroutines = 16
		perG       = 1000
		limit      = goroutines * perG / 2
	)
	var calls atomic.Int32
	b := NewWriteBudget(limit, func() { calls.Add(1) })

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				b.Charge(1)
			}
		}()
	}
	wg.Wait()

	if !b.Exhausted() {
		t.Fatal("budget not exhausted after charging twice its limit")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("onExhaust ran %d times, want 1", got)
	}
}
