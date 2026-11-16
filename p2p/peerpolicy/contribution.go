// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"math"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
)

// Contribution uses a rolling window and a readiness-gated grace period.
const (
	activityWidth = time.Minute
	activityCount = 30
	activityGrace = IdleTimeout
)

const IdleTimeout = 10 * time.Minute

// ConnectionActivity is shared by transport liveness and contribution scoring.
// Protocol reads update it without acquiring the tracker mutex.
type ConnectionActivity struct {
	last             atomic.Int64
	auxiliary        atomic.Int64
	started          atomic.Bool
	idleDisconnected atomic.Bool
}

func (a *ConnectionActivity) Start(now mclock.AbsTime) {
	a.last.Store(int64(now))
	a.auxiliary.Store(-1)
	a.started.Store(true)
}

func (a *ConnectionActivity) Observe(now mclock.AbsTime, auxiliary bool) {
	last := a.last.Load()
	if last >= 0 && a.last.CompareAndSwap(last, int64(now)) && auxiliary {
		a.auxiliary.Store(int64(now))
	}
}

func (a *ConnectionActivity) Last() (mclock.AbsTime, bool) {
	if !a.started.Load() {
		return 0, false
	}
	last := a.last.Load()
	return mclock.AbsTime(last), last >= 0
}

func (a *ConnectionActivity) Closing() bool { return a.last.Load() < 0 }

func (a *ConnectionActivity) Close() { a.last.Store(-1) }

func (a *ConnectionActivity) MarkIdleDisconnect() { a.idleDisconnected.Store(true) }

type ActivityEvent struct {
	ReceivedMessages     uint64         `json:"receivedMessages"`
	ReceivedBytes        uint64         `json:"receivedBytes"`
	ServedResponses      uint64         `json:"servedResponses"`
	ServedBytes          uint64         `json:"servedBytes"`
	AcceptedTransactions uint64         `json:"acceptedTransactions"`
	UnclassifiedMessages uint64         `json:"unclassifiedMessages"`
	StartedAt            mclock.AbsTime `json:"-"`
}

// Scope is ETH only: neither remote forwarding nor SNAP/WIT usefulness is known.
// Accepted transactions are local pool admissions, not evidence of inclusion.
type ContributionSnapshot struct {
	ActivityEvent
	Mode                       string  `json:"mode"`
	Scope                      string  `json:"scope"`
	UsefulDataCoverage         string  `json:"usefulDataCoverage"`
	Status                     string  `json:"status"`
	Risk                       uint64  `json:"risk"`
	RiskReason                 string  `json:"riskReason,omitempty"`
	Protected                  bool    `json:"protected"`
	ConnectionKnown            bool    `json:"connectionKnown"`
	ServedMinutes              uint64  `json:"servedMinutes"`
	ConnectionSeconds          uint64  `json:"connectionSeconds"`
	ObservedSeconds            uint64  `json:"observedSeconds"`
	WindowSeconds              uint64  `json:"windowSeconds"`
	GraceSeconds               uint64  `json:"graceSeconds"`
	LastContributionAgeSeconds *uint64 `json:"lastContributionAgeSeconds,omitempty"`
	ProtocolIdleSeconds        *uint64 `json:"protocolIdleSeconds,omitempty"`
	ProtocolActivityScope      string  `json:"protocolActivityScope,omitempty"`
	DisconnectReason           string  `json:"disconnectReason,omitempty"`
	auxiliaryActive            bool
}

type activityBucket struct {
	tick int64
	ActivityEvent
}

type activityRecord struct {
	windows         [activityCount]activityBucket
	registered      bool
	connectedAt     mclock.AbsTime
	observedAt      mclock.AbsTime
	readyStarted    bool
	resetAt         mclock.AbsTime
	pausedFor       mclock.AbsTime
	acceptAfter     mclock.AbsTime
	lastUseful      mclock.AbsTime
	hasContribution bool
	connection      *ConnectionActivity
}

func (t *Tracker) BindConnection(id string, startedAt mclock.AbsTime, connection *ConnectionActivity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.peers.Peek(id); ok && p.activity.registered && p.activity.connectedAt == startedAt {
		p.activity.connection = connection
	}
}

func (t *Tracker) IdleSince(id string, connection *ConnectionActivity) (mclock.AbsTime, bool) {
	if !t.mu.TryLock() {
		return 0, false
	}
	defer t.mu.Unlock()
	return t.idleSince(id, connection)
}

func (t *Tracker) idleSince(id string, connection *ConnectionActivity) (mclock.AbsTime, bool) {
	p, ok := t.peers.Peek(id)
	if !ok || connection == nil || p.activity.connection != connection {
		return 0, false
	}
	now := t.clock.Now()
	t.prepareActivity(&p.activity, now)
	last, live := connection.Last()
	contribution := p.activity.snapshot(now, t.ready, p.exempt)
	return last, live && contribution.Status == "idle"
}

// The server checks current connection flags. Never wait for observation or
// invoke its class resolver from the server's connection-management loop.
func (t *Tracker) RetireIdle(id string, connection *ConnectionActivity, last mclock.AbsTime) bool {
	if !t.mu.TryLock() {
		return false
	}
	defer t.mu.Unlock()
	current, idle := t.idleSince(id, connection)
	return idle && current == last && connection.last.CompareAndSwap(int64(last), -1)
}

// Connected begins a fresh connection observation without erasing misconduct.
func (t *Tracker) Connected(id string) mclock.AbsTime {
	if id == "" || len(id) > 128 {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock.Now()
	if p := t.record(id); p != nil {
		p.activity = activityRecord{registered: true, connectedAt: now, observedAt: now,
			readyStarted: t.readyStarted, resetAt: t.resetAt, pausedFor: t.pauseTotal(now), acceptAfter: now}
	}
	return now
}

// RestoreSession recovers connection age after eviction, restarting observation
// coverage because its history was lost. current runs under the tracker lock and
// must confirm this exact session without calling back into the tracker.
func (t *Tracker) RestoreSession(id string, startedAt mclock.AbsTime, current func() bool) {
	if id == "" || len(id) > 128 || current == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !current() {
		return
	}
	p := t.record(id)
	if p == nil || p.activity.registered {
		return
	}
	now := t.clock.Now()
	p.activity = activityRecord{registered: true, connectedAt: startedAt, observedAt: now,
		readyStarted: t.readyStarted, resetAt: t.resetAt, pausedFor: t.pauseTotal(now), acceptAfter: now}
}

func (a *activityRecord) observe(now mclock.AbsTime, event ActivityEvent) {
	if event == (ActivityEvent{}) {
		return
	}
	if a.registered && event.StartedAt < a.connectedAt {
		return
	}
	// Delayed admissions from incomplete coverage cannot establish contribution.
	if event.AcceptedTransactions > 0 && event.StartedAt < a.acceptAfter {
		event.AcceptedTransactions = 0
		event.UnclassifiedMessages = saturatingAdd(event.UnclassifiedMessages, 1, math.MaxUint64)
	}
	tick := int64(now) / int64(activityWidth)
	b := &a.windows[tick%activityCount]
	if b.tick != tick {
		*b = activityBucket{tick: tick}
	}
	b.ActivityEvent.add(event)
	if event.AcceptedTransactions > 0 {
		a.lastUseful, a.hasContribution = now, true
	}
}

func (a *ActivityEvent) add(event ActivityEvent) {
	a.ReceivedMessages = saturatingAdd(a.ReceivedMessages, event.ReceivedMessages, math.MaxUint64)
	a.ReceivedBytes = saturatingAdd(a.ReceivedBytes, event.ReceivedBytes, math.MaxUint64)
	a.ServedResponses = saturatingAdd(a.ServedResponses, event.ServedResponses, math.MaxUint64)
	a.ServedBytes = saturatingAdd(a.ServedBytes, event.ServedBytes, math.MaxUint64)
	a.AcceptedTransactions = saturatingAdd(a.AcceptedTransactions, event.AcceptedTransactions, math.MaxUint64)
	a.UnclassifiedMessages = saturatingAdd(a.UnclassifiedMessages, event.UnclassifiedMessages, math.MaxUint64)
}

func (e Evidence) activityOnly() bool {
	return e.Activity != (ActivityEvent{}) && e.Reason == None && e.Family == Other &&
		!e.Refused && e.Items == 0 && e.Bytes == 0 && len(e.Hashes) == 0 && len(e.ObjectBytes) == 0
}

func contributionSnapshot() *ContributionSnapshot {
	return &ContributionSnapshot{Mode: "observe", Scope: "eth", UsefulDataCoverage: "transaction-admission", Status: "unknown",
		WindowSeconds: uint64(activityCount * activityWidth / time.Second), GraceSeconds: uint64(activityGrace / time.Second)}
}

func (a *activityRecord) snapshot(now mclock.AbsTime, ready, protected bool) *ContributionSnapshot {
	result := contributionSnapshot()
	result.Protected = protected
	result.ConnectionKnown = a.registered
	if a.registered {
		result.ConnectionSeconds = uint64(max(now-a.connectedAt, 0) / mclock.AbsTime(time.Second))
		if a.readyStarted {
			result.ObservedSeconds = uint64(max(now-a.observedAt, 0) / mclock.AbsTime(time.Second))
		}
	}
	tick := int64(now) / int64(activityWidth)
	for _, b := range a.windows {
		if tick-b.tick >= activityCount || b.tick > tick {
			continue
		}
		result.ActivityEvent.add(b.ActivityEvent)
		if b.ServedResponses > 0 {
			result.ServedMinutes++
		}
	}
	if a.hasContribution {
		age := uint64(max(now-a.lastUseful, 0) / mclock.AbsTime(time.Second))
		result.LastContributionAgeSeconds = &age
	}
	ready = a.connectionSnapshot(now, ready, result)
	result.classify(a.registered && ready)
	result.score()
	return result
}

func (a *activityRecord) connectionSnapshot(now mclock.AbsTime, ready bool, result *ContributionSnapshot) bool {
	if a.connection == nil {
		return ready
	}
	result.ProtocolActivityScope = "all-subprotocols"
	if a.connection.idleDisconnected.Load() {
		result.DisconnectReason = "idle"
	}
	last, live := a.connection.Last()
	if !live {
		return false
	}
	age := uint64(max(now-last, 0) / mclock.AbsTime(time.Second))
	result.ProtocolIdleSeconds = &age
	auxiliary := a.connection.auxiliary.Load()
	result.auxiliaryActive = auxiliary >= 0 && now.Sub(mclock.AbsTime(auxiliary)) < activityCount*activityWidth
	return ready
}

func (s *ContributionSnapshot) classify(ready bool) {
	switch {
	case !ready:
		s.Status = "unknown"
	case s.ObservedSeconds < s.GraceSeconds:
		s.Status = "warming-up"
	case s.ProtocolIdleSeconds != nil && *s.ProtocolIdleSeconds >= s.GraceSeconds:
		s.Status = "idle"
	case s.AcceptedTransactions > 0:
		s.Status = "contributing"
	case s.UnclassifiedMessages > 0 || s.auxiliaryActive:
		s.Status = "unknown"
	case s.ServedMinutes >= 3:
		s.Status = "request-only"
	case s.ActivityEvent == (ActivityEvent{}) && s.ProtocolIdleSeconds == nil:
		s.Status = "idle"
	default:
		s.Status = "unknown"
	}
}

// Limited contribution is weaker evidence than a protocol fault. Its score
// stays bounded rather than accumulating with elapsed time or scrape frequency.
const behaviorRisk = 20

func (s *ContributionSnapshot) score() {
	if !s.Protected && (s.Status == "idle" || s.Status == "request-only") {
		s.Risk = behaviorRisk
		s.RiskReason = s.Status
	}
}

// Short readiness gaps pause the grace clock; long gaps restart it. Neither
// erases traffic that was actually observed within the rolling window.
func (t *Tracker) SetReady(ready bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ready == t.ready {
		return
	}
	now := t.clock.Now()
	if ready {
		pause := now - t.readyAt
		if t.readyStarted {
			t.pausedFor += pause
		}
		if !t.readyStarted || pause >= mclock.AbsTime(activityWidth) {
			t.resetAt, t.resetPausedFor = now, t.pausedFor
		}
		t.readyStarted = true
	}
	t.ready, t.readyAt = ready, now
}

func (t *Tracker) pauseTotal(now mclock.AbsTime) mclock.AbsTime {
	if t.readyStarted && !t.ready {
		return t.pausedFor + now - t.readyAt
	}
	return t.pausedFor
}

func (t *Tracker) prepareActivity(a *activityRecord, now mclock.AbsTime) {
	if !t.readyStarted {
		return
	}
	if !a.readyStarted || a.resetAt != t.resetAt {
		a.observedAt = max(a.connectedAt, t.resetAt)
		a.resetAt, a.pausedFor, a.readyStarted = t.resetAt, t.resetPausedFor, true
	}
	paused := t.pauseTotal(now)
	a.observedAt += max(paused-a.pausedFor, 0)
	a.pausedFor = paused
	a.acceptAfter = max(a.acceptAfter, a.connectedAt, t.readyAt)
}
