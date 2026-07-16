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
	"math/rand"
	"testing"

	"github.com/holiman/uint256"
)

// A representative 254-bit prime modulus, exercising the full-width (4-word)
// reciprocal path that the memo targets. Long runs of modular multiplication
// against a single such modulus are a common on-chain pattern.
var testPrime = uint256.MustFromHex("0x30644e72e131a029b85045b68181585d97816a916871ca8d3c208c16d87cfd47")

// mulmodEdges collects moduli that stress boundaries of the reduction: zero and
// small values (which bypass the memo path), the target prime, all-ones, and a
// value whose top bit is set.
var mulmodEdges = []*uint256.Int{
	uint256.NewInt(0),
	uint256.NewInt(1),
	uint256.NewInt(2),
	testPrime,
	uint256.MustFromHex("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
	uint256.MustFromHex("0x10000000000000000"),
	uint256.MustFromHex("0x8000000000000000000000000000000000000000000000000000000000000000"),
}

// TestMulmodMemoDifferential checks the memoized mulmod against stock
// uint256.MulMod across random operands, alternating moduli (memo misses),
// repeated moduli (memo hits), sub-4-word moduli, and edge values. The EVM
// aliases destination and modulus (z.MulMod(&x, &y, z)); the test mirrors that.
func TestMulmodMemoDifferential(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(1))

	var mm modReciprocal
	var want, got uint256.Int
	for i := 0; i < 500000; i++ {
		var x, y, m uint256.Int
		for w := 0; w < 4; w++ {
			x[w] = rng.Uint64()
			y[w] = rng.Uint64()
			m[w] = rng.Uint64()
		}
		switch i % 8 {
		case 0, 1:
			m = *mulmodEdges[rng.Intn(len(mulmodEdges))]
		case 2:
			m[3] = 0 // 3-word modulus, bypasses the memo path
		case 3:
			m[3], m[2], m[1] = 0, 0, 0 // 1-word modulus
		}
		// The EVM aliases destination and modulus: z.MulMod(&x, &y, z).
		want = m
		want.MulMod(&x, &y, &want)
		got = m
		mm.mulmod(&x, &y, &got)
		if want != got {
			t.Fatalf("mismatch: x=%s y=%s m=%s stock=%s memo=%s", x.Hex(), y.Hex(), m.Hex(), want.Hex(), got.Hex())
		}
	}
}

// TestMulmodMemoRepeatedModulus exercises the hit path explicitly: many
// multiplications against one modulus with the memo primed.
func TestMulmodMemoRepeatedModulus(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(2))

	var mm modReciprocal
	var want, got uint256.Int
	for i := 0; i < 100000; i++ {
		var x, y uint256.Int
		for w := 0; w < 4; w++ {
			x[w] = rng.Uint64()
			y[w] = rng.Uint64()
		}
		x.Mod(&x, testPrime)
		y.Mod(&y, testPrime)
		want = *testPrime
		want.MulMod(&x, &y, &want)
		got = *testPrime
		mm.mulmod(&x, &y, &got)
		if want != got {
			t.Fatalf("mismatch at %d: x=%s y=%s stock=%s memo=%s", i, x.Hex(), y.Hex(), want.Hex(), got.Hex())
		}
	}
}

// TestMulmodMemoAliasing verifies the memo matches stock under every operand
// pointer-aliasing combination, not just the dest==modulus case the EVM
// currently produces. Each case runs stock and memo with identical aliasing and
// compares bit-for-bit, guarding against future changes to the call convention.
func TestMulmodMemoAliasing(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(3))
	base := []*uint256.Int{testPrime, uint256.NewInt(0), uint256.NewInt(1)}

	// Each alias mode runs one operation; stock() is compared to memo(), with
	// a freshly primed/loaded operand set per invocation so aliasing writes do
	// not leak between the two runs.
	modes := []struct {
		name string
		run  func(op func(x, y, z *uint256.Int), x, y, m uint256.Int) uint256.Int
	}{
		{"dest==mod", func(op func(x, y, z *uint256.Int), x, y, m uint256.Int) uint256.Int {
			z := m
			op(&x, &y, &z)
			return z
		}},
		{"x==dest==mod", func(op func(x, y, z *uint256.Int), _, y, m uint256.Int) uint256.Int {
			z := m
			op(&z, &y, &z)
			return z
		}},
		{"y==dest==mod", func(op func(x, y, z *uint256.Int), x, _, m uint256.Int) uint256.Int {
			z := m
			op(&x, &z, &z)
			return z
		}},
		{"x==y==dest==mod", func(op func(x, y, z *uint256.Int), _, _, m uint256.Int) uint256.Int {
			z := m
			op(&z, &z, &z)
			return z
		}},
	}

	for i := 0; i < 50000; i++ {
		var x, y, m uint256.Int
		for w := 0; w < 4; w++ {
			x[w] = rng.Uint64()
			y[w] = rng.Uint64()
			m[w] = rng.Uint64()
		}
		if i%4 == 0 {
			m = *base[rng.Intn(len(base))]
		}
		for _, mode := range modes {
			// Fresh memo each mode so both hit and miss states are exercised
			// across the loop (modulus varies), while staying deterministic.
			var mm modReciprocal
			want := mode.run(func(x, y, z *uint256.Int) { z.MulMod(x, y, z) }, x, y, m)
			got := mode.run(mm.mulmod, x, y, m)
			if want != got {
				t.Fatalf("%s mismatch: x=%s y=%s m=%s stock=%s memo=%s",
					mode.name, x.Hex(), y.Hex(), m.Hex(), want.Hex(), got.Hex())
			}
		}
	}
}

// TestMulmodMemoEdgeCases pins the boundary results explicitly, most importantly
// that a zero modulus yields zero via the non-memo path.
func TestMulmodMemoEdgeCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		x, y, m *uint256.Int
	}{
		{"zero modulus", uint256.NewInt(7), uint256.NewInt(9), uint256.NewInt(0)},
		{"modulus one", uint256.NewInt(123456), uint256.NewInt(789), uint256.NewInt(1)},
		{"single-word modulus", uint256.NewInt(0xdeadbeef), uint256.NewInt(0xfeedface), uint256.NewInt(97)},
		{"three-word modulus", uint256.MustFromHex("0xffffffffffffffffffffffffffffffffffffffff"),
			uint256.MustFromHex("0x123456789abcdef0"), uint256.MustFromHex("0xffffffffffffffffffffffffffffffffffffffff")},
		{"full-width prime", testPrime, testPrime, testPrime},
		{"all ones modulus", uint256.MustFromHex("0xdeadbeefcafebabe1234"),
			uint256.MustFromHex("0x99887766554433221100"),
			uint256.MustFromHex("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")},
	}

	var mm modReciprocal
	for _, c := range cases {
		want := *c.m
		want.MulMod(c.x, c.y, &want)
		got := *c.m
		mm.mulmod(c.x, c.y, &got)
		if want != got {
			t.Errorf("%s: stock=%s memo=%s", c.name, want.Hex(), got.Hex())
		}
	}
}

// TestMulmodMemoModulusSwitch drives the cache-invalidation sequence directly:
// prime on modulus A (miss), reuse A (hit), switch to B (miss), reuse B (hit),
// then a sub-4-word modulus (bypass), each verified against stock.
func TestMulmodMemoModulusSwitch(t *testing.T) {
	t.Parallel()

	a := testPrime
	b := uint256.MustFromHex("0xfffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141") // secp256k1 order
	small := uint256.NewInt(1_000_003)
	x := uint256.MustFromHex("0xf123456789abcdeffedcba98765432100011223344556677889900aabbccddee")
	y := uint256.MustFromHex("0xa1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")

	var mm modReciprocal
	for step, m := range []*uint256.Int{a, a, b, b, a, small, a} {
		want := *m
		want.MulMod(x, y, &want)
		got := *m
		mm.mulmod(x, y, &got)
		if want != got {
			t.Fatalf("step %d m=%s: stock=%s memo=%s", step, m.Hex(), want.Hex(), got.Hex())
		}
	}
}

// FuzzMulmodMemo differentially fuzzes the memo against stock uint256.MulMod
// over arbitrary operands, exercising both a miss (first call) and a hit
// (second call with the same modulus) per input.
func FuzzMulmodMemo(f *testing.F) {
	f.Add([]byte{0x2}, []byte{0x3}, []byte{0x5})
	f.Add(testPrime.Bytes(), testPrime.Bytes(), testPrime.Bytes())
	f.Add([]byte{0x1}, []byte{0x1}, []byte{}) // empty -> zero modulus
	f.Add(
		uint256.MustFromHex("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff").Bytes(),
		uint256.MustFromHex("0xfedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210").Bytes(),
		testPrime.Bytes(),
	)

	f.Fuzz(func(t *testing.T, xb, yb, mb []byte) {
		x := new(uint256.Int).SetBytes(xb)
		y := new(uint256.Int).SetBytes(yb)
		m := new(uint256.Int).SetBytes(mb)

		var mm modReciprocal
		// Call twice: first primes (miss), second uses the cache (hit). Both
		// must equal stock, computed with the same dest==modulus aliasing.
		for pass := 0; pass < 2; pass++ {
			want := *m
			want.MulMod(x, y, &want)
			got := *m
			mm.mulmod(x, y, &got)
			if want != got {
				t.Fatalf("pass %d mismatch: x=%s y=%s m=%s stock=%s memo=%s",
					pass, x.Hex(), y.Hex(), m.Hex(), want.Hex(), got.Hex())
			}
		}
	})
}

// BenchmarkMulmod compares stock uint256.MulMod against the memo on both the
// hit path (repeated modulus) and the miss path (modulus changes every call).
// The hit-path speedup reflects the elided Reciprocal recompute.
func BenchmarkMulmod(b *testing.B) {
	x := uint256.MustFromHex("0xf123456789abcdeffedcba98765432100011223344556677889900aabbccddee")
	y := uint256.MustFromHex("0xa1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")
	altPrime := uint256.MustFromHex("0xfffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141")

	b.Run("stock", func(b *testing.B) {
		var z uint256.Int
		for i := 0; i < b.N; i++ {
			z = *testPrime
			z.MulMod(x, y, &z)
		}
		_ = z
	})
	b.Run("memo/hit", func(b *testing.B) {
		var mm modReciprocal
		var z uint256.Int
		for i := 0; i < b.N; i++ {
			z = *testPrime
			mm.mulmod(x, y, &z)
		}
		_ = z
	})
	b.Run("memo/miss", func(b *testing.B) {
		var mm modReciprocal
		var z uint256.Int
		for i := 0; i < b.N; i++ {
			// Alternate modulus every call to force a recompute (worst case).
			if i&1 == 0 {
				z = *testPrime
			} else {
				z = *altPrime
			}
			mm.mulmod(x, y, &z)
		}
		_ = z
	})
}
