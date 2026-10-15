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

package vm

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// keccak256CacheLen counts retained entries directly, independent of the
// cache's own counter.
func keccak256CacheLen(c *Keccak256Cache) int {
	n := 0
	c.m.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

func TestKeccak256CacheConcurrentStoreStaysBounded(t *testing.T) {
	const (
		goroutines       = 32
		keysPerGoroutine = 4096 // 32 * 4096 = 131072, 2x keccak256CacheCap
	)

	cache := NewKeccak256Cache()
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			var key [64]byte
			for i := 0; i < keysPerGoroutine; i++ {
				// Every (g, i) pair is a distinct 64-byte key.
				key[0] = byte(g)
				key[1] = byte(g >> 8)
				key[2] = byte(i)
				key[3] = byte(i >> 8)
				key[4] = byte(i >> 16)
				cache.Store(key, common.Hash{31: byte(i)})
			}
		}(g)
	}
	wg.Wait()

	got := keccak256CacheLen(cache)
	if got == 0 {
		t.Fatal("cache retained zero entries; admission logic appears broken")
	}
	// Concurrent Stores can overshoot the cap by at most one per goroutine.
	maxAcceptable := keccak256CacheCap + goroutines
	if got > maxAcceptable {
		t.Fatalf("cache retained %d entries after storing %d distinct keys; want at most %d (cap=%d + bounded race overshoot)",
			got, goroutines*keysPerGoroutine, maxAcceptable, keccak256CacheCap)
	}
	if got < keccak256CacheCap {
		t.Fatalf("cache retained only %d entries; want at least %d (cap wasn't reached — admission logic under-admits)",
			got, keccak256CacheCap)
	}
	t.Logf("stored %d distinct keys concurrently across %d goroutines; cache retained %d (cap=%d)",
		goroutines*keysPerGoroutine, goroutines, got, keccak256CacheCap)
}

func TestKeccak256CacheCapIsExact(t *testing.T) {
	cache := NewKeccak256Cache()
	var key [64]byte
	for i := 0; i < keccak256CacheCap+1024; i++ {
		key[0] = byte(i)
		key[1] = byte(i >> 8)
		key[2] = byte(i >> 16)
		cache.Store(key, common.Hash{31: byte(i)})
	}
	if got := keccak256CacheLen(cache); got != keccak256CacheCap {
		t.Fatalf("cache retained %d entries after storing %d distinct keys sequentially, want exactly %d",
			got, keccak256CacheCap+1024, keccak256CacheCap)
	}
}

func TestKeccak256CacheLoadReturnsStoredValue(t *testing.T) {
	cache := NewKeccak256Cache()
	var key [64]byte
	key[0] = 0xaa

	if _, ok := cache.Load(key); ok {
		t.Fatal("Load on empty cache reported a hit")
	}

	want := common.HexToHash("0xdeadbeef")
	cache.Store(key, want)
	got, ok := cache.Load(key)
	if !ok {
		t.Fatal("Load after Store reported a miss")
	}
	if got != want {
		t.Fatalf("Load returned %x, want %x", got, want)
	}
}
