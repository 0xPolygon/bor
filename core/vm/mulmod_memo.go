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

import "github.com/holiman/uint256"

// modReciprocal memoizes uint256.Reciprocal for the last full-width (4-word)
// MULMOD modulus seen by an EVM instance.
//
// uint256.MulMod, for a full-width modulus, computes Reciprocal(m) internally
// and then reduces with it; the reciprocal is ~60% of that call's cost and is
// thrown away every time. A common on-chain pattern is a long run of modular
// multiplications against a single fixed-prime modulus (field arithmetic used
// broadly by cryptographic and verification contracts), so a one-entry memo of
// (modulus, reciprocal) hits almost always and elides the recompute.
//
// No synchronization: the memo is per-EVM scratch state, like depth and
// returnData. The EVM "should never be reused and is not thread safe" (see the
// EVM type doc); every construction site builds a fresh instance and each
// executes on a single goroutine, so an unsynchronized field is safe here.
type modReciprocal struct {
	mod uint256.Int
	mu  [5]uint64
	ok  bool
}

// mulmod computes z = (x * y) % z in place, mirroring the exact semantics of
// z.MulMod(x, y, z) — including m == 0 yielding 0 — but reuses the memoized
// reciprocal when the modulus repeats.
//
// Correctness is a library guarantee, not a re-derivation: MulModWithReciprocal
// with mu == Reciprocal(m) produces the identical result to MulMod, because
// MulMod computes exactly that reciprocal and reduction internally. The memo
// only changes how the reciprocal is obtained (cached vs recomputed), never the
// arithmetic — the output is a pure function of (x, y, m), independent of cache
// state. On a miss we recompute Reciprocal exactly as stock would.
//
// Only full-width (z[3] != 0) moduli take the memo path; narrower moduli — and
// m == 0 — never reach the reciprocal reduction inside MulMod, so they fall
// through to the stock call unchanged.
func (mm *modReciprocal) mulmod(x, y, z *uint256.Int) {
	if z[3] != 0 {
		if !mm.ok || mm.mod != *z {
			mm.mod = *z
			mm.mu = uint256.Reciprocal(z)
			mm.ok = true
		}
		z.MulModWithReciprocal(x, y, z, &mm.mu)
		return
	}
	z.MulMod(x, y, z)
}
