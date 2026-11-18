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

const (
	activityWidth               = time.Minute
	activityCount               = 30
	activityGrace               = 10 * time.Minute
	requestOnlyMinServedMinutes = 3
	behaviorRisk                = 20
)

const IdleTimeout = 10 * time.Minute

type contributionStatusKind uint8

const (
	contributionUnknown contributionStatusKind = iota
	contributionWarmingUp
	contributionUseful
	contributionRequestOnly
	contributionIdle
)

var contributionStatuses = [...]string{
	contributionUnknown:     "unknown",
	contributionWarmingUp:   "warming-up",
	contributionUseful:      "contributing",
	contributionRequestOnly: "request-only",
	contributionIdle:        "idle",
}

type ConnectionActivity struct {
	last                    atomic.Int64
	sequence                atomic.Uint64
	lastContributing        atomic.Uint64
	auxiliary               atomic.Int64
	started                 atomic.Bool
	idleDisconnected        atomic.Bool
	requestOnlyDisconnected atomic.Bool
}

func (a *ConnectionActivity) Start(now mclock.AbsTime) {
	a.last.Store(int64(now))
	a.auxiliary.Store(-1)
	a.sequence.Store(0)
	a.lastContributing.Store(0)
	a.started.Store(true)
}

func (a *ConnectionActivity) Observe(now mclock.AbsTime, auxiliary bool) uint64 {
	return a.observe(now, auxiliary, false)
}

func (a *ConnectionActivity) ObserveData(now mclock.AbsTime) uint64 {
	return a.observe(now, false, true)
}

func (a *ConnectionActivity) observe(now mclock.AbsTime, auxiliary, canContribute bool) uint64 {
	sequence := a.sequence.Add(1)
	if canContribute {
		a.lastContributing.Store(sequence)
	}
	last := a.last.Load()
	if last >= 0 && a.last.CompareAndSwap(last, int64(now)) && auxiliary {
		a.auxiliary.Store(int64(now))
	}
	return sequence
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

func (a *ConnectionActivity) MarkRequestOnlyDisconnect() { a.requestOnlyDisconnected.Store(true) }

type ActivityEvent struct {
	ReceivedMessages     uint64         `json:"receivedMessages"`
	ReceivedRequests     uint64         `json:"receivedRequests"`
	ReceivedBytes        uint64         `json:"receivedBytes"`
	ServedResponses      uint64         `json:"servedResponses"`
	ServedBytes          uint64         `json:"servedBytes"`
	AcceptedTransactions uint64         `json:"acceptedTransactions"`
	UnclassifiedMessages uint64         `json:"unclassifiedMessages"`
	StartedAt            mclock.AbsTime `json:"-"`
	MessageSequence      uint64         `json:"-"`
	UsefulBlock          bool           `json:"-"`
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
	IdleGraceSeconds           uint64  `json:"idleGraceSeconds,omitempty"`
	LastContributionAgeSeconds *uint64 `json:"lastContributionAgeSeconds,omitempty"`
	ProtocolIdleSeconds        *uint64 `json:"protocolIdleSeconds,omitempty"`
	ProtocolActivityScope      string  `json:"protocolActivityScope,omitempty"`
	DisconnectReason           string  `json:"disconnectReason,omitempty"`
	auxiliaryActive            bool
	status                     contributionStatusKind
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
	lastHandled     uint64
	hasContribution bool
	connection      *ConnectionActivity
}

func (t *Tracker) SetIdleGrace(grace time.Duration) {
	if grace <= 0 {
		grace = IdleTimeout
	}
	seconds := uint64(grace / time.Second)
	if grace%time.Second != 0 {
		seconds++
	}
	t.mu.Lock()
	t.idleGraceSeconds = seconds
	t.mu.Unlock()
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
	contribution := p.activity.snapshot(now, t.ready, p.exempt, t.idleGraceSeconds)
	return last, live && contribution.status == contributionIdle
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

func (t *Tracker) RequestOnlySince(id string, connection *ConnectionActivity) (mclock.AbsTime, bool) {
	if !t.mu.TryLock() {
		return 0, false
	}
	defer t.mu.Unlock()
	return t.requestOnlySince(id, connection)
}

func (t *Tracker) requestOnlySince(id string, connection *ConnectionActivity) (mclock.AbsTime, bool) {
	p, ok := t.peers.Peek(id)
	if !ok || connection == nil || p.activity.connection != connection {
		return 0, false
	}
	now := t.clock.Now()
	t.prepareActivity(&p.activity, now)
	last, live := connection.Last()
	s := p.activity.snapshot(now, t.ready, p.exempt, t.idleGraceSeconds)
	eligible := s.status == contributionRequestOnly && !s.Protected && s.ObservedSeconds >= s.IdleGraceSeconds &&
		s.ServedMinutes >= uint64(activityGrace/activityWidth) && s.ReceivedRequests > 0 && p.exceededServingLimits(now)
	return last, live && eligible && connection.lastContributing.Load() <= p.activity.lastHandled
}

func (p *peerRecord) exceededServingLimits(now mclock.AbsTime) bool {
	tick := int64(now) / int64(windowWidth)
	const servingReasons = 1<<RequestVolume | 1<<ServingVolume | 1<<ServingRepetition
	for _, b := range p.windows {
		if b.tick <= tick && tick-b.tick < windowCount && b.reasons&servingReasons != 0 {
			return true
		}
	}
	return false
}

func (t *Tracker) RetireRequestOnly(id string, connection *ConnectionActivity, _ mclock.AbsTime) bool {
	if !t.mu.TryLock() {
		return false
	}
	defer t.mu.Unlock()
	current, eligible := t.requestOnlySince(id, connection)
	return eligible && connection.last.CompareAndSwap(int64(current), -1)
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
	if event.ReceivedMessages > 0 {
		a.lastHandled = max(a.lastHandled, event.MessageSequence)
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
	if event.AcceptedTransactions > 0 || event.UsefulBlock {
		a.lastUseful, a.hasContribution = now, true
	}
}

func (a *ActivityEvent) add(event ActivityEvent) {
	a.ReceivedMessages = saturatingAdd(a.ReceivedMessages, event.ReceivedMessages, math.MaxUint64)
	a.ReceivedRequests = saturatingAdd(a.ReceivedRequests, event.ReceivedRequests, math.MaxUint64)
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
	return &ContributionSnapshot{Mode: "observe", Scope: "eth", UsefulDataCoverage: "transaction-admission,block-import,sync-body-validation", Status: contributionStatuses[contributionUnknown],
		WindowSeconds: uint64(activityCount * activityWidth / time.Second), GraceSeconds: uint64(activityGrace / time.Second)}
}

func (a *activityRecord) snapshot(now mclock.AbsTime, ready, protected bool, idleGraceSeconds uint64) *ContributionSnapshot {
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
	ready = a.connectionSnapshot(now, ready, result, idleGraceSeconds)
	result.status = result.classify(a.registered && ready)
	result.Status = contributionStatuses[result.status]
	result.score()
	return result
}

func (a *activityRecord) connectionSnapshot(now mclock.AbsTime, ready bool, result *ContributionSnapshot, idleGraceSeconds uint64) bool {
	if a.connection == nil {
		return ready
	}
	result.ProtocolActivityScope = "all-subprotocols"
	result.IdleGraceSeconds = idleGraceSeconds
	if a.connection.idleDisconnected.Load() {
		result.DisconnectReason = "idle"
	}
	if a.connection.requestOnlyDisconnected.Load() {
		result.DisconnectReason = "request-only"
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

func (s *ContributionSnapshot) classify(ready bool) contributionStatusKind {
	switch {
	case !ready:
		return contributionUnknown
	case s.ProtocolIdleSeconds != nil && s.ObservedSeconds >= s.IdleGraceSeconds && *s.ProtocolIdleSeconds >= s.IdleGraceSeconds && !s.recentlyContributed():
		return contributionIdle
	case s.ObservedSeconds < s.GraceSeconds:
		return contributionWarmingUp
	case s.recentlyContributed():
		return contributionUseful
	case s.UnclassifiedMessages > 0 || s.auxiliaryActive:
		return contributionUnknown
	case s.ServedMinutes >= requestOnlyMinServedMinutes:
		return contributionRequestOnly
	case s.ActivityEvent == (ActivityEvent{}) && s.ProtocolIdleSeconds == nil:
		return contributionIdle
	default:
		return contributionUnknown
	}
}

func (s *ContributionSnapshot) recentlyContributed() bool {
	return s.LastContributionAgeSeconds != nil && *s.LastContributionAgeSeconds < s.WindowSeconds
}

func (s *ContributionSnapshot) score() {
	if !s.Protected && (s.status == contributionIdle || s.status == contributionRequestOnly) {
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
