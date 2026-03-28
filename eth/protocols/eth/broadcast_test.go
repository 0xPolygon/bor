// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestTxAnnQueueDiscard verifies that when the announcement queue overflows its
// limit, the oldest hashes are dropped and only the newest ones are retained.
// This mirrors the discard logic in announceTransactions (broadcast.go:199-202).
func TestTxAnnQueueDiscard(t *testing.T) {
	const limit = 10
	const total = 15 // 5 over the limit

	queue := make([]common.Hash, 0, total)
	for i := 0; i < total; i++ {
		queue = append(queue, common.Hash{byte(i)})
	}

	// Apply the same discard logic as announceTransactions.
	if len(queue) > limit {
		queue = queue[:copy(queue, queue[len(queue)-limit:])]
	}

	if len(queue) != limit {
		t.Fatalf("expected queue length %d after discard, got %d", limit, len(queue))
	}
	// The oldest 5 hashes (0–4) should be gone; newest 10 (5–14) remain.
	for i, h := range queue {
		want := byte(5 + i)
		if h[0] != want {
			t.Errorf("queue[%d] = %d, want %d", i, h[0], want)
		}
	}
}
