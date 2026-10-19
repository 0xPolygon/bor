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

func (h *hashHistory) Get(hash common.Hash) (announcement, bool) {
	rank := maphash.Bytes(h.seed, hash[:])
	slot := &h.slots[rank%maxHashes]
	return slot.entry, slot.used && slot.hash == hash
}

func (h *hashHistory) Add(hash common.Hash, entry announcement) {
	rank := maphash.Bytes(h.seed, hash[:])
	slot := &h.slots[rank%maxHashes]
	if !slot.used || slot.hash == hash || entry.tick-slot.entry.tick >= windowCount || rank < slot.rank {
		*slot = historySlot{hash: hash, entry: entry, rank: rank, used: true}
	}
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
