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

package state

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

const (
	// warmRingMaxGenerations bounds how many blocks' commit node sets the ring
	// retains. A block's write set overlaps only ~20% with its immediate
	// predecessor but far more with the preceding few dozen blocks (hot
	// contracts and the trie's upper levels repeat constantly), so depth is
	// what converts the carry from a consecutive-block optimisation into a
	// working-set cache.
	warmRingMaxGenerations = 128

	// warmRingMaxBytes bounds the retained blob payload. Blobs are shared with
	// the committed trienode sets, so this measures extended lifetime, not
	// extra copies. At mainnet catch-up rates (~2-8MB of committed nodes per
	// block) the byte budget is the binding constraint, keeping roughly the
	// last 30-100 blocks.
	warmRingMaxBytes = 256 * 1024 * 1024
)

var (
	warmRingNodesGauge       = metrics.NewRegisteredGauge("chain/imports/pipelined/warm_ring/nodes", nil)
	warmRingBytesGauge       = metrics.NewRegisteredGauge("chain/imports/pipelined/warm_ring/bytes", nil)
	warmRingGenerationsGauge = metrics.NewRegisteredGauge("chain/imports/pipelined/warm_ring/generations", nil)
)

// WarmNodeRing is a bounded, hash-verified cache of the trie nodes committed
// by the most recent pipelined SRC runs. Each Add records one block's commit
// node set as a generation; lookups consult a single merged map so cost does
// not grow with depth, and eviction drops whole generations oldest-first when
// the generation or byte budget is exceeded.
//
// Correctness never depends on the ring's contents: the node hash is part of
// the lookup key, so entries from evicted forks or stale paths are structural
// misses and the caller falls through to pathdb. Reorgs therefore need no
// invalidation — abandoned-fork entries are dead weight until evicted.
type WarmNodeRing struct {
	mu       sync.RWMutex
	nodes    map[warmKey]ringEntry
	gens     []warmRingGen // oldest first
	genSeq   uint64
	bytes    int
	maxGens  int
	maxBytes int
}

// ringEntry tags each blob with the generation that most recently wrote it,
// so evicting an old generation never removes a key a newer generation also
// produced (the newer Add refreshed the ownership tag).
type ringEntry struct {
	blob []byte
	gen  uint64
}

// warmRingGen records which keys a generation inserted or refreshed, for
// oldest-first eviction.
type warmRingGen struct {
	id   uint64
	keys []warmKey
}

// NewWarmNodeRing constructs a ring with the default generation and byte
// budgets.
func NewWarmNodeRing() *WarmNodeRing {
	return newWarmNodeRing(warmRingMaxGenerations, warmRingMaxBytes)
}

func newWarmNodeRing(maxGens, maxBytes int) *WarmNodeRing {
	return &WarmNodeRing{
		nodes:    make(map[warmKey]ringEntry),
		maxGens:  maxGens,
		maxBytes: maxBytes,
	}
}

// Add records one committed block's node set as the newest generation and
// evicts oldest generations past the budgets. Blobs are referenced, not
// copied; deleted nodes (empty blobs) are skipped. Nil-safe no-op.
func (r *WarmNodeRing) Add(merged *trienode.MergedNodeSet) {
	if r == nil || merged == nil || len(merged.Sets) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.genSeq++
	gen := warmRingGen{id: r.genSeq}
	for owner, set := range merged.Sets {
		for path, n := range set.Nodes {
			if n == nil || len(n.Blob) == 0 {
				continue
			}
			key := warmKey{owner: owner, path: path, hash: n.Hash}
			if _, exists := r.nodes[key]; !exists {
				r.bytes += len(n.Blob)
			}
			// Existing keys (a node rewritten to identical content, or fork
			// double-commits) are refreshed to the newest generation so early
			// eviction can't drop them while they're still current.
			r.nodes[key] = ringEntry{blob: n.Blob, gen: r.genSeq}
			gen.keys = append(gen.keys, key)
		}
	}
	if len(gen.keys) > 0 {
		r.gens = append(r.gens, gen)
	}
	r.evictLocked()
	warmRingNodesGauge.Update(int64(len(r.nodes)))
	warmRingBytesGauge.Update(int64(r.bytes))
	warmRingGenerationsGauge.Update(int64(len(r.gens)))
}

// evictLocked drops oldest generations while either budget is exceeded. The
// newest generation is never evicted, so one oversized block can temporarily
// exceed the byte budget rather than leave the ring empty.
func (r *WarmNodeRing) evictLocked() {
	for len(r.gens) > 1 && (len(r.gens) > r.maxGens || r.bytes > r.maxBytes) {
		oldest := r.gens[0]
		r.gens = r.gens[1:]
		for _, key := range oldest.keys {
			entry, ok := r.nodes[key]
			if !ok || entry.gen != oldest.id {
				continue // refreshed by a newer generation
			}
			r.bytes -= len(entry.blob)
			delete(r.nodes, key)
		}
	}
}

// Lookup implements WarmNodeSource. The expected hash is part of the key, so
// a hit is correct by construction.
func (r *WarmNodeRing) Lookup(owner common.Hash, path []byte, expectedHash common.Hash) ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.nodes[warmKey{owner: owner, path: string(path), hash: expectedHash}]
	if !ok {
		return nil, false
	}
	return entry.blob, true
}

// Len implements WarmNodeSource. Safe on a nil ring.
func (r *WarmNodeRing) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}
