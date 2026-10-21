// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"math/bits"
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
	transitions     peerTransitions
	windows         [windowCount]bucket
	hashes          peerHistories
	exempt          bool
	refusalSequence uint64
}

type Tracker struct {
	mu             sync.Mutex
	transitions    transitionBudget
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
	if p == nil {
		return
	}
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
	if p == nil {
		return
	}
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
	tick := int64(now) / int64(windowWidth)
	previousRisk := p.risk(tick, nil)
	p.captureTransition(tick, previousRisk)
	previousAction := actionFor(previousRisk)
	b := &p.windows[tick%windowCount]
	if b.tick != tick {
		*b = bucket{tick: tick}
	}
	t.metrics.traffic(event)
	for reasons := t.evidenceReasons(p, b, tick, event); reasons != 0; {
		reason := Reason(bits.TrailingZeros32(reasons))
		bit := uint32(1) << reason
		reasons &^= bit
		t.metrics.reasons[reason].Inc(1)
		if b.reasons&bit == 0 {
			t.metrics.windows[reason].Inc(1)
		}
		b.reasons |= bit
	}
	risk := p.risk(tick, nil)
	p.captureTransition(tick, risk)
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
	if !t.canAdmit() {
		return nil
	}
	p := &peerRecord{hashes: newPeerHistories()}
	if t.peers.Add(id, p) {
		t.metrics.evictions.Inc(1)
	}
	return p
}

func (t *Tracker) canAdmit() bool {
	if t.classResolver == nil || t.peers.Len() < maxPeers {
		return true
	}
	// Only new identities scan the bounded cache. Live sessions keep their
	// history even when every slot is occupied; excess identities wait for space.
	for range t.peers.Len() {
		id, peer, _ := t.peers.GetOldest()
		if !peer.activity.registered {
			return true
		}
		if _, connected := t.classResolver(id); !connected {
			return true
		}
		t.peers.Get(id)
	}
	return false
}

func (p *peerRecord) traffic(b *bucket, tick int64, event Evidence) uint32 {
	// Completed replies already passed native serving admission. Their volume
	// remains in traffic metrics, but only peer-specific refusals establish abuse.
	if event.Family == Other || event.Family == BodyReplies {
		return 0
	}
	a := allowances[event.Family]
	u := &b.usage[event.Family]
	u.items = saturatingAdd(u.items, event.Items, a.items+1)
	if a.bytes != 0 {
		u.bytes = saturatingAdd(u.bytes, event.Bytes, a.bytes+1)
	}
	var reasons uint32
	if p.announcements(b, tick, event) {
		reasons |= 1 << AnnouncementRepetition
	}
	if u.items > a.items || (a.bytes != 0 && u.bytes > a.bytes) {
		reasons |= 1 << a.reason
	}
	return reasons
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

func (t *Tracker) evidenceReasons(p *peerRecord, b *bucket, tick int64, event Evidence) uint32 {
	if event.Reason != None {
		return 1 << event.Reason
	}
	if !event.Refused {
		return p.traffic(b, tick, event)
	}
	reason := p.refused(b, tick, event)
	if reason != ServingRefused {
		t.metrics.reasons[ServingRefused].Inc(1)
	}
	return 1 << reason
}

func (t *Tracker) snapshotPeer(id string, p *peerRecord, now mclock.AbsTime, windows map[string]uint64) (Snapshot, bool) {
	connected := p.activity.registered
	if t.classResolver != nil {
		protected, live := t.classResolver(id)
		connected = connected && live
		if live {
			p.setExempt(protected)
		}
	}
	t.prepareActivity(&p.activity, now)
	contribution := p.activity.snapshot(now, t.ready && connected, p.exempt)
	abuse := p.risk(int64(now)/int64(windowWidth), windows)
	risk := max(abuse, contribution.Risk)
	return Snapshot{
		Mode: "observe", Risk: risk, AbuseRisk: abuse, Action: actionFor(risk),
		Windows: windows, Contribution: contribution,
	}, connected
}

// SetExempt is called with the authenticated connection class before observation.
// Retaining the class also covers asynchronous outcomes after disconnection.
func (t *Tracker) SetExempt(id string, exempt bool) {
	if id == "" || len(id) > 128 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p := t.record(id); p != nil {
		p.setExempt(t.connectionClass(id, exempt))
	}
}

func (p *peerRecord) setExempt(exempt bool) {
	if p.exempt != exempt {
		p.windows = [windowCount]bucket{}
		p.hashes = newPeerHistories()
		p.refusalSequence = 0
	}
	p.exempt = exempt
}

// SetClassResolver installs the authenticated live-connection lookup. The second
// result reports whether a connection exists; otherwise the retained class is used.
// Calls run under the tracker mutex, so the resolver must be bounded, perform no
// I/O, and never call back into the tracker. Any locks it takes must not be held
// by callers entering the tracker.
func (t *Tracker) SetClassResolver(resolve func(string) (exempt, connected bool)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.classResolver = resolve
}

// RefreshClass retains the latest connection class before disconnect. It neither
// creates a record nor treats the administrative update as peer activity.
func (t *Tracker) RefreshClass(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.peers.Peek(id); ok {
		p.setExempt(t.connectionClass(id, p.exempt))
	}
}

func (t *Tracker) connectionClass(id string, fallback bool) bool {
	if t.classResolver != nil {
		if exempt, connected := t.classResolver(id); connected {
			return exempt
		}
	}
	return fallback
}

// Only peer-specific admission refusals qualify. Successful delivery and global
// congestion do not establish repeated-download abuse.
func (p *peerRecord) refused(b *bucket, tick int64, event Evidence) Reason {
	p.refusalSequence++
	b.refusals = min(b.refusals+1, 3)
	repeated := false
	hashes := event.Hashes
	start, step := p.hashes.refusedBodies.sample(hashes)
	for i := start; i < len(hashes); i += step {
		hash := &hashes[i]
		entry, ok := p.hashes.refusedBodies.Get(hash)
		if !ok || tick-entry.tick >= windowCount {
			entry = announcement{tick: tick}
		}
		if entry.lastRefusal == p.refusalSequence {
			continue
		}
		entry.lastRefusal = p.refusalSequence
		if entry.count < 3 {
			entry.count++
		}
		repeated = repeated || entry.count == 3
		p.hashes.refusedBodies.Add(hash, entry)
	}
	if repeated {
		return ServingRepetition
	}
	if b.refusals >= 3 {
		return ServingVolume
	}
	return ServingRefused
}
