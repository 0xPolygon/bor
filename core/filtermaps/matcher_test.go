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

package filtermaps

import (
	crand "crypto/rand"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func TestMatcher(t *testing.T) {
	ts := newTestSetup(t)
	defer ts.close()

	ts.chain.addBlocks(100, 10, 10, 4, true)
	ts.setHistory(0, false)
	ts.fm.WaitIdle()

	for i := 0; i < 2000; i++ {
		bhash := ts.chain.canonical[rand.Intn(len(ts.chain.canonical))]
		receipts := ts.chain.receipts[bhash]
		if len(receipts) == 0 {
			continue
		}
		receipt := receipts[rand.Intn(len(receipts))]
		if len(receipt.Logs) == 0 {
			continue
		}
		log := receipt.Logs[rand.Intn(len(receipt.Logs))]
		var ok bool
		addresses := make([]common.Address, rand.Intn(3))
		for i := range addresses {
			crand.Read(addresses[i][:])
		}
		if len(addresses) > 0 {
			addresses[rand.Intn(len(addresses))] = log.Address
			ok = true
		}
		topics := make([][]common.Hash, rand.Intn(len(log.Topics)+1))
		for j := range topics {
			topics[j] = make([]common.Hash, rand.Intn(3))
			for i := range topics[j] {
				crand.Read(topics[j][i][:])
			}
			if len(topics[j]) > 0 {
				topics[j][rand.Intn(len(topics[j]))] = log.Topics[j]
				ok = true
			}
		}
		if !ok {
			continue // cannot search for match-all pattern
		}
		mb := ts.fm.NewMatcherBackend()
		logs, err := GetPotentialMatches(t.Context(), mb, 0, 1000, addresses, topics)
		mb.Close()
		if err != nil {
			t.Fatalf("Log search error: %v", err)
		}
		var found bool
		for _, l := range logs {
			if l == log {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Log search did not return expected log (addresses: %v, topics: %v, expected log: %v)", addresses, topics, *log)
		}
	}
}

// TestMatcherDuringHeadMapRewrite checks that a search for the last fully
// indexed block returns its logs while the head map is being re-rendered.
//
// When the block after the last fully indexed one starts in an already rendered
// map and continues in the head map, adding a new head block makes the renderer
// rewrite the head map. While it does that, the indexed range temporarily ends
// at the last block of the previous map, and writeFinishedMaps releases
// indexLock between row batches. A search that runs in this window must still
// find the logs of the last indexed block, because that block stays in the
// valid range reported to the matcher, so an empty result would be accepted.
func TestMatcherDuringHeadMapRewrite(t *testing.T) {
	ts := newTestSetup(t)
	// More rows per map than rowsPerBatch, so that writeFinishedMaps releases
	// indexLock in the middle of writing a map.
	ts.params.logMapHeight = 11
	ts.params.logValuesPerMap = 12
	ts.params.deriveFields()
	defer ts.close()

	ts.chain.addBlocks(20, 10, 3, 2, false)
	ts.setHistory(0, false)
	ts.fm.WaitIdle()

	// Add a block with more log values (400*4*4 = 6400) than a single map can
	// hold (4096), so it crosses a map boundary and becomes the head block.
	ts.chain.addBlocks(1, 400, 4, 3, false)
	ts.fm.WaitIdle()

	head := ts.chain.CurrentBlock().Number.Uint64()
	searchBlock := head - 1
	receipts := ts.chain.receipts[ts.chain.canonical[searchBlock]]
	if len(receipts) == 0 || len(receipts[0].Logs) == 0 {
		t.Fatalf("no logs in block %d", searchBlock)
	}
	log := receipts[0].Logs[0]

	mb := ts.fm.NewMatcherBackend()
	defer mb.Close()

	if _, err := mb.SyncLogIndex(t.Context()); err != nil {
		t.Fatalf("SyncLogIndex error: %v", err)
	}

	// Pause the indexer while the temporary range is set, in the middle of the
	// head map rewrite. The hook never blocks after the test returns: pausedCh
	// is buffered and resumeCh is always closed, so the indexer can shut down
	// even if the test fails before the pause.
	pausedCh, resumeCh := make(chan struct{}, 1), make(chan struct{})
	resume := sync.OnceFunc(func() { close(resumeCh) })
	defer resume()
	armed := true
	ts.fm.testProcessEventsHook = func() {
		if !armed || !ts.fm.hasTempRange {
			return
		}
		armed = false
		pausedCh <- struct{}{}
		<-resumeCh
	}
	ts.chain.addBlocks(1, 2, 1, 1, false)

	select {
	case <-pausedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("indexer did not pause during the head map rewrite")
	}
	logs, err := GetPotentialMatches(t.Context(), mb, searchBlock, searchBlock, []common.Address{log.Address}, nil)
	resume()
	if err != nil {
		t.Fatalf("Log search error: %v", err)
	}

	sr, err := mb.SyncLogIndex(t.Context())
	if err != nil {
		t.Fatalf("SyncLogIndex error: %v", err)
	}
	if !sr.ValidBlocks.Includes(searchBlock) {
		t.Fatalf("block %d not in valid range %v, the search result would be discarded", searchBlock, sr.ValidBlocks)
	}

	var found bool
	for _, l := range logs {
		if l == log {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("search for block %d returned %d logs, expected log not found: %v", searchBlock, len(logs), *log)
	}
}
