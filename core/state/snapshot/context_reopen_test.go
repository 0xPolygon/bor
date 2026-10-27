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
package snapshot

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
)

// recyclingDB hands out iterators that wipe their key buffer on Release, the
// way a pebble iterator may reuse its memory once closed.
type recyclingDB struct {
	ethdb.KeyValueStore
}

func (db recyclingDB) NewIterator(prefix []byte, start []byte) ethdb.Iterator {
	return &recyclingIterator{Iterator: db.KeyValueStore.NewIterator(prefix, start)}
}

type recyclingIterator struct {
	ethdb.Iterator
	buf []byte
}

func (it *recyclingIterator) Key() []byte {
	it.buf = append(it.buf[:0], it.Iterator.Key()...)
	return it.buf
}

func (it *recyclingIterator) Release() {
	clear(it.buf)
	it.Iterator.Release()
}

// TestReopenIteratorCopiesKey checks that reopenIterator restarts at the next
// key even when the released iterator reuses its key buffer.
func TestReopenIteratorCopiesKey(t *testing.T) {
	db := recyclingDB{memorydb.New()}
	hashes := []common.Hash{{0x01}, {0x02}, {0x03}}
	for _, h := range hashes {
		rawdb.WriteAccountSnapshot(db, h, []byte{0x01})
	}
	ctx := newGeneratorContext(&generatorStats{}, db, nil, nil)
	defer ctx.close()

	if !ctx.account.Next() {
		t.Fatal("account iterator is empty")
	}
	ctx.reopenIterator(snapAccount)
	if !ctx.account.Next() {
		t.Fatal("reopened iterator is empty")
	}
	want := append(rawdb.SnapshotAccountPrefix, hashes[1].Bytes()...)
	if have := ctx.account.Key(); !bytes.Equal(have, want) {
		t.Fatalf("reopened at %x, want %x", have, want)
	}
}
