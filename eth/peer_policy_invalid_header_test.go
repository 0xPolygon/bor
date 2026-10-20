// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package eth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func TestPeerPolicyInvalidHeaderClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want uint64
	}{
		{"invalid block number", consensus.ErrInvalidNumber, 60},
		{"wrapped invalid block number", fmt.Errorf("verify header: %w", consensus.ErrInvalidNumber), 60},

		{"no error", nil, 0},
		{"future block", consensus.ErrFutureBlock, 0},
		{"unknown ancestor", consensus.ErrUnknownAncestor, 0},
		{"pruned ancestor", consensus.ErrPrunedAncestor, 0},
		{"unexpected withdrawals", consensus.ErrUnexpectedWithdrawals, 0},
		{"unknown validators", errors.New("unknown validators"), 0},
		{"invalid validator list", errors.New("invalid validator list on sprint end block"), 0},
		{"cancelled context", context.Canceled, 0},
		{"wrapped cancelled context", fmt.Errorf("snapshot: %w", context.Canceled), 0},
		{"local insertion failure", errors.New("leveldb: closed"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
			h.observeInvalidHeader("peer", tc.err)

			if got := h.peerPolicy.Snapshot("peer"); got.Risk != tc.want {
				t.Fatalf("risk %d, want %d: %+v", got.Risk, tc.want, got)
			}
		})
	}
}

// Error text alone must not decide anything: the classifier compares sentinels.
func TestPeerPolicyInvalidHeaderIgnoresLookalikeText(t *testing.T) {
	h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
	h.observeInvalidHeader("peer", errors.New(consensus.ErrInvalidNumber.Error()))

	if got := h.peerPolicy.Snapshot("peer"); got.Risk != 0 {
		t.Fatalf("a message resembling a sentinel was scored: %+v", got)
	}
}
