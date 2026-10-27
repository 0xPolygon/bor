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

package pqsig

import "testing"

func FuzzMLDSA65(f *testing.F) {
	f.Add([]byte{})                             // too short for a key seed
	f.Add(append(make([]byte, 32), 0x3f, 0x00)) // message longer than the data
	f.Add(make([]byte, 40))
	f.Add(append(make([]byte, 33), 0x0c, 0xae, 0x01)) // hint area
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzMLDSA65(data)
	})
}

func FuzzSLHDSA(f *testing.F) {
	f.Add([]byte{}) // no seed index
	for i := byte(0); i < 4; i++ {
		f.Add([]byte{i})
		f.Add([]byte{i, 0x00, 0x10, 0x01}) // R
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzSLHDSA(data)
	})
}
