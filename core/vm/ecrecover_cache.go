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

// ecrecoverCacheCap is smaller than keccak256CacheCap since ecrecover's
// 3000-gas cost limits per-block call volume far more.
const ecrecoverCacheCap = 16384

// EcrecoverCache memoizes the ECRECOVER precompile fast path.
type EcrecoverCache = BoundedCache[[128]byte, []byte]

// NewEcrecoverCache returns an empty cache.
func NewEcrecoverCache() *EcrecoverCache {
	return newBoundedCache[[128]byte, []byte](ecrecoverCacheCap)
}
