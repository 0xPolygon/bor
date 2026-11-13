// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"math"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/log"
)

const (
	transitionPollInterval = time.Second
	peerTransitionInterval = 30 * time.Second
	transitionLogWindow    = time.Minute
	transitionLogLimit     = 60
	transitionLogBatch     = 8
)

type riskSample struct {
	risk    uint64
	reasons uint32
}

type peerTransitions struct {
	current, reported, peak riskSample
	recoveryReasons         uint32
	changes                 uint64
}

type transitionBudget struct {
	started mclock.AbsTime
	emitted int
	cursor  int
	// Cooldowns outlive peer-cache eviction without retaining identities forever.
	recent map[string]mclock.AbsTime
}

type riskTransition struct {
	peerID                  string
	current, previous, peak riskSample
	changes                 uint64
	protected               bool
}

func riskBand(risk uint64) string {
	if risk == 0 {
		return "zero"
	}
	if action := actionFor(risk); action != "none" {
		return action
	}
	return "positive"
}

func (p *peerRecord) captureTransition(tick int64, risk uint64) {
	sample := riskSample{risk: risk}
	for _, b := range p.windows {
		if tick-b.tick < windowCount && b.tick <= tick {
			sample.reasons |= b.reasons
		}
	}
	s := &p.transitions
	if riskBand(risk) != riskBand(s.current.risk) {
		s.changes = saturatingAdd(s.changes, 1, math.MaxUint64)
		if risk == 0 {
			s.recoveryReasons = s.current.reasons
		}
	}
	if risk >= s.peak.risk {
		s.peak = sample
	}
	s.current = sample
}

func (b *transitionBudget) refresh(now mclock.AbsTime) {
	if now-b.started >= mclock.AbsTime(transitionLogWindow) {
		b.started, b.emitted = now, 0
	}
	if b.recent == nil {
		b.recent = make(map[string]mclock.AbsTime)
	}
	for id, at := range b.recent {
		if now-at >= mclock.AbsTime(peerTransitionInterval) {
			delete(b.recent, id)
		}
	}
}

func (b *transitionBudget) allows(id string) bool {
	_, limited := b.recent[id]
	return !limited && len(b.recent) < 2*transitionLogLimit && b.emitted < transitionLogLimit
}

func (p *peerRecord) takeTransition(id string) riskTransition {
	s := &p.transitions
	entry := riskTransition{id, s.current, s.reported, s.peak, s.changes, p.exempt}
	if entry.current.risk == 0 {
		entry.current.reasons = s.recoveryReasons
	}
	s.reported, s.peak = s.current, s.current
	s.changes, s.recoveryReasons = 0, 0
	return entry
}

// Pending transitions share the bounded peer cache. Coalescing retains peak risk;
// the global budget also bounds output when identities churn through that cache.
func (t *Tracker) drainTransitions() []riskTransition {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock.Now()
	t.transitions.refresh(now)
	entries := make([]riskTransition, 0, transitionLogBatch)
	keys := t.peers.Keys()
	start := t.transitions.cursor
	for i := range keys {
		index := (start + i) % len(keys)
		id := keys[index]
		p, _ := t.peers.Peek(id)
		p.setExempt(t.connectionClass(id, p.exempt))
		tick := int64(now) / int64(windowWidth)
		p.captureTransition(tick, p.risk(tick, nil))
		if p.transitions.changes == 0 || len(entries) == transitionLogBatch || !t.transitions.allows(id) {
			continue
		}
		entries = append(entries, p.takeTransition(id))
		t.transitions.emitted++
		t.transitions.cursor = index + 1
		t.transitions.recent[id] = now
	}
	return entries
}

// RunTransitionLogger runs independently of metrics until done closes. The
// handler owns this worker; logging never runs on an observation goroutine.
func (t *Tracker) RunTransitionLogger(done <-chan struct{}) {
	t.runTransitionLogger(done, writeRiskTransition)
}

func (t *Tracker) runTransitionLogger(done <-chan struct{}, emit func(riskTransition)) {
	timer := t.clock.NewTimer(transitionPollInterval)
	defer timer.Stop()
	for {
		select {
		case <-done:
			return
		case <-timer.C():
			for _, entry := range t.drainTransitions() {
				select {
				case <-done:
					return
				default:
					emit(entry)
				}
			}
			timer.Reset(transitionPollInterval)
		}
	}
}

func writeRiskTransition(entry riskTransition) {
	log.Info("Peer abuse risk changed", "peer_id", entry.peerID, "mode", "observe", "score_type", "abuse",
		"risk", entry.current.risk, "previous_risk", entry.previous.risk,
		"from_band", riskBand(entry.previous.risk), "to_band", riskBand(entry.current.risk),
		"would_action", actionFor(entry.current.risk), "reasons", transitionReasons(entry.current.reasons),
		"peak_risk", entry.peak.risk, "peak_reasons", transitionReasons(entry.peak.reasons),
		"transition_count", entry.changes, "protected", entry.protected)
}

func transitionReasons(bits uint32) string {
	var names []string
	for reason := InvalidEncoding; reason < reasonCount; reason++ {
		if bits&(uint32(1)<<reason) != 0 {
			names = append(names, reason.String())
		}
	}
	return strings.Join(names, ",")
}
