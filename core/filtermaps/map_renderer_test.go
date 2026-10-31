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
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
)

// A corrupt index where the map before the head map ends after it must make
// loadHeadSnapshot fail instead of sizing a slice from a wrapped length.
func TestLoadHeadSnapshotInvertedRange(t *testing.T) {
	ts := newTestSetup(t)
	defer ts.close()

	ts.chain.addBlocks(100, 5, 2, 4, true)
	ts.setHistory(0, false)
	ts.fm.WaitIdle()

	fm := ts.fm
	if fm.indexedRange.maps.AfterLast() < 2 {
		t.Fatalf("need at least two rendered maps, have %d", fm.indexedRange.maps.AfterLast())
	}
	head := fm.indexedRange.maps.Last()
	lastBlock, _, err := fm.getLastBlockOfMap(head)
	if err != nil {
		t.Fatal(err)
	}
	setPrevLastBlock := func(number uint64) {
		rawdb.WriteFilterMapLastBlock(ts.db, head-1, number, common.Hash{})
		fm.lastBlockCache.Remove(head - 1)
	}

	// The previous map ending at the head map's last block leaves an empty
	// range, which is still valid.
	setPrevLastBlock(lastBlock)
	if err := fm.loadHeadSnapshot(); err != nil {
		t.Fatalf("empty head range rejected: %v", err)
	}
	// Ending one block later inverts the range.
	setPrevLastBlock(lastBlock + 1)
	err = fm.loadHeadSnapshot()
	if err == nil || !strings.Contains(err.Error(), "underflow") {
		t.Fatalf("inverted head range: have %v, want an underflow error", err)
	}
}
