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
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
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
