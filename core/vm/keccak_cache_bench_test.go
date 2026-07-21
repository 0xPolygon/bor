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

// Throwaway analysis, not a production feature. Answers a question raised
// by the copy-node instrumentation (instrument.go): KECCAK256_other (calls
// with input != 64 bytes) showed an 84.7% would-hit rate — but is caching
// worth it at those sizes, or does deriving/storing a cache key for a large
// input cost about as much as just computing the real hash?
//
// A cache HIT pays: key-derivation + map lookup, and SKIPS the real hash.
// A cache MISS pays: key-derivation + map lookup (fails) + the real hash +
// a map store. So caching only wins if, averaged over the observed hit
// rate, (hit savings) > (miss overhead) — and both terms scale with input
// size, not just the hit rate.
//
// Two keying schemes are compared:
//  1. Raw-bytes-as-key (what the existing 64-byte Keccak256Cache already
//     does, generalized from a fixed [64]byte array to a variable-length
//     string). Go's compiler special-cases `m[string(byteSlice)]` to avoid
//     allocating when the converted string is only used as a lookup key,
//     so BenchmarkCacheHit below measures that real, already-optimized
//     path — not a naive string conversion.
//  2. sha256-of-input-as-key (what instrument.go's ObserveKey and geth PR
//     #35388's precompileCacheKey both use, needed if you want a fixed-size
//     key type instead of holding variable-length data as a map key).
//
// Run on the same hardware class as production (n2d-standard-16) for
// numbers that mean anything — a laptop's crypto-extension availability
// differs from GCP's.

import (
	"crypto/sha256"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// keccakBenchSizes spans the observed KECCAK256_other range up to
// maxObservedCallInput (8192) — the bound this instrumentation (and geth PR
// #35388) already use to exclude one-off huge inputs from consideration.
var keccakBenchSizes = []int{65, 128, 256, 512, 1024, 2048, 4096, 8192}

func randomInput(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(int64(n))).Read(b) //nolint:gosec // benchmark fixture, not security sensitive
	return b
}

// BenchmarkKeccak256Real times the actual opcode work: what every call pays
// today (no cache), and what a miss still has to pay under any cache design.
func BenchmarkKeccak256Real(b *testing.B) {
	for _, size := range keccakBenchSizes {
		data := randomInput(size)
		hasher := crypto.NewKeccakState()
		b.Run(sizeLabel(size), func(b *testing.B) {
			b.SetBytes(int64(size))
			var out [32]byte
			for i := 0; i < b.N; i++ {
				hasher.Reset()
				hasher.Write(data)
				hasher.Read(out[:])
			}
		})
	}
}

// BenchmarkCacheHitRawKey times a HIT under the raw-bytes-as-key scheme:
// `cache[string(data)]`, which the Go compiler recognizes and does NOT
// allocate for — this is the realistic cost of extending Keccak256Cache's
// existing design (fixed [64]byte key) to a variable-length string key.
func BenchmarkCacheHitRawKey(b *testing.B) {
	for _, size := range keccakBenchSizes {
		data := randomInput(size)
		cache := make(map[string][32]byte, 1)
		cache[string(data)] = [32]byte{}
		b.Run(sizeLabel(size), func(b *testing.B) {
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				_, _ = cache[string(data)]
			}
		})
	}
}

// BenchmarkCacheMissRawKey times a MISS under the same scheme: the failed
// lookup, then the real hash, then the store — everything a miss actually
// costs on top of (or instead of) the uncached baseline.
func BenchmarkCacheMissRawKey(b *testing.B) {
	for _, size := range keccakBenchSizes {
		data := randomInput(size)
		hasher := crypto.NewKeccakState()
		b.Run(sizeLabel(size), func(b *testing.B) {
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				cache := make(map[string][32]byte, 1) // fresh map: force a real miss every iteration
				if _, ok := cache[string(data)]; !ok {
					hasher.Reset()
					hasher.Write(data)
					var out [32]byte
					hasher.Read(out[:])
					cache[string(data)] = out // allocates+copies the key here — the real store cost
				}
			}
		})
	}
}

// BenchmarkCacheKeySHA256 times deriving a fixed-size cache key via sha256
// of the raw input — the scheme instrument.go's ObserveKey and geth PR
// #35388's precompileCacheKey both use. Comparing this against
// BenchmarkKeccak256Real directly answers "is computing a cache key about
// as expensive as just computing the real hash" for large inputs.
func BenchmarkCacheKeySHA256(b *testing.B) {
	for _, size := range keccakBenchSizes {
		data := randomInput(size)
		b.Run(sizeLabel(size), func(b *testing.B) {
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				_ = sha256.Sum256(data)
			}
		})
	}
}

func sizeLabel(n int) string {
	if n < 1024 {
		return itoa(n) + "B"
	}
	return itoa(n/1024) + "KB"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
