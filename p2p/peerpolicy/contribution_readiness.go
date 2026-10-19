// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import "github.com/ethereum/go-ethereum/common/mclock"

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
	a.acceptAfter = max(a.connectedAt, t.readyAt)
}
