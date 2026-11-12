// Copyright 2026 The go-ethereum Authors
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

package legacypool

import "time"

func (pool *LegacyPool) setRebroadcastTick(tick time.Time) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.rebroadcastTick = tick
}

// Call with pool.mu held. Queue reports use the ticker's cadence so work after
// selection cannot postpone retries by a whole interval. Advance past missed
// ticks as well, since a slow subscriber can block the pool's publishing loop.
func (pool *LegacyPool) rebroadcastTime() time.Time {
	now := time.Now()
	if pool.rebroadcastTick.IsZero() {
		return now
	}
	return now.Add(-now.Sub(pool.rebroadcastTick) % pool.config.RebroadcastInterval)
}
