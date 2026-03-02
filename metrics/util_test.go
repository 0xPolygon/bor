package metrics

import (
	"testing"
	"time"
)

func TestRecordPerItemDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		items    int
		wantN    int64 // expected snapshot count after the call
	}{
		{
			name:     "normal batch",
			duration: 100 * time.Millisecond,
			items:    10,
			wantN:    1,
		},
		{
			name:     "single item",
			duration: 50 * time.Millisecond,
			items:    1,
			wantN:    1,
		},
		{
			name:     "zero items is no-op",
			duration: 100 * time.Millisecond,
			items:    0,
			wantN:    0,
		},
		{
			name:     "negative items is no-op",
			duration: 100 * time.Millisecond,
			items:    -1,
			wantN:    0,
		},
		{
			name:     "zero duration is no-op",
			duration: 0,
			items:    5,
			wantN:    0,
		},
		{
			name:     "negative duration is no-op",
			duration: -10 * time.Millisecond,
			items:    5,
			wantN:    0,
		},
		{
			name:     "sub-nanosecond per item clamps to 1ns",
			duration: 1 * time.Nanosecond,
			items:    100,
			wantN:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timer := NewTimer()
			defer timer.Stop()

			RecordPerItemDuration(timer, tt.duration, tt.items)

			snap := timer.Snapshot()
			if got := snap.Count(); got != tt.wantN {
				t.Errorf("Count() = %d, want %d", got, tt.wantN)
			}

			if tt.wantN > 0 {
				expectedPerItem := time.Duration(int64(tt.duration) / int64(tt.items))
				if expectedPerItem <= 0 {
					expectedPerItem = time.Nanosecond
				}
				if got := snap.Mean(); got != float64(expectedPerItem.Nanoseconds()) {
					t.Errorf("Mean() = %f, want %f", got, float64(expectedPerItem.Nanoseconds()))
				}
			}
		})
	}
}

func TestRecordPerItemDuration_NilTimer(t *testing.T) {
	// Must not panic.
	RecordPerItemDuration(nil, 100*time.Millisecond, 10)
}
