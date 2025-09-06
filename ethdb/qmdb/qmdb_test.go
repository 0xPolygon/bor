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
	"path/filepath"
	"testing"
)

func TestQMDBBasicOperations(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "testdb")

	// Create database
	db, err := New(dbPath, 16, 16, "test", false)
	if err != nil {
		t.Fatalf("Failed to create QMDB database: %v", err)
	}
	defer db.Close()

	// Test basic Put/Get operations
	key := []byte("test-key")
	value := []byte("test-value")

	// Test Put
	err = db.Put(key, value)
	if err != nil {
		t.Fatalf("Failed to put key-value pair: %v", err)
	}

	// Test Get
	retrievedValue, err := db.Get(key)
	if err != nil {
		t.Fatalf("Failed to get value: %v", err)
	}

	if string(retrievedValue) != string(value) {
		t.Fatalf("Retrieved value %s doesn't match original %s", retrievedValue, value)
	}

	// Test Has
	exists, err := db.Has(key)
	if err != nil {
		t.Fatalf("Failed to check key existence: %v", err)
	}
	if !exists {
		t.Fatal("Key should exist but Has returned false")
	}

	// Test Delete
	err = db.Delete(key)
	if err != nil {
		t.Fatalf("Failed to delete key: %v", err)
	}

	// Verify key is deleted
	_, err = db.Get(key)
	if err != errQmdbNotFound {
		t.Fatal("Key should be deleted but still exists")
	}

	exists, err = db.Has(key)
	if err != nil {
		t.Fatalf("Failed to check deleted key existence: %v", err)
	}
	if exists {
		t.Fatal("Deleted key should not exist")
	}
}

func TestQMDBBatch(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "testdb_batch")

	// Create database
	db, err := New(dbPath, 16, 16, "test", false)
	if err != nil {
		t.Fatalf("Failed to create QMDB database: %v", err)
	}
	defer db.Close()

	// Create batch
	batch := db.NewBatch()

	// Add operations to batch
	keys := [][]byte{
		[]byte("key1"),
		[]byte("key2"),
		[]byte("key3"),
	}
	values := [][]byte{
		[]byte("value1"),
		[]byte("value2"),
		[]byte("value3"),
	}

	for i, key := range keys {
		err := batch.Put(key, values[i])
		if err != nil {
			t.Fatalf("Failed to add to batch: %v", err)
		}
	}

	// Check batch size
	if batch.ValueSize() == 0 {
		t.Fatal("Batch size should be greater than 0")
	}

	// Write batch
	err = batch.Write()
	if err != nil {
		t.Fatalf("Failed to write batch: %v", err)
	}

	// Verify all keys exist
	for i, key := range keys {
		value, err := db.Get(key)
		if err != nil {
			t.Fatalf("Failed to get key %s: %v", key, err)
		}
		if string(value) != string(values[i]) {
			t.Fatalf("Value mismatch for key %s: got %s, want %s", key, value, values[i])
		}
	}
}

func TestQMDBIterator(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "testdb_iter")

	// Create database
	db, err := New(dbPath, 16, 16, "test", false)
	if err != nil {
		t.Fatalf("Failed to create QMDB database: %v", err)
	}
	defer db.Close()

	// Test iterator (should return error)
	iter := db.NewIterator(nil, nil)
	defer iter.Release()

	// Iterator should not be valid
	if iter.Next() {
		t.Fatal("Iterator should not advance")
	}

	// Should have error
	if iter.Error() == nil {
		t.Fatal("Iterator should return error for unsupported operation")
	}

	// Keys and values should be nil
	if iter.Key() != nil {
		t.Fatal("Iterator key should be nil")
	}
	if iter.Value() != nil {
		t.Fatal("Iterator value should be nil")
	}
}