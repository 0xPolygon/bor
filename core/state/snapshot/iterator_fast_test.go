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
	"errors"
	"testing"

	"github.com/VictoriaMetrics/fastcache"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
)

// TestFastIteratorStaleLayer checks that a layer turning stale while being
// iterated fails the whole iterator instead of silently dropping that layer.
func TestFastIteratorStaleLayer(t *testing.T) {
	tests := []struct {
		name    string
		account bool
		after   int // items read before the layer turns stale, -1 for before creation
	}{
		{"account", true, 1},
		{"storage", false, 1},
		{"account stale before creation", true, -1},
		{"storage stale before creation", false, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &diskLayer{
				diskdb: rawdb.NewMemoryDatabase(),
				root:   common.HexToHash("0x01"),
				cache:  fastcache.New(1024 * 500),
			}
			snaps := &Tree{layers: map[common.Hash]snapshot{base.root: base}}

			// The bottom diff layer holds the first and the last key, so that the
			// iterator steps it once the first key was read.
			snaps.Update(common.HexToHash("0x02"), common.HexToHash("0x01"),
				randomAccountSet("0xaa", "0xcc"), randomStorageSet([]string{"0xaa"}, [][]string{{"0x01", "0x03"}}, nil))
			snaps.Update(common.HexToHash("0x03"), common.HexToHash("0x02"),
				randomAccountSet("0xbb"), randomStorageSet([]string{"0xaa"}, [][]string{{"0x02"}}, nil))
			bottom := snaps.Snapshot(common.HexToHash("0x02")).(*diffLayer)

			if tt.after < 0 {
				bottom.stale.Store(true)
			}
			var (
				it  Iterator
				err error
			)
			if tt.account {
				it, err = snaps.AccountIterator(common.HexToHash("0x03"), common.Hash{})
			} else {
				it, err = snaps.StorageIterator(common.HexToHash("0x03"), common.HexToHash("0xaa"), common.Hash{})
			}
			if err != nil {
				t.Fatalf("failed to create iterator: %v", err)
			}
			defer it.Release()

			for i := 0; i < tt.after; i++ {
				if !it.Next() {
					t.Fatalf("iterator exhausted at %d: %v", i, it.Error())
				}
			}
			bottom.stale.Store(true)
			for it.Next() {
			}
			if !errors.Is(it.Error(), ErrSnapshotStale) {
				t.Fatalf("unexpected error: have %v, want %v", it.Error(), ErrSnapshotStale)
			}
		})
	}
}

type stoppedIterator struct {
	err      error
	released bool
}

func (it *stoppedIterator) Next() bool        { return false }
func (it *stoppedIterator) Error() error      { return it.err }
func (it *stoppedIterator) Hash() common.Hash { return common.Hash{} }
func (it *stoppedIterator) Release()          { it.released = true }

func TestFastIteratorReleasesStoppedLayer(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"exhausted", nil},
		{"stale", ErrSnapshotStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stopped := &stoppedIterator{err: tc.err}
			fi := &fastIterator{iterators: []*weightedIterator{{it: stopped}}}
			fi.next(0)
			if !stopped.released {
				t.Fatal("stopped sub-iterator was not released")
			}
			if !errors.Is(fi.fail, tc.err) {
				t.Fatalf("fail: have %v, want %v", fi.fail, tc.err)
			}
		})
	}
}
