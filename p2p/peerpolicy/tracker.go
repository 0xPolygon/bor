// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/common/mclock"
)

const (
	windowWidth = 10 * time.Second
	windowCount = 6
	maxPeers    = 1024
	maxHashes   = 128
	// Bound per-packet history work under the shared tracker lock.
	maxSampledHashes = 4096
)

type bucket struct {
	tick     int64
	reasons  uint32
	usage    [familyCount]usage
	repeats  uint64
	refusals uint64
}

type (
	usage        struct{ items, bytes uint64 }
	announcement struct {
		tick        int64
		count       uint8
		lastRefusal uint64
	}
)

type peerRecord struct {
	activity        activityRecord
	windows         [windowCount]bucket
	hashes          peerHistories
	exempt          bool
	refusalSequence uint64
}

type Tracker struct {
	mu             sync.Mutex
	clock          mclock.Clock
	peers          lru.BasicLRU[string, *peerRecord]
	metrics        *observationMetrics
	ready          bool
	readyStarted   bool
	readyAt        mclock.AbsTime
	pausedFor      mclock.AbsTime
	resetAt        mclock.AbsTime
	resetPausedFor mclock.AbsTime
	classResolver  func(string) (bool, bool)
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
	p := t.record(id)
	p.setExempt(t.connectionClass(id, p.exempt))
	t.observe(p, event)
}

// ObserveWithClass applies the authenticated connection class and its evidence
// atomically. Observe uses the retained class for outcomes after disconnection.
func (t *Tracker) ObserveWithClass(id string, exempt bool, event Evidence) {
	if id == "" || len(id) > 128 || event.Reason >= reasonCount || event.Family >= familyCount {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.record(id)
	// Once a live resolver owns class changes, a captured connection flag must
	// not overwrite the final retained class after that connection disappears.
	if t.classResolver != nil {
		exempt = p.exempt
	}
	p.setExempt(t.connectionClass(id, exempt))
	t.observe(p, event)
}

// ObserveSession prevents a delayed callback from restoring an older connection
// class. Misconduct still belongs to the identity; contribution belongs to a session.
// False requires resolving the current connection class after record eviction.
func (t *Tracker) ObserveSession(id string, exempt bool, startedAt mclock.AbsTime, event Evidence) bool {
	if id == "" || len(id) > 128 || event.Reason >= reasonCount || event.Family >= familyCount {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.peers.Peek(id)
	if !ok || !p.activity.registered {
		return false
	}
	t.peers.Get(id)
	currentClass := p.exempt
	if t.classResolver == nil && p.activity.connectedAt == startedAt {
		currentClass = exempt
	}
	p.setExempt(t.connectionClass(id, currentClass))
	if event.Activity != (ActivityEvent{}) {
		event.Activity.StartedAt = startedAt
	}
	t.observe(p, event)
	return true
}

func (t *Tracker) observe(p *peerRecord, event Evidence) {
	now := t.clock.Now()
	t.prepareActivity(&p.activity, now)
	p.activity.observe(now, event.Activity)
	if p.exempt || event.activityOnly() {
		return
	}
	tick := int64(t.clock.Now()) / int64(windowWidth)
	previousAction := actionFor(p.risk(tick, nil))
	b := &p.windows[tick%windowCount]
	if b.tick != tick {
		*b = bucket{tick: tick}
	}
	t.metrics.traffic(event)
	reason := t.evidenceReason(p, b, tick, event)
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
	p := &peerRecord{hashes: newPeerHistories()}
	if t.peers.Add(id, p) {
		t.metrics.evictions.Inc(1)
	}
	return p
}

func (p *peerRecord) traffic(b *bucket, tick int64, event Evidence) Reason {
	// Completed replies already passed native serving admission. Their volume
	// remains in traffic metrics, but only peer-specific refusals establish abuse.
	if event.Family == Other || event.Family == BodyReplies {
		return None
	}
	a := allowances[event.Family]
	u := &b.usage[event.Family]
	u.items = saturatingAdd(u.items, event.Items, a.items+1)
	if a.bytes != 0 {
		u.bytes = saturatingAdd(u.bytes, event.Bytes, a.bytes+1)
	}
	repeated := p.announcements(b, tick, event)
	if u.items > a.items || (a.bytes != 0 && u.bytes > a.bytes) {
		return a.reason
	}
	if repeated {
		return AnnouncementRepetition
	}
	return None
}

func (p *peerRecord) announcements(b *bucket, tick int64, event Evidence) bool {
	if event.Family != BlockAnnouncements && event.Family != TransactionAnnouncements {
		return false
	}
	history := &p.hashes.blocks
	if event.Family == TransactionAnnouncements {
		history = &p.hashes.transactions
	}
	hashes := event.Hashes
	start, step := history.sample(hashes)
	for i := start; i < len(hashes); i += step {
		if history.record(&hashes[i], tick) {
			b.repeats = min(b.repeats+1, 33)
		}
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
	result.Contribution = contributionSnapshot()
	if p, ok := t.peers.Peek(id); ok {
		result, _ = t.snapshotPeer(id, p, t.clock.Now(), result.Windows)
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

func (t *Tracker) evidenceReason(p *peerRecord, b *bucket, tick int64, event Evidence) Reason {
	if event.Reason != None {
		return event.Reason
	}
	if !event.Refused {
		return p.traffic(b, tick, event)
	}
	reason := p.refused(b, tick, event)
	if reason != ServingRefused {
		t.metrics.reasons[ServingRefused].Inc(1)
	}
	return reason
}
