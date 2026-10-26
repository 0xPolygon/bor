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

package filtermaps

import (
	"context"

	"golang.org/x/sync/semaphore"
)

// maxIndexReaders is the weight a writer takes. Readers take one each, so the
// value only has to exceed the reader count.
const maxIndexReaders = 1 << 30

// indexRWLock is a reader-writer lock whose read side can be acquired under a
// context. A reader blocked on sync.RWMutex cannot give up before the writer
// releases, which left cancelled eth_getLogs requests parked there for hours
// (ethereum/go-ethereum#35630). semaphore.Weighted serves waiters in FIFO
// order, so a waiting writer still blocks readers arriving after it and the map
// renderer keeps the priority sync.RWMutex gave it.
type indexRWLock struct {
	sem *semaphore.Weighted
}

func newIndexRWLock() indexRWLock {
	return indexRWLock{sem: semaphore.NewWeighted(maxIndexReaders)}
}

// Lock acquires the lock for writing. The indexer cannot abandon a half-written
// batch, so there is no cancellable form.
func (l *indexRWLock) Lock() {
	l.sem.Acquire(context.Background(), maxIndexReaders)
}

// Unlock releases a write acquisition.
func (l *indexRWLock) Unlock() {
	l.sem.Release(maxIndexReaders)
}

// RLock acquires the lock for reading and cannot be cancelled. A caller that
// holds a context uses RLockCtx instead.
func (l *indexRWLock) RLock() {
	l.sem.Acquire(context.Background(), 1)
}

// RLockCtx acquires the lock for reading and gives up with ctx.Err() if the
// context is cancelled, whether the cancellation arrives before the call or
// during the wait. The lock is not held when RLockCtx returns an error.
func (l *indexRWLock) RLockCtx(ctx context.Context) error {
	return l.sem.Acquire(ctx, 1)
}

// RUnlock releases a read acquisition.
func (l *indexRWLock) RUnlock() {
	l.sem.Release(1)
}
