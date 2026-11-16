// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Package p2p implements the Ethereum p2p network protocols.
package p2p

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

const (
	maxPeerJailEntries = 4096
	noPeerJailExpiry   = mclock.AbsTime(1<<63 - 1)
)

type peerJailEntry struct {
	penaltyUntil mclock.AbsTime
	idleUntil    mclock.AbsTime
}

func (e peerJailEntry) expiry() mclock.AbsTime {
	return max(e.penaltyUntil, e.idleUntil)
}

type peerJail struct {
	mu         sync.Mutex
	jailed     map[enode.ID]peerJailEntry
	jailPeriod time.Duration
	clock      mclock.Clock
}

func newPeerJail(jailPeriod time.Duration, clock mclock.Clock) *peerJail {
	return &peerJail{
		jailed:     make(map[enode.ID]peerJailEntry),
		jailPeriod: jailPeriod,
		clock:      clock,
	}
}

func (pj *peerJail) JailPeer(id enode.ID) {
	pj.JailPeerFor(id, pj.jailPeriod)
}

func (pj *peerJail) JailPeerFor(id enode.ID, period time.Duration) {
	pj.jailPeerFor(id, period, false)
}

func (pj *peerJail) cooldownPeer(id enode.ID, period time.Duration) {
	pj.jailPeerFor(id, period, true)
}

func (pj *peerJail) jailPeerFor(id enode.ID, period time.Duration, idle bool) {
	now := pj.clock.Now()
	until := now.Add(period)
	pj.mu.Lock()
	defer pj.mu.Unlock()
	entry, exists := pj.jailed[id]
	if !exists && len(pj.jailed) >= maxPeerJailEntries {
		pj.makeRoom(now)
	}
	if idle {
		entry.idleUntil = max(entry.idleUntil, until)
	} else {
		entry.penaltyUntil = max(entry.penaltyUntil, until)
	}
	pj.jailed[id] = entry
}

func (pj *peerJail) clearIdleCooldown(id enode.ID) {
	pj.mu.Lock()
	defer pj.mu.Unlock()
	entry, exists := pj.jailed[id]
	if !exists {
		return
	}
	entry.idleUntil = 0
	if entry.penaltyUntil == 0 || pj.clock.Now() > entry.penaltyUntil {
		delete(pj.jailed, id)
	} else {
		pj.jailed[id] = entry
	}
}

func (pj *peerJail) makeRoom(now mclock.AbsTime) {
	var earliestID enode.ID
	earliestExpiry := noPeerJailExpiry
	for id, entry := range pj.jailed {
		until := entry.expiry()
		if now > until {
			delete(pj.jailed, id)
			continue
		}
		if until <= earliestExpiry {
			earliestID, earliestExpiry = id, until
		}
	}
	if len(pj.jailed) >= maxPeerJailEntries {
		delete(pj.jailed, earliestID)
	}
}

func (pj *peerJail) IsJailed(id enode.ID) bool {
	_, jailed := pj.JailedUntil(id)
	return jailed
}

func (pj *peerJail) JailedUntil(id enode.ID) (mclock.AbsTime, bool) {
	pj.mu.Lock()
	defer pj.mu.Unlock()
	entry, exists := pj.jailed[id]
	if !exists || pj.clock.Now() > entry.expiry() {
		delete(pj.jailed, id)
		return 0, false
	}
	return entry.expiry(), true
}
