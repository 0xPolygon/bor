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

package pathdb

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/triedb/readattr"
)

// TestDiskLayerReadAttribution verifies that a flat-state read which misses
// every in-memory layer and bottoms out on rawdb is recorded under the tag
// set on the reading goroutine, with the kind and key of the read.
func TestDiskLayerReadAttribution(t *testing.T) {
	readattr.SetEnabled(true)
	defer readattr.SetEnabled(false)

	disk, _ := rawdb.Open(rawdb.NewMemoryDatabase(), rawdb.OpenOptions{Ancient: t.TempDir()})
	db := New(disk, nil, false)
	// A fresh database is born "not covered yet" (empty generation marker);
	// clear the generator so flat-state reads are allowed.
	db.tree.bottom().setGenerator(nil)

	// The account doesn't need to exist: a read that misses every memory
	// layer bottoms out in rawdb either way (that's the disk-read signal).
	hash := common.Hash{0x01}

	r, err := db.StateReader(types.EmptyRootHash)
	if err != nil {
		t.Fatalf("failed to open state reader: %v", err)
	}
	readattr.Set("tx:1:0xdead")
	_, err = r.Account(hash)
	readattr.Reset()
	if err != nil {
		t.Fatalf("failed to read account: %v", err)
	}
	if got := readattr.Take("tx:1:0xdead"); got != 1 {
		t.Fatalf("expected 1 disk read attributed to tag, got %d", got)
	}
	entries := readattr.Recent(1)
	if len(entries) != 1 {
		t.Fatalf("expected 1 recorded entry, got %d", len(entries))
	}
	if entries[0].Kind != "account" || entries[0].Key != hash.Hex() {
		t.Fatalf("unexpected entry: kind=%s key=%s", entries[0].Kind, entries[0].Key)
	}
}

// TestDiskLayerReadUnattributed verifies reads on untagged goroutines are
// recorded under the empty tag rather than silently dropped.
func TestDiskLayerReadUnattributed(t *testing.T) {
	readattr.SetEnabled(true)
	defer readattr.SetEnabled(false)

	disk, _ := rawdb.Open(rawdb.NewMemoryDatabase(), rawdb.OpenOptions{Ancient: t.TempDir()})
	db := New(disk, nil, false)
	db.tree.bottom().setGenerator(nil)

	r, err := db.StateReader(types.EmptyRootHash)
	if err != nil {
		t.Fatalf("failed to open state reader: %v", err)
	}
	if _, err := r.Account(common.Hash{0x02}); err != nil {
		t.Fatalf("failed to read account: %v", err)
	}
	if got := readattr.Take(""); got != 1 {
		t.Fatalf("expected 1 unattributed disk read, got %d", got)
	}
}
