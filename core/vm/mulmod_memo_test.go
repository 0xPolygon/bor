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

// TestMulmodMemoDifferential checks the memoized mulmod against stock
// uint256.MulMod across random operands, alternating moduli (memo misses),
// repeated moduli (memo hits), sub-4-word moduli, and edge values.
func TestMulmodMemoDifferential(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(1))
	edges := []*uint256.Int{
		uint256.NewInt(0),
		uint256.NewInt(1),
		uint256.NewInt(2),
		uint256.MustFromHex("0x30644e72e131a029b85045b68181585d97816a916871ca8d3c208c16d87cfd47"), // bn254 P
		uint256.MustFromHex("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
		uint256.MustFromHex("0x10000000000000000"),
		uint256.MustFromHex("0x8000000000000000000000000000000000000000000000000000000000000000"),
	}

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
			m = *edges[rng.Intn(len(edges))]
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

	p := uint256.MustFromHex("0x30644e72e131a029b85045b68181585d97816a916871ca8d3c208c16d87cfd47")
	rng := rand.New(rand.NewSource(2))

	var mm modReciprocal
	var want, got uint256.Int
	for i := 0; i < 100000; i++ {
		var x, y uint256.Int
		for w := 0; w < 4; w++ {
			x[w] = rng.Uint64()
			y[w] = rng.Uint64()
		}
		x.Mod(&x, p)
		y.Mod(&y, p)
		want = *p
		want.MulMod(&x, &y, &want)
		got = *p
		mm.mulmod(&x, &y, &got)
		if want != got {
			t.Fatalf("mismatch at %d: x=%s y=%s stock=%s memo=%s", i, x.Hex(), y.Hex(), want.Hex(), got.Hex())
		}
	}
}
