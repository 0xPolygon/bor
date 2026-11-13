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
	"bytes"
	"crypto/mldsa"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// pqValidSeeds returns the valid inputs of a vector file, for seeding.
func pqValidSeeds(f *testing.F, file string) [][]byte {
	tests, err := loadJson(file)
	if err != nil {
		f.Fatal(err)
	}
	var seeds [][]byte
	for _, test := range tests {
		if test.Expected == common.Bytes2Hex(true32Byte) && len(test.Input) < 40_000 {
			seeds = append(seeds, common.Hex2Bytes(test.Input))
		}
	}
	return seeds
}

// pqMutate applies up to eight byte edits encoded in edits (3 bytes each:
// big-endian position, xor value) to a copy of input.
func pqMutate(input, edits []byte) []byte {
	out := bytes.Clone(input)
	for i := 0; i+2 < len(edits) && i < 24; i += 3 {
		pos := (int(edits[i])<<8 | int(edits[i+1])) % len(out)
		out[pos] ^= edits[i+2]
	}
	return out
}

// checkPQRun runs p with exactly its required gas and checks the invariants
// every call must keep: no error, all gas used, an unmodified input and a
// 32-byte 0/1 output. It returns the verdict.
func checkPQRun(t *testing.T, p PrecompiledContract, input []byte) bool {
	orig := bytes.Clone(input)
	ret, left, err := RunPrecompiledContract(p, input, p.RequiredGas(input), nil)
	if err != nil || left != 0 {
		t.Fatalf("%s: err %v, gas left %d", p.Name(), err, left)
	}
	if !bytes.Equal(input, orig) {
		t.Fatalf("%s modified its input", p.Name())
	}
	if !bytes.Equal(ret, true32Byte) && !bytes.Equal(ret, false32Byte) {
		t.Fatalf("%s returned %x", p.Name(), ret)
	}
	return bytes.Equal(ret, true32Byte)
}

// FuzzMLDSA65Verify mutates valid ML-DSA-65 inputs and compares the
// precompile with circl, an independent implementation.
func FuzzMLDSA65Verify(f *testing.F) {
	seeds := pqValidSeeds(f, "mldsa65Verify")
	for i := range seeds {
		f.Add(uint16(i), []byte{})
		f.Add(uint16(i), []byte{0x14, 0x5c, 0x01}) // hint area
	}
	f.Fuzz(func(t *testing.T, seed uint16, edits []byte) {
		orig := seeds[int(seed)%len(seeds)]
		input := pqMutate(orig, edits)
		got := checkPQRun(t, mldsa65Precompile, input)
		pk, sig, msg := input[:mldsa.MLDSA65PublicKeySize], input[mldsa.MLDSA65PublicKeySize:mldsa65InputPrefix], input[mldsa65InputPrefix:]
		if want := circlMLDSA65(pk, sig, msg); got != want {
			t.Fatalf("precompile %v, circl %v for input %x", got, want, input)
		}
		// ML-DSA with the canonical hint encoding is strongly unforgeable:
		// any change to pk, signature or message must fail.
		if got != bytes.Equal(input, orig) {
			t.Fatalf("verdict %v for input changed=%v", got, !bytes.Equal(input, orig))
		}
	})
}

// FuzzMLDSA65VerifyRaw feeds arbitrary bytes, including every short length.
func FuzzMLDSA65VerifyRaw(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, mldsa65InputPrefix))
	f.Fuzz(func(t *testing.T, input []byte) {
		checkPQRun(t, mldsa65Precompile, input)
	})
}

// FuzzSLHDSASha2128sVerify mutates valid SLH-DSA inputs: a mutated input must
// never verify unless the mutation was a no-op.
func FuzzSLHDSASha2128sVerify(f *testing.F) {
	seeds := pqValidSeeds(f, "slhdsaSha2128sVerify")
	for i := range seeds {
		f.Add(uint16(i), []byte{})
		f.Add(uint16(i), []byte{0x00, 0x20, 0x80}) // R
	}
	f.Fuzz(func(t *testing.T, seed uint16, edits []byte) {
		orig := seeds[int(seed)%len(seeds)]
		input := pqMutate(orig, edits)
		if got := checkPQRun(t, slhdsaPrecompile, input); got != bytes.Equal(input, orig) {
			t.Fatalf("verdict %v for input changed=%v", got, !bytes.Equal(input, orig))
		}
	})
}

// FuzzSLHDSASha2128sVerifyRaw feeds arbitrary bytes, including every short length.
func FuzzSLHDSASha2128sVerifyRaw(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, slhdsaSha2128sInputPrefix))
	f.Fuzz(func(t *testing.T, input []byte) {
		checkPQRun(t, slhdsaPrecompile, input)
	})
}
