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

package vm

import (
	"sync"
	"sync/atomic"
)

// BoundedCache is a size-capped, thread-safe memoization cache shared by a
// block's prefetcher and all BlockSTM V2 workers. Writes from discarded
// speculative executions are never rolled back, hence the cap. Past the cap
// Store is a no-op and callers recompute on miss.
type BoundedCache[K comparable, V any] struct {
	m       sync.Map
	count   atomic.Int64
	maxSize int64
}

// newBoundedCache returns an empty cache capped at maxSize entries.
func newBoundedCache[K comparable, V any](maxSize int64) *BoundedCache[K, V] {
	return &BoundedCache[K, V]{maxSize: maxSize}
}

// Load returns the cached value for key, if present.
func (c *BoundedCache[K, V]) Load(key K) (V, bool) {
	v, ok := c.m.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	return v.(V), true
}

// Store adds key/value unless the cap is reached. The count check isn't
// atomic with the insert, so concurrent callers can overshoot the cap by at
// most the number of workers; cheaper than locking this hot path.
func (c *BoundedCache[K, V]) Store(key K, value V) {
	if c.count.Load() >= c.maxSize {
		return
	}
	if _, loaded := c.m.LoadOrStore(key, value); !loaded {
		c.count.Add(1)
	}
}
