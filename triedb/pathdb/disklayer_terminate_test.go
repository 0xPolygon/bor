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

package pathdb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestDiskLayerTerminatePersistsAddressCache proves that diskLayer.terminate
// actually calls through to dl.nodes.Close(persist): with persist=true, the
// address-biased cache's snapshot file must exist on disk afterwards. If the
// dl.nodes.Close(persist) call were ever dropped, no snapshot would be
// written and this assertion would fail while terminate() itself would still
// return nil (no error path involved), so the file check is the only way to
// observe the call happened at all.
func TestDiskLayerTerminatePersistsAddressCache(t *testing.T) {
	db := New(rawdb.NewMemoryDatabase(), nil, false)
	disk := db.tree.bottom()
	if disk.nodes == nil {
		t.Fatal("expected the default disk layer to have an address-biased cache")
	}

	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	accountHash := crypto.Keccak256Hash(addr.Bytes())
	journalDir := t.TempDir()

	cache, err := NewAddressBiasedCache(rawdb.NewMemoryDatabase(), map[common.Address]int{addr: 32 * 1024}, 16*1024, 0, journalDir)
	if err != nil {
		t.Fatalf("failed to create address cache: %v", err)
	}
	cache.wg.Wait()
	disk.nodes = cache

	if err := disk.terminate(true); err != nil {
		t.Fatalf("terminate(true) returned an unexpected error: %v", err)
	}

	path := snapshotPath(journalDir, accountHash)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected terminate(true) to persist the address cache via nodes.Close(true), got stat err: %v", err)
	}
}

// TestDiskLayerTerminatePropagatesFrozenFlushError proves that terminate
// propagates an error from the frozen buffer's waitFlush, rather than
// swallowing it. This exercises the same "if err != nil { return err }"
// pattern the exported Database.Journal path relies on for the analogous
// disk.terminate(false) call.
func TestDiskLayerTerminatePropagatesFrozenFlushError(t *testing.T) {
	db := New(rawdb.NewMemoryDatabase(), nil, false)
	disk := db.tree.bottom()

	boom := errors.New("flush boom")
	frozen := &buffer{done: make(chan struct{})}
	close(frozen.done)
	frozen.flushErr = boom
	disk.frozen = frozen

	err := disk.terminate(false)
	if !errors.Is(err, boom) {
		t.Fatalf("expected terminate to propagate the frozen buffer's flush error, got: %v", err)
	}
}

// TestJournalPropagatesTerminateError proves Database.Journal propagates an
// error from disk.terminate(false) instead of swallowing it (the survived
// "remove if body" mutant on that call site would otherwise let Journal
// continue as if nothing failed).
func TestJournalPropagatesTerminateError(t *testing.T) {
	db := New(rawdb.NewMemoryDatabase(), nil, false)
	disk := db.tree.bottom()

	boom := errors.New("flush boom")
	frozen := &buffer{done: make(chan struct{})}
	close(frozen.done)
	frozen.flushErr = boom
	disk.frozen = frozen

	err := db.Journal(disk.rootHash())
	if !errors.Is(err, boom) {
		t.Fatalf("expected Journal to propagate disk.terminate's error, got: %v", err)
	}
}

// attachAddressCache replaces the disk layer's node cache with an
// address-biased cache that persists snapshots under a temp dir, and returns
// the snapshot path for addr.
func attachAddressCache(t *testing.T, disk *diskLayer) string {
	t.Helper()

	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	journalDir := t.TempDir()

	cache, err := NewAddressBiasedCache(rawdb.NewMemoryDatabase(), map[common.Address]int{addr: 32 * 1024}, 16*1024, 0, journalDir)
	if err != nil {
		t.Fatalf("failed to create address cache: %v", err)
	}
	cache.wg.Wait()
	disk.nodes = cache

	return snapshotPath(journalDir, crypto.Keccak256Hash(addr.Bytes()))
}

// TestDisableDoesNotPersistAddressCache checks that Database.Disable marks
// state sync as running and does not save the address cache snapshot, since
// it is not a final shutdown.
func TestDisableDoesNotPersistAddressCache(t *testing.T) {
	diskdb := rawdb.NewMemoryDatabase()
	db := New(diskdb, nil, false)
	path := attachAddressCache(t, db.tree.bottom())

	if err := db.Disable(); err != nil {
		t.Fatalf("Disable returned an unexpected error: %v", err)
	}
	if status := rawdb.ReadSnapSyncStatusFlag(diskdb); status != rawdb.StateSyncRunning {
		t.Fatalf("expected snap sync status %d after Disable, got %d", rawdb.StateSyncRunning, status)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected Disable not to persist the address cache, got stat err: %v", err)
	}
}

// TestJournalDoesNotPersistAddressCache checks that Database.Journal does not
// save the address cache snapshot. Only Database.Close persists it.
func TestJournalDoesNotPersistAddressCache(t *testing.T) {
	db := New(rawdb.NewMemoryDatabase(), nil, false)
	disk := db.tree.bottom()
	path := attachAddressCache(t, disk)

	if err := db.Journal(disk.rootHash()); err != nil {
		t.Fatalf("Journal returned an unexpected error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected Journal not to persist the address cache, got stat err: %v", err)
	}
}

// TestCloseDoesPersistAddressCache checks that Database.Close, the final
// shutdown, saves the address cache snapshot.
func TestCloseDoesPersistAddressCache(t *testing.T) {
	db := New(rawdb.NewMemoryDatabase(), nil, false)
	path := attachAddressCache(t, db.tree.bottom())

	if err := db.Close(); err != nil {
		t.Fatalf("Close returned an unexpected error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected Close to persist the address cache, got stat err: %v", err)
	}
}

// TestDisableAndClosePropagateTerminateError checks that Disable and Close
// return the error from disk.terminate instead of continuing.
func TestDisableAndClosePropagateTerminateError(t *testing.T) {
	for name, call := range map[string]func(*Database) error{
		"Disable": (*Database).Disable,
		"Close":   (*Database).Close,
	} {
		t.Run(name, func(t *testing.T) {
			db := New(rawdb.NewMemoryDatabase(), nil, false)
			disk := db.tree.bottom()

			boom := errors.New("flush boom")
			frozen := &buffer{done: make(chan struct{})}
			close(frozen.done)
			frozen.flushErr = boom
			disk.frozen = frozen

			if err := call(db); !errors.Is(err, boom) {
				t.Fatalf("expected %s to propagate disk.terminate's error, got: %v", name, err)
			}
		})
	}
}

// TestAddressCachePersistFlag checks that the address cache is only saved on
// Close when Config.AddressCachePersist is set, even with a journal
// directory configured.
func TestAddressCachePersistFlag(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	accountHash := crypto.Keccak256Hash(addr.Bytes())

	for _, persist := range []bool{false, true} {
		journalDir := t.TempDir()
		config := *Defaults
		config.JournalDirectory = journalDir
		config.AddressCacheSizes = map[common.Address]int{addr: 32 * 1024}
		config.AddressCachePersist = persist

		db := New(rawdb.NewMemoryDatabase(), &config, false)
		if err := db.Close(); err != nil {
			t.Fatalf("persist=%v: Close returned an unexpected error: %v", persist, err)
		}

		_, err := os.Stat(snapshotPath(journalDir, accountHash))
		if persist && err != nil {
			t.Fatalf("expected Close to persist the address cache with the flag on, got stat err: %v", err)
		}
		if !persist && !os.IsNotExist(err) {
			t.Fatalf("expected Close not to persist the address cache with the flag off, got stat err: %v", err)
		}
	}
}

// writeStaleSnapshot creates a fake address cache snapshot under journalDir
// and returns its path.
func writeStaleSnapshot(t *testing.T, journalDir string) string {
	t.Helper()

	path := snapshotPath(journalDir, common.HexToHash("0x01"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create snapshot dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatalf("failed to write snapshot: %v", err)
	}
	return path
}

// TestDisableRemovesStaleSnapshots checks that Disable drops saved address
// cache snapshots when persistence is on, since state sync is about to
// rebuild the trie they describe, and leaves files alone when it is off.
func TestDisableRemovesStaleSnapshots(t *testing.T) {
	for _, persist := range []bool{false, true} {
		journalDir := t.TempDir()
		path := writeStaleSnapshot(t, journalDir)

		config := *Defaults
		config.JournalDirectory = journalDir
		config.AddressCachePersist = persist
		db := New(rawdb.NewMemoryDatabase(), &config, false)

		if err := db.Disable(); err != nil {
			t.Fatalf("persist=%v: Disable returned an unexpected error: %v", persist, err)
		}
		_, err := os.Stat(path)
		if persist && !os.IsNotExist(err) {
			t.Fatalf("expected Disable to remove the stale snapshot, got stat err: %v", err)
		}
		if !persist && err != nil {
			t.Fatalf("expected Disable to keep files when persistence is off, got stat err: %v", err)
		}
	}
}

// TestDisableLogsSnapshotRemovalFailure checks that a failure to drop stale
// snapshots is logged and does not fail Disable.
func TestDisableLogsSnapshotRemovalFailure(t *testing.T) {
	skipIfRoot(t)
	h := installCapturingHandler(t)

	journalDir := t.TempDir()
	writeStaleSnapshot(t, journalDir)

	// A read-only snapshot dir makes RemoveAll fail on its entries.
	dir := snapshotDir(journalDir)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("failed to make snapshot dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	config := *Defaults
	config.JournalDirectory = journalDir
	config.AddressCachePersist = true
	db := New(rawdb.NewMemoryDatabase(), &config, false)

	if err := db.Disable(); err != nil {
		t.Fatalf("Disable returned an unexpected error: %v", err)
	}
	if !h.contains("Failed to remove stale address cache snapshots") {
		t.Fatal("expected Disable to log the snapshot removal failure")
	}
}

// TestEnableAfterDisableRunsColdPreload checks the full state-sync cycle: a
// warm snapshot saved before the sync must not be reloaded by the disk layer
// that Enable builds, so that layer runs a real preload of the new trie.
func TestEnableAfterDisableRunsColdPreload(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	journalDir := t.TempDir()
	// See TestAddressBiasedCache_WarmReloadSkipsPreload for why this size
	// makes any saved snapshot deterministically warm.
	const cacheSize = 1024
	sizes := map[common.Address]int{addr: cacheSize}

	accountHash := crypto.Keccak256Hash(addr.Bytes())
	seed := rawdb.NewMemoryDatabase()
	rawdb.WriteStorageTrieNode(seed, accountHash, nil, encodeBranchNode(t, []byte{0}, make([]byte, 32)))
	first, err := NewAddressBiasedCache(seed, sizes, 4*1024, 0, journalDir)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	first.wg.Wait()
	first.Close(true)

	config := *Defaults
	config.JournalDirectory = journalDir
	config.AddressCacheSizes = sizes
	config.AddressCachePersist = true
	db := New(rawdb.NewMemoryDatabase(), &config, false)
	defer db.Close()

	if err := db.Disable(); err != nil {
		t.Fatalf("Disable returned an unexpected error: %v", err)
	}

	h := installCapturingHandler(t)
	if err := db.Enable(types.EmptyRootHash); err != nil {
		t.Fatalf("Enable returned an unexpected error: %v", err)
	}
	db.tree.bottom().nodes.wg.Wait()

	if h.contains("Reloaded address cache snapshot") {
		t.Fatal("expected Enable not to reload the pre-sync snapshot")
	}
	if !h.contains("Starting storage trie preload") {
		t.Fatal("expected Enable to run a cold preload")
	}
}
