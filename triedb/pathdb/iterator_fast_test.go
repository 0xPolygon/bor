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
	"bytes"
	"errors"
	"math/big"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

// newSupersededDiskLayer returns a database whose disk layer has moved on, along
// with a view that still resolves the superseded disk layer, mimicking a reader
// that obtained the layer just before it was merged into its successor. Both
// disk layers share the same write buffer.
func newSupersededDiskLayer(t *testing.T) (*Database, *Database, common.Hash) {
	t.Helper()

	db := New(rawdb.NewMemoryDatabase(), &Config{
		WriteBufferSize:   16 * 1024 * 1024,
		NoAsyncGeneration: true,
	}, false)

	update := func(root, parent common.Hash, block uint64, slot string) {
		t.Helper()
		states := NewStateSetWithOrigin(randomAccountSet("0xaa"), randomStorageSet([]string{"0xaa"}, [][]string{{slot}}, nil), nil, nil, false)
		if err := db.Update(root, parent, block, trienode.NewMergedNodeSet(), states); err != nil {
			t.Fatalf("failed to update layer %x: %v", root, err)
		}
	}
	update(common.HexToHash("0x02"), types.EmptyRootHash, 1, "0x01")
	update(common.HexToHash("0x03"), common.HexToHash("0x02"), 2, "0x02")
	if err := db.tree.cap(common.HexToHash("0x03"), 1); err != nil {
		t.Fatalf("failed to cap: %v", err)
	}
	superseded := db.tree.bottom()

	update(common.HexToHash("0x04"), common.HexToHash("0x03"), 3, "0x03")
	if err := db.tree.cap(common.HexToHash("0x04"), 1); err != nil {
		t.Fatalf("failed to cap: %v", err)
	}
	if db.tree.bottom() == superseded || db.tree.bottom().buffer != superseded.buffer {
		t.Fatal("expected a new disk layer sharing the write buffer")
	}
	return db, &Database{tree: newLayerTree(superseded)}, superseded.rootHash()
}

func TestFastIteratorSupersededDiskLayer(t *testing.T) {
	tests := []struct {
		name    string
		account bool
	}{
		{"account", true},
		{"storage", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, view, root := newSupersededDiskLayer(t)

			it, err := newFastIterator(view, root, common.HexToHash("0xaa"), common.Hash{}, tt.account)
			if !errors.Is(err, errSnapshotStale) {
				if it != nil {
					it.Release()
				}
				t.Fatalf("unexpected error: have %v, want %v", err, errSnapshotStale)
			}
		})
	}
}

// TestFastIteratorConcurrentCommit creates fast iterators on a superseded disk
// layer while its successor keeps merging new layers into the shared buffer.
// Run with -race.
func TestFastIteratorConcurrentCommit(t *testing.T) {
	tests := []struct {
		name    string
		account bool
	}{
		{"account", true},
		{"storage", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, view, root := newSupersededDiskLayer(t)

			done := make(chan struct{})
			go func() {
				defer close(done)
				parent := common.HexToHash("0x04")
				for i := 0; i < 200; i++ {
					next := common.BigToHash(big.NewInt(int64(0x1000 + i)))
					states := NewStateSetWithOrigin(randomAccountSet("0xaa", next.Hex()), randomStorageSet([]string{"0xaa"}, [][]string{{next.Hex()}}, nil), nil, nil, false)
					if err := db.Update(next, parent, uint64(4+i), trienode.NewMergedNodeSet(), states); err != nil {
						t.Errorf("failed to update: %v", err)
						return
					}
					if err := db.tree.cap(next, 1); err != nil {
						t.Errorf("failed to cap: %v", err)
						return
					}
					parent = next
				}
			}()
			for {
				select {
				case <-done:
					return
				default:
				}
				it, err := newFastIterator(view, root, common.HexToHash("0xaa"), common.Hash{}, tt.account)
				if err == nil {
					it.Release()
				}
			}
		})
	}
}

func TestDiskLayerSources(t *testing.T) {
	account := common.HexToHash("0xaa")
	slot := common.HexToHash("0x01")
	errFlush := errors.New("flush failed")

	live := func() *diskLayer {
		diskdb := rawdb.NewMemoryDatabase()
		rawdb.WriteAccountSnapshot(diskdb, account, []byte{0x2})
		rawdb.WriteStorageSnapshot(diskdb, account, slot, []byte{0x2})

		states := newStates(map[common.Hash][]byte{account: {0x1}}, map[common.Hash]map[common.Hash][]byte{account: {slot: {0x1}}}, false)
		return &diskLayer{db: &Database{diskdb: diskdb}, buffer: newBuffer(0, 0, nil, states, 1)}
	}
	failedFlush := func() *diskLayer {
		dl := live()
		dl.frozen = &buffer{done: make(chan struct{}), flushErr: errFlush}
		close(dl.frozen.done)
		return dl
	}
	stale := func() *diskLayer {
		dl := live()
		dl.stale = true
		return dl
	}
	tests := []struct {
		name  string
		layer func() *diskLayer
		want  []common.Hash
		err   error
	}{
		{"live", live, []common.Hash{account}, nil},
		{"stale", stale, nil, errSnapshotStale},
		{"flush error", failedFlush, nil, errFlush},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accounts, accountIt, err := tt.layer().accountSources(common.Hash{})
			if !errors.Is(err, tt.err) || !slices.Equal(accounts, tt.want) {
				t.Fatalf("account list: have (%v, %v), want (%v, %v)", accounts, err, tt.want, tt.err)
			}
			want := tt.want
			if want != nil {
				want = []common.Hash{slot}
			}
			slots, storageIt, err := tt.layer().storageSources(account, common.Hash{})
			if !errors.Is(err, tt.err) || !slices.Equal(slots, want) {
				t.Fatalf("storage list: have (%v, %v), want (%v, %v)", slots, err, want, tt.err)
			}
			if tt.err != nil {
				if accountIt != nil || storageIt != nil {
					t.Fatal("disk iterator returned with an error")
				}
				return
			}
			defer accountIt.Release()
			defer storageIt.Release()
			if !accountIt.Next() || accountIt.Hash() != account || !bytes.Equal(accountIt.Account(), []byte{0x2}) {
				t.Fatal("disk account iterator not positioned on the persistent account")
			}
			if !storageIt.Next() || storageIt.Hash() != slot || !bytes.Equal(storageIt.Slot(), []byte{0x2}) {
				t.Fatal("disk storage iterator not positioned on the persistent slot")
			}
		})
	}
}

// hookedDB runs hook once, when the next iterator over prefix is opened.
type hookedDB struct {
	ethdb.Database
	prefix []byte
	hook   atomic.Pointer[func()]
}

func (db *hookedDB) NewIterator(prefix []byte, start []byte) ethdb.Iterator {
	if bytes.HasPrefix(prefix, db.prefix) {
		if hook := db.hook.Swap(nil); hook != nil {
			(*hook)()
		}
	}
	return db.Database.NewIterator(prefix, start)
}

// TestFastIteratorFlushBeforeDiskOpen persists a newer state while the fast
// iterator of the older root opens its persistent iterator. The iterator must
// then fail or still yield the older state, never the newer one.
func TestFastIteratorFlushBeforeDiskOpen(t *testing.T) {
	var (
		account = common.HexToHash("0xaa")
		slot    = common.HexToHash("0x01")
		older   = common.HexToHash("0x02")
		newer   = common.HexToHash("0x03")
	)
	tests := []struct {
		name    string
		account bool
		prefix  []byte
	}{
		{"account", true, rawdb.SnapshotAccountPrefix},
		{"storage", false, rawdb.SnapshotStoragePrefix},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diskdb := &hookedDB{Database: rawdb.NewMemoryDatabase(), prefix: tt.prefix}
			db := New(diskdb, &Config{NoAsyncFlush: true, NoAsyncGeneration: true}, false)

			update := func(root, parent common.Hash, block uint64, value byte) {
				t.Helper()
				states := NewStateSetWithOrigin(
					map[common.Hash][]byte{account: {value}},
					map[common.Hash]map[common.Hash][]byte{account: {slot: {value}}},
					nil, nil, false)
				if err := db.Update(root, parent, block, trienode.NewMergedNodeSet(), states); err != nil {
					t.Fatalf("failed to update layer %x: %v", root, err)
				}
			}
			update(older, types.EmptyRootHash, 1, 0x1)
			if err := db.tree.cap(older, 0); err != nil {
				t.Fatalf("failed to persist: %v", err)
			}
			update(newer, older, 2, 0x2)

			persisted := make(chan error, 1)
			hook := func() {
				go func() { persisted <- db.tree.cap(newer, 0) }()
				// The persist either lands before the iterator is opened, or it is
				// held off until the layer lock is released.
				select {
				case err := <-persisted:
					persisted <- err
				case <-time.After(100 * time.Millisecond):
				}
			}
			diskdb.hook.Store(&hook)

			it, err := newFastIterator(db, older, account, common.Hash{}, tt.account)
			var values [][]byte
			if err == nil {
				for it.Next() {
					if tt.account {
						values = append(values, common.CopyBytes(it.Account()))
					} else {
						values = append(values, common.CopyBytes(it.Slot()))
					}
				}
				err = it.Error()
				it.Release()
			}
			if perr := <-persisted; perr != nil {
				t.Fatalf("failed to persist: %v", perr)
			}
			if err != nil {
				return
			}
			if len(values) != 1 || !bytes.Equal(values[0], []byte{0x1}) {
				t.Fatalf("older root iterated as %x, want [01]", values)
			}
		})
	}
}

// iterateValues drains it, returning the hashes and values it yielded.
func iterateValues(t *testing.T, it Iterator, account bool) ([]common.Hash, [][]byte) {
	t.Helper()
	defer it.Release()

	var (
		hashes []common.Hash
		values [][]byte
	)
	for it.Next() {
		hashes = append(hashes, it.Hash())
		if account {
			values = append(values, common.CopyBytes(it.(AccountIterator).Account()))
		} else {
			values = append(values, common.CopyBytes(it.(StorageIterator).Slot()))
		}
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iteration failed: %v", err)
	}
	return hashes, values
}

// waitLock polls until locked reports true twice in a row, some time apart.
func waitLock(t *testing.T, locked func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !(locked() && func() bool { time.Sleep(10 * time.Millisecond); return locked() }()) {
		if time.Now().After(deadline) {
			t.Fatal("lock state not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestFastIteratorDiskLayerReplacedAtOpen replaces the disk layer while a fast
// iterator is being created on a diff layer above it, after the walk reached the
// disk layer and before its sources were taken. The iterator must still be
// created, over the same state as before the replacement.
func TestFastIteratorDiskLayerReplacedAtOpen(t *testing.T) {
	account := common.HexToHash("0xaa")
	tests := []struct {
		name    string
		account bool
	}{
		{"account", true},
		{"storage", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := New(rawdb.NewMemoryDatabase(), &Config{
				WriteBufferSize:   16 * 1024 * 1024,
				NoAsyncGeneration: true,
			}, false)

			update := func(root, parent common.Hash, block uint64, slot string) {
				t.Helper()
				states := NewStateSetWithOrigin(randomAccountSet("0xaa", slot), randomStorageSet([]string{"0xaa"}, [][]string{{slot}}, nil), nil, nil, false)
				if err := db.Update(root, parent, block, trienode.NewMergedNodeSet(), states); err != nil {
					t.Fatalf("failed to update layer %x: %v", root, err)
				}
			}
			update(common.HexToHash("0x02"), types.EmptyRootHash, 1, "0x01")
			if err := db.tree.cap(common.HexToHash("0x02"), 0); err != nil {
				t.Fatalf("failed to persist: %v", err)
			}
			update(common.HexToHash("0x03"), common.HexToHash("0x02"), 2, "0x02")
			update(common.HexToHash("0x04"), common.HexToHash("0x03"), 3, "0x03")

			head := common.HexToHash("0x04")
			open := func() (Iterator, error) {
				if tt.account {
					return newFastAccountIterator(db, head, common.Hash{})
				}
				return newFastStorageIterator(db, head, account, common.Hash{})
			}
			it, err := open()
			if err != nil {
				t.Fatalf("failed to create iterator: %v", err)
			}
			wantHashes, wantValues := iterateValues(t, it, tt.account)

			// Hold the walk in the disk layer's flush wait, with the read lock
			// taken, until the cap below is queued for the write lock.
			disk := db.tree.bottom()
			flushed := make(chan struct{})
			disk.frozen = &buffer{done: flushed}

			type result struct {
				it  Iterator
				err error
			}
			opened := make(chan result, 1)
			go func() {
				it, err := open()
				opened <- result{it, err}
			}()
			waitLock(t, func() bool {
				if disk.lock.TryLock() {
					disk.lock.Unlock()
					return false
				}
				return true
			})
			capped := make(chan error, 1)
			go func() { capped <- db.tree.cap(head, 1) }()
			waitLock(t, func() bool {
				if disk.lock.TryRLock() {
					disk.lock.RUnlock()
					return false
				}
				return true
			})
			close(flushed)

			if err := <-capped; err != nil {
				t.Fatalf("failed to cap: %v", err)
			}
			if db.tree.bottom() == disk || !disk.stale {
				t.Fatal("expected the disk layer to be replaced")
			}
			res := <-opened
			if res.err != nil {
				t.Fatalf("failed to create iterator: %v", res.err)
			}
			hashes, values := iterateValues(t, res.it, tt.account)
			if !slices.Equal(hashes, wantHashes) || !slices.EqualFunc(values, wantValues, bytes.Equal) {
				t.Fatalf("iterated (%x, %x), want (%x, %x)", hashes, values, wantHashes, wantValues)
			}
		})
	}
}
