// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package bor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/consensus"
)

func TestIsObjectivelyInvalidHeader(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, false},
		{"missing vanity", errMissingVanity, true},
		{"missing signature", errMissingSignature, true},
		{"non-canonical seal", errNonCanonicalSeal, false},
		{"wrapped non-canonical seal", fmt.Errorf("verify header: %w", errNonCanonicalSeal), false},
		{"invalid mix digest", errInvalidMixDigest, true},
		{"invalid uncle hash", errInvalidUncleHash, true},
		{"nil difficulty", errInvalidDifficulty, true},
		{"invalid number", consensus.ErrInvalidNumber, true},
		{"wrapped sentinel", fmt.Errorf("verify header: %w", errInvalidMixDigest), true},
		{"same text without sentinel", errors.New(errInvalidMixDigest.Error()), false},

		{"future block", consensus.ErrFutureBlock, false},
		{"wrapped future block", fmt.Errorf("verify: %w", consensus.ErrFutureBlock), false},
		{"cancelled context", context.Canceled, false},
		{"expired context", context.DeadlineExceeded, false},
		{"unknown ancestor", consensus.ErrUnknownAncestor, false},
		{"pruned ancestor", consensus.ErrPrunedAncestor, false},
		{"invalid timestamp", ErrInvalidTimestamp, false},
		{"span validators", errInvalidSpanValidators, false},
		{"unknown validators", errUnknownValidators, false},
		{"extra validators", errExtraValidators, false},
		{"unknown error", errors.New("database is closed"), false},
		{"wrapped unknown error", fmt.Errorf("import: %w", errors.New("context canceled")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsObjectivelyInvalidHeader(tc.err); got != tc.want {
				t.Fatalf("IsObjectivelyInvalidHeader(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
