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

	rejectCases := []struct {
		name       string
		endBlockNr uint64
	}{
		{"max uint64", math.MaxUint64},
		{"max uint64 minus 15", math.MaxUint64 - 15},
		{"max int64", math.MaxInt64},
		{"max int64 minus 15", math.MaxInt64 - 15},
	}

	for _, tt := range rejectCases {
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

	// Boundary value: math.MaxInt64 - tipConfirmationOffset is the highest accepted endBlockNr.
	// The call passes the range guard and then panics on nil backend internals,
	// which confirms the guard did not reject it.
	t.Run("max int64 minus tipConfirmationOffset (boundary, should pass guard)", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if r := recover(); r == nil {
				// No panic means the function returned normally — check that
				// the error is not errInvalidBlockNumber.
			}
			// A panic here means the boundary value passed the guard and
			// proceeded into backend logic (which is nil in this test). That's
			// the expected outcome.
		}()

		_, err := backend.GetVoteOnHash(context.Background(), 0, math.MaxInt64-tipConfirmationOffset, "0x00", "test")
		if errors.Is(err, errInvalidBlockNumber) {
			t.Fatal("expected boundary value to pass the range check, but got errInvalidBlockNumber")
		}
	})
}
