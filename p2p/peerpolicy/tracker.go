// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/common/mclock"
)

const (
	windowWidth = 10 * time.Second
	windowCount = 6
	maxPeers    = 1024
	maxHashes   = 128
)

type bucket struct {
	tick          int64
	reasons       uint32
	usage         [familyCount]usage
	repeats       uint64
	repeatedBytes uint64
}

type usage struct{ items, bytes uint64 }
type hashKey struct {
	family Family
	hash   common.Hash
}
type announcement struct {
	tick  int64
	count uint8
}
type peerRecord struct {
	windows [windowCount]bucket
	hashes  lru.BasicLRU[hashKey, announcement]
}

type Tracker struct {
	mu      sync.Mutex
	clock   mclock.Clock
	peers   lru.BasicLRU[string, *peerRecord]
	metrics *observationMetrics
}

func New(clock mclock.Clock) *Tracker {
	return &Tracker{clock: clock, peers: lru.NewBasicLRU[string, *peerRecord](maxPeers), metrics: newObservationMetrics()}
}

// Observe retains no payloads and performs no I/O. Records survive reconnects
// until LRU eviction; that bound is not a substitute for node-wide serving caps.
func (t *Tracker) Observe(id string, event Evidence) {
	if id == "" || len(id) > 128 || event.Reason >= reasonCount || event.Family >= familyCount {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tick := int64(t.clock.Now()) / int64(windowWidth)
	p := t.record(id)
	previousAction := actionFor(p.risk(tick, nil))
	b := &p.windows[tick%windowCount]
	if b.tick != tick {
		*b = bucket{tick: tick}
	}
	t.metrics.traffic(event)
	reason := event.Reason
	if reason == None {
		reason = p.traffic(b, tick, event)
	}
	if reason != None {
		t.metrics.reasons[reason].Inc(1)
		bit := uint32(1) << reason
		if b.reasons&bit == 0 {
			t.metrics.windows[reason].Inc(1)
		}
		b.reasons |= bit
	}
	risk := p.risk(tick, nil)
	action := actionFor(risk)
	t.metrics.risk.Update(int64(risk))
	if action != previousAction {
		t.metrics.transition(action)
	}
}

func (t *Tracker) record(id string) *peerRecord {
	if p, ok := t.peers.Get(id); ok {
		return p
	}
	p := &peerRecord{hashes: lru.NewBasicLRU[hashKey, announcement](maxHashes)}
	if t.peers.Add(id, p) {
		t.metrics.evictions.Inc(1)
	}
	return p
}

func (p *peerRecord) traffic(b *bucket, tick int64, event Evidence) Reason {
	if event.Family == Other {
		return None
	}
	a := allowances[event.Family]
	u := &b.usage[event.Family]
	u.items = saturatingAdd(u.items, event.Items, a.items+1)
	u.bytes = saturatingAdd(u.bytes, event.Bytes, a.bytes+1)
	repeated := p.announcements(b, tick, event)
	repeatedBodies := p.repeatedBodies(b, tick, event)
	if u.items > a.items || u.bytes > a.bytes {
		return a.reason
	}
	if repeated {
		return AnnouncementRepetition
	}
	if repeatedBodies {
		return ServingRepetition
	}
	return None
}

func (p *peerRecord) announcements(b *bucket, tick int64, event Evidence) bool {
	if event.Family != BlockAnnouncements && event.Family != TransactionAnnouncements {
		return false
	}
	// Bound work independently of packet length; total volume still counts every item.
	for _, hash := range event.Hashes[:min(len(event.Hashes), maxHashes)] {
		key := hashKey{event.Family, hash}
		entry, ok := p.hashes.Get(key)
		if !ok || tick-entry.tick >= windowCount {
			entry = announcement{tick: tick}
		}
		if entry.count < 3 {
			entry.count++
		}
		if entry.count == 3 {
			b.repeats = min(b.repeats+1, 33)
		}
		p.hashes.Add(key, entry)
	}
	return b.repeats > 32
}

func saturatingAdd(current, delta, limit uint64) uint64 {
	if delta >= limit-current {
		return limit
	}
	return current + delta
}

func (p *peerRecord) risk(tick int64, counts map[string]uint64) uint64 {
	var risk uint64
	for _, b := range p.windows {
		if tick-b.tick >= windowCount || b.tick > tick {
			continue
		}
		risk += b.score(counts)
	}
	return min(risk, 100)
}

func actionFor(risk uint64) string {
	if risk >= 100 {
		return "jail"
	}
	if risk >= 40 {
		return "throttle"
	}
	return "none"
}

func (t *Tracker) Snapshot(id string) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := Snapshot{Mode: "observe", Action: "none", Windows: make(map[string]uint64)}
	if p, ok := t.peers.Peek(id); ok {
		result.Risk = p.risk(int64(t.clock.Now())/int64(windowWidth), result.Windows)
		result.Action = actionFor(result.Risk)
	}
	return result
}

// score takes the strongest signal until asynchronous producers share delivery
// provenance. This undercounts independent incidents rather than stacking them.
func (b bucket) score(counts map[string]uint64) uint64 {
	var strongest uint64
	for reason := InvalidEncoding; reason < reasonCount; reason++ {
		if b.reasons&(uint32(1)<<reason) == 0 {
			continue
		}
		strongest = max(strongest, weight(reason))
		if counts != nil {
			counts[reason.String()]++
		}
	}
	return strongest
}
