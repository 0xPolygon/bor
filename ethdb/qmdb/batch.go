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

package qmdb

import (
	"errors"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/minhd-vu/qmdb-go"
)

// Batch is a write-only database that commits changes atomically
type Batch struct {
	db        *Database
	changeset *qmdb.QmdbChangeSet
	size      int
	closed    bool
}

// NewBatch creates a write-only database batch
func (db *Database) NewBatch() ethdb.Batch {
	return &Batch{
		db:        db,
		changeset: qmdb.NewChangeSet(),
		size:      0,
		closed:    false,
	}
}

// NewBatchWithSize creates a write-only database batch with pre-allocated buffer
func (db *Database) NewBatchWithSize(size int) ethdb.Batch {
	return db.NewBatch() // Ignore size hint for simplicity
}

// Put inserts the given value into the batch for later committing
func (b *Batch) Put(key []byte, value []byte) error {
	if b.closed {
		return errBatchClosed
	}

	// Hash key and determine shard
	keyHash, err := qmdb.Hash(key)
	if err != nil {
		return err
	}
	shardId := qmdb.Byte0ToShardId(keyHash[0])

	// Add to changeset (always use Write operation for simplicity)
	err = b.changeset.AddOp(qmdb.OpWrite, uint8(shardId), keyHash[:], key, value)
	if err != nil {
		return err
	}

	b.size += len(key) + len(value)
	return nil
}

// Delete inserts a key removal into the batch for later committing
func (b *Batch) Delete(key []byte) error {
	if b.closed {
		return errBatchClosed
	}

	// Hash key and determine shard
	keyHash, err := qmdb.Hash(key)
	if err != nil {
		return err
	}
	shardId := qmdb.Byte0ToShardId(keyHash[0])

	// Add delete operation
	err = b.changeset.AddOp(qmdb.OpDelete, uint8(shardId), keyHash[:], key, nil)
	if err != nil {
		return err
	}

	b.size += len(key)
	return nil
}

// ValueSize retrieves the amount of data queued up for writing
func (b *Batch) ValueSize() int {
	return b.size
}

// Write flushes any accumulated data to disk
func (b *Batch) Write() error {
	if b.closed {
		return errBatchClosed
	}

	b.db.mutex.Lock()
	defer b.db.mutex.Unlock()

	if b.db.closed {
		return errDBClosed
	}

	// Sort and commit changeset
	b.changeset.Sort()

	// Create task manager and start new block
	changesets := []*qmdb.QmdbChangeSet{b.changeset}
	taskManager, err := qmdb.NewTasksManager(changesets, b.db.blockHeight)
	if err != nil {
		return err
	}
	defer taskManager.Free()

	b.db.blockHeight++
	err = b.db.handle.StartBlock(b.db.blockHeight, taskManager)
	if err != nil {
		return err
	}

	err = b.db.handle.Flush()
	if err != nil {
		return err
	}

	b.closed = true
	return nil
}

// Reset resets the batch for reuse
func (b *Batch) Reset() {
	if b.changeset != nil {
		b.changeset.Free()
	}
	b.changeset = qmdb.NewChangeSet()
	b.size = 0
	b.closed = false
}

// Replay replays the batch contents
func (b *Batch) Replay(w ethdb.KeyValueWriter) error {
	return errors.New("replay not implemented") // Skip for simplicity
}