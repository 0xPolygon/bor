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

import "github.com/ethereum/go-ethereum/common"

// keccak256CacheCap is ~12-13 MiB of entries, well above the distinct
// mapping-slot lookups of a full block.
const keccak256CacheCap = 65536

// Keccak256Cache memoizes the opKeccak256 64-byte fast path.
type Keccak256Cache = BoundedCache[[64]byte, common.Hash]

// NewKeccak256Cache returns an empty cache.
func NewKeccak256Cache() *Keccak256Cache {
	return newBoundedCache[[64]byte, common.Hash](keccak256CacheCap)
}
