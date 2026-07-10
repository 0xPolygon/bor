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
// MULMOD modulus seen by an EVM instance. uint256.MulMod recomputes the
// reciprocal on every call when the modulus occupies all four words; real
// workloads run long modular-arithmetic sequences against a single modulus,
// so a one-entry memo hits almost always. The EVM executes on one goroutine,
// so no synchronization is needed.
type modReciprocal struct {
	mod uint256.Int
	mu  [5]uint64
	ok  bool
}

// mulmod computes z = (x * y) % z in place, mirroring the semantics of
// z.MulMod(x, y, z) — including m == 0 yielding 0 — but reuses the memoized
// reciprocal when the modulus repeats. Only full-width moduli take the memo
// path; narrower moduli never reach the reciprocal reduction in MulMod.
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
