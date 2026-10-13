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

// Package readattr connects state reads that bottom out in the database to
// the transaction whose execution caused them.
//
// Attribution is goroutine-scoped: every state disk read happens
// synchronously on the goroutine that demanded it, so a tag set around a
// transaction's execution attributes exactly the reads that transaction
// triggered — trie-node reads included, regardless of how many accounts a
// node would serve elsewhere. Concurrent goroutines (the pipelined SRC,
// prefetchers, sync) carry their own tags and cannot pollute a
// transaction's count.
//
// The tag store is keyed by goroutine id, parsed from the runtime.Stack
// header. That is only touched on the rare paths — a Set/Reset per
// transaction and one lookup per actual disk read — so the overhead is
// negligible, and zero when the feature is off (default; enable with
// BOR_DISK_READ_TRACE=1 at node start).
package readattr

import (
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

// ringCap bounds the in-memory log of recorded reads.
const ringCap = 4096

var enabled atomic.Bool

func init() {
	if os.Getenv("BOR_DISK_READ_TRACE") != "" {
		enabled.Store(true)
	}
}

// Enabled reports whether disk-read attribution is turned on.
func Enabled() bool { return enabled.Load() }

// SetEnabled flips attribution on/off. Intended for startup and tests.
func SetEnabled(v bool) { enabled.Store(v) }

// currentGoid returns the current goroutine's id, parsed from the
// runtime.Stack header ("goroutine <id> [running]:").
func currentGoid() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	b := buf[:n]
	const prefix = "goroutine "
	if len(b) <= len(prefix) || string(b[:len(prefix)]) != prefix {
		return 0
	}
	rest := b[len(prefix):]
	end := len(rest)
	for i, c := range rest {
		if c == ' ' || c == '[' {
			end = i
			break
		}
	}
	id, _ := strconv.ParseUint(string(rest[:end]), 10, 64)
	return id
}

var tags sync.Map // goid -> string

// Set tags every state disk read performed by the current goroutine with the
// given attribution tag — typically "tx:<block>:<txhash>" around a
// transaction's execution, or a phase name like "src:<block>" outside any
// transaction.
//
// The caller MUST Reset before the goroutine exits (defer) so a recycled
// goroutine id never inherits a stale tag.
func Set(tag string) {
	if !enabled.Load() {
		return
	}
	tags.Store(currentGoid(), tag)
}

// Reset clears the tag from the current goroutine.
func Reset() {
	if !enabled.Load() {
		return
	}
	tags.Delete(currentGoid())
}

// Current returns the current goroutine's tag, or "" when untagged or
// disabled.
func Current() string {
	if !enabled.Load() {
		return ""
	}
	if v, ok := tags.Load(currentGoid()); ok {
		return v.(string)
	}
	return ""
}

// Entry is one recorded disk read, as seen at the pathdb disk-layer
// chokepoint.
type Entry struct {
	Time time.Time
	Tag  string
	Kind string // "node", "account" or "storage"
	Key  string
	Size int
	Dur  time.Duration
}

var (
	mu   sync.Mutex
	ring []Entry

	counts sync.Map // tag -> *atomic.Int64
)

// Record books one disk read under the current goroutine's tag. kind is
// "node" (MPT trie node; key is owner hex + "/" + path hex), "account"
// (flat-state account; key is keccak(address)) or "storage" (flat-state slot;
// key is keccak(address) + "/" + keccak(slot)). Keys are hashes, not
// addresses — mapping back to plaintext requires the usual reverse
// lookups.
func Record(kind, key string, size int, dur time.Duration) {
	if !enabled.Load() {
		return
	}
	tag := Current()
	c, _ := counts.LoadOrStore(tag, new(atomic.Int64))
	c.(*atomic.Int64).Add(1)

	mu.Lock()
	if len(ring) >= ringCap {
		ring = ring[len(ring)-ringCap/2:]
	}
	ring = append(ring, Entry{Time: time.Now(), Tag: tag, Kind: kind, Key: key, Size: size, Dur: dur})
	mu.Unlock()

	log.Debug("State read went to disk", "tag", tag, "kind", kind, "key", key, "size", size, "dur", time.Duration(dur).Round(time.Microsecond))
}

// Take returns and deletes the number of disk reads recorded under tag, so
// callers can attribute a window (typically one transaction) without the
// counter table growing forever.
func Take(tag string) int64 {
	if c, ok := counts.LoadAndDelete(tag); ok {
		return c.(*atomic.Int64).Load()
	}
	return 0
}

// Recent returns the last n recorded entries, oldest first.
func Recent(n int) []Entry {
	mu.Lock()
	defer mu.Unlock()
	if n > len(ring) {
		n = len(ring)
	}
	out := make([]Entry, n)
	copy(out, ring[len(ring)-n:])
	return out
}
