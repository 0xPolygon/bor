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

// Package qmdb implements the key-value database layer based on QMDB.
package qmdb

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/minhd-vu/qmdb-go"
)

var (
	errDBClosed      = errors.New("database is closed")
	errBatchClosed   = errors.New("batch is closed")
	errQmdbNotFound  = errors.New("not found")
)

// Ensure Database implements the ethdb.KeyValueStore interface
var _ ethdb.KeyValueStore = (*Database)(nil)

// Database is a QMDB-backed key-value store
type Database struct {
	handle      *qmdb.QmdbHandle
	shared      *qmdb.QmdbSharedHandle
	path        string
	blockHeight int64
	mutex       sync.RWMutex
	closed      bool

	log log.Logger // Contextual logger
}

// New creates a new QMDB database instance
func New(path string, cache int, handles int, namespace string, readonly bool) (*Database, error) {
	// Initialize QMDB directory
	if err := qmdb.InitDir(path); err != nil {
		return nil, fmt.Errorf("failed to initialize QMDB directory: %w", err)
	}

	// Create QMDB handle
	handle, err := qmdb.New(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create QMDB handle: %w", err)
	}

	// Get shared handle for reads
	shared := handle.GetShared()
	if shared == nil {
		handle.Free()
		return nil, errors.New("failed to get QMDB shared handle")
	}

	db := &Database{
		handle:      handle,
		shared:      shared,
		path:        path,
		blockHeight: 0,
		closed:      false,
		log:         log.New("database", "qmdb", "path", path),
	}

	db.log.Info("Created QMDB database", "cache", cache, "handles", handles, "readonly", readonly)
	return db, nil
}

// Get retrieves the given key if it's present in the database
func (db *Database) Get(key []byte) ([]byte, error) {
	db.mutex.RLock()
	defer db.mutex.RUnlock()

	if db.closed {
		return nil, errDBClosed
	}

	// Hash the key for QMDB
	keyHash, err := qmdb.Hash(key)
	if err != nil {
		return nil, err
	}

	// Read from QMDB
	value, found, err := db.shared.ReadEntry(db.blockHeight, keyHash[:], key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errQmdbNotFound
	}

	return value, nil
}

// Has retrieves if a key is present in the database
func (db *Database) Has(key []byte) (bool, error) {
	_, err := db.Get(key)
	if err == errQmdbNotFound {
		return false, nil
	}
	return err == nil, err
}

// Put inserts the given value into the database
func (db *Database) Put(key []byte, value []byte) error {
	// For simplicity, create a batch and write immediately
	batch := db.NewBatch()
	if err := batch.Put(key, value); err != nil {
		return err
	}
	return batch.Write()
}

// Delete removes the key from the database
func (db *Database) Delete(key []byte) error {
	// For simplicity, create a batch and write immediately
	batch := db.NewBatch()
	if err := batch.Delete(key); err != nil {
		return err
	}
	return batch.Write()
}

// DeleteRange deletes all keys in the given range
func (db *Database) DeleteRange(start, end []byte) error {
	return errors.New("DeleteRange not supported by QMDB")
}

// Stat returns a particular internal statistic of the database
func (db *Database) Stat() (string, error) {
	return fmt.Sprintf("qmdb,path=%s,height=%d", db.path, db.blockHeight), nil
}

// Compact flattens the underlying data store for the given key range
func (db *Database) Compact(start []byte, limit []byte) error {
	return nil // QMDB handles compaction internally
}

// Close closes the database connection
func (db *Database) Close() error {
	db.mutex.Lock()
	defer db.mutex.Unlock()

	if db.closed {
		return nil
	}

	if db.shared != nil {
		db.shared.Free()
		db.shared = nil
	}
	if db.handle != nil {
		db.handle.Free()
		db.handle = nil
	}

	db.closed = true
	db.log.Info("Closed QMDB database")
	return nil
}