package eth

import (
	"context"
	"errors"
	"math"
	"testing"
)

// TestGetVoteOnHashRejectsOutOfRangeBlockNumbers verifies that GetVoteOnHash returns an error when endBlockNr is outside the safe range.
func TestGetVoteOnHashRejectsOutOfRangeBlockNumbers(t *testing.T) {
	t.Parallel()

	backend := &EthAPIBackend{}

	tests := []struct {
		name       string
		endBlockNr uint64
	}{
		{"max uint64", math.MaxUint64},
		{"max uint64 minus 15", math.MaxUint64 - 15},
		{"max int64", math.MaxInt64},
		{"max int64 minus 15", math.MaxInt64 - 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := backend.GetVoteOnHash(context.Background(), 0, tt.endBlockNr, "0x00", "test")
			if err == nil {
				t.Fatalf("expected error for endBlockNr=%d, got nil", tt.endBlockNr)
			}
			if !errors.Is(err, errInvalidBlockNumber) {
				t.Fatalf("expected errInvalidBlockNumber, got %v", err)
			}
		})
	}
}
