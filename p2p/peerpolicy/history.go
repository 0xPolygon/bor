// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"hash/maphash"

	"github.com/ethereum/go-ethereum/common"
)

type historySlot struct {
	hash  common.Hash
	entry announcement
	rank  uint64
	used  bool
}

// Each keyed slot retains its lowest-ranked exact hash. Unlike recency eviction,
// cycling a large working set preserves a stable sample. This is a lower bound:
// unsampled hashes cannot contribute evidence, and collisions never imply repeats.
type hashHistory struct {
	seed  maphash.Seed
	slots [maxHashes]historySlot
}

// Independent samples prevent announcement churn from evicting refused bodies.
type peerHistories struct {
	blocks        hashHistory
	transactions  hashHistory
	refusedBodies hashHistory
}

func newPeerHistories() peerHistories {
	return peerHistories{blocks: newHashHistory(), transactions: newHashHistory(), refusedBodies: newHashHistory()}
}

func newHashHistory() hashHistory { return hashHistory{seed: maphash.MakeSeed()} }

// Pointers avoid per-visit hash copies; history slots still own their values.
func (h *hashHistory) rank(hash *common.Hash) uint64 { return maphash.Bytes(h.seed, hash[:]) }

// sample spreads at most maxSampledHashes visits across the packet.
// Identical packets keep the same sampled positions for this history.
func (h *hashHistory) sample(hashes []common.Hash) (start, step int) {
	if len(hashes) <= maxSampledHashes {
		return 0, 1
	}
	step = (len(hashes) + maxSampledHashes - 1) / maxSampledHashes
	return int(h.rank(&hashes[0]) % uint64(step)), step
}

func (h *hashHistory) Get(hash *common.Hash) (announcement, bool) {
	slot := &h.slots[h.rank(hash)%maxHashes]
	return slot.entry, slot.used && slot.hash == *hash
}

func (h *hashHistory) Add(hash *common.Hash, entry announcement) {
	rank := h.rank(hash)
	slot := &h.slots[rank%maxHashes]
	if !slot.used || slot.hash == *hash || entry.tick-slot.entry.tick >= windowCount || rank < slot.rank {
		*slot = historySlot{hash: *hash, entry: entry, rank: rank, used: true}
	}
}

func (h *hashHistory) record(hash *common.Hash, tick int64) bool {
	entry, ok := h.Get(hash)
	if !ok || tick-entry.tick >= windowCount {
		entry = announcement{tick: tick}
	}
	if entry.count < 3 {
		entry.count++
	}
	h.Add(hash, entry)

	return entry.count == 3
}

func (h *hashHistory) Len() int {
	n := 0
	for _, slot := range h.slots {
		if slot.used {
			n++
		}
	}
	return n
}

// SetExempt is called with the authenticated connection class before observation.
// Retaining the class also covers asynchronous outcomes after disconnection.
func (t *Tracker) SetExempt(id string, exempt bool) {
	if id == "" || len(id) > 128 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.record(id).setExempt(t.connectionClass(id, exempt))
}

func (p *peerRecord) setExempt(exempt bool) {
	if p.exempt != exempt {
		p.windows = [windowCount]bucket{}
		p.hashes = newPeerHistories()
		p.refusalSequence = 0
	}
	p.exempt = exempt
}
