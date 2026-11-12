// Copyright 2025 The go-ethereum Authors
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

// TestLogIteratorUpdateChainViewShortened checks that the log iterator rejects
// a target view update that shortens the chain down to the block the iterator
// is currently sitting on. The iterator's delimiter flag means that the next
// step reads block blockNumber+1, which does not exist in such a view, so
// accepting the update would make the renderer ask the view for a block number
// after its head.
func TestLogIteratorUpdateChainViewShortened(t *testing.T) {
	ts := newTestSetup(t)
	defer ts.close()

	ts.chain.addBlocks(10, 3, 2, 2, false)
	head := ts.chain.CurrentBlock().Number.Uint64()

	longView := NewChainView(ts.chain, head, ts.chain.GetCanonicalHash(head))
	if longView == nil {
		t.Fatal("failed to create chain view at head")
	}
	// shortView is the view produced by a "Shorten chain del=1" reorg; its head
	// is exactly the block the iterator is positioned at.
	shortView := NewChainView(ts.chain, head-1, ts.chain.GetCanonicalHash(head-1))
	if shortView == nil {
		t.Fatal("failed to create shortened chain view")
	}
	// The two views agree on the iterator's position, so matchViews alone does
	// not catch this case; without the explicit delimiter check the update
	// would be accepted.
	if !matchViews(shortView, longView, head-1) {
		t.Fatal("views unexpectedly diverge at the iterator position")
	}
	// Iterator sitting at the block delimiter of the last block before the
	// long view's head, i.e. about to step onto the head block.
	it := &logIterator{
		params:      &ts.params,
		chainView:   longView,
		blockNumber: head - 1,
		delimiter:   true,
	}
	if it.updateChainView(shortView) {
		t.Fatalf("updateChainView accepted a view whose head (%d) is the iterator's block", shortView.HeadNumber())
	}
	if it.chainView != longView {
		t.Fatal("chain view replaced even though the update was rejected")
	}
	// A view that still has a block after the iterator's position is fine.
	if !it.updateChainView(longView) {
		t.Fatal("updateChainView rejected a valid view update")
	}
	// A finished iterator does not step onto another block, so shortening the
	// view down to its position is harmless and must still be accepted.
	it2 := &logIterator{
		params:      &ts.params,
		chainView:   longView,
		blockNumber: head - 1,
		finished:    true,
	}
	if !it2.updateChainView(shortView) {
		t.Fatal("updateChainView rejected a valid update for a finished iterator")
	}
}

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
