// Copyright 2026 The go-ethereum Authors
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

package p2p

import (
	"sync"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

// One authenticated connection is allowed per live RLPx identity. Lease operations
// never call into Server, since peer removal can hold the server lock.
type bulkLeaseBook struct {
	mu   sync.Mutex
	held map[enode.ID]uint64
	seq  uint64
}

func newBulkLeaseBook() *bulkLeaseBook {
	return &bulkLeaseBook{held: make(map[enode.ID]uint64)}
}

// A stale close must not release a newer connection's lease.
func (b *bulkLeaseBook) acquire(id enode.ID) (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.held[id]; ok {
		return nil, false
	}
	b.seq++
	gen := b.seq
	b.held[id] = gen
	return sync.OnceFunc(func() { b.release(id, gen) }), true
}

func (b *bulkLeaseBook) release(id enode.ID, gen uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.held[id]; ok && cur == gen {
		delete(b.held, id)
	}
}

func (b *bulkLeaseBook) active() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.held)
}

func (b *bulkLeaseBook) holds(id enode.ID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.held[id]
	return ok
}
