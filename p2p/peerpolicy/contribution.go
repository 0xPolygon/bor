// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"math"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
)

// These are provisional observation profiles, not serving or eviction limits.
const (
	activityWidth = time.Minute
	activityCount = 30
	activityGrace = 10 * time.Minute
)

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
	Protected                  bool    `json:"protected"`
	ConnectionKnown            bool    `json:"connectionKnown"`
	ServedMinutes              uint64  `json:"servedMinutes"`
	ConnectionSeconds          uint64  `json:"connectionSeconds"`
	ObservedSeconds            uint64  `json:"observedSeconds"`
	WindowSeconds              uint64  `json:"windowSeconds"`
	GraceSeconds               uint64  `json:"graceSeconds"`
	LastContributionAgeSeconds *uint64 `json:"lastContributionAgeSeconds,omitempty"`
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
}

// Connected begins a fresh connection observation without erasing misconduct.
func (t *Tracker) Connected(id string) mclock.AbsTime {
	if id == "" || len(id) > 128 {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock.Now()
	t.record(id).activity = activityRecord{registered: true, connectedAt: now, observedAt: now,
		readyStarted: t.readyStarted, resetAt: t.resetAt, pausedFor: t.pauseTotal(now), acceptAfter: now}
	return now
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
	result.classify(a.registered && ready)
	return result
}

func (s *ContributionSnapshot) classify(ready bool) {
	switch {
	case !ready:
		s.Status = "unknown"
	case s.ObservedSeconds < s.GraceSeconds:
		s.Status = "warming-up"
	case s.AcceptedTransactions > 0:
		s.Status = "contributing"
	case s.UnclassifiedMessages > 0:
		s.Status = "unknown"
	case s.ServedMinutes >= 3:
		s.Status = "request-only"
	case s.ActivityEvent == (ActivityEvent{}):
		s.Status = "idle"
	default:
		s.Status = "unknown"
	}
}
