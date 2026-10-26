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
	"crypto/sha256"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
)

// TestPQVerifyCacheParity runs every vector through the cache twice (miss,
// then hit) and checks output and gas against an uncached run.
func TestPQVerifyCacheParity(t *testing.T) {
	for _, tc := range []struct {
		file string
		addr common.Address
	}{
		{"mldsa65Verify", mldsa65VerifyAddr},
		{"slhdsaSha2128sVerify", slhdsaSha2128sVerifyAddr},
	} {
		tests, err := loadJson(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		p := allPrecompiles[tc.addr]
		cache := NewPQVerifyCache()
		evm := &EVM{}
		evm.Config.PQVerifyCache = cache
		distinct := make(map[string]bool)
		for _, test := range tests {
			input := common.Hex2Bytes(test.Input)
			if pqVerifyCacheable(p, input) {
				distinct[test.Input] = true
			}
			gas := test.Gas + 1000
			want, wantGas, wantErr := RunPrecompiledContract(p, input, gas, nil)
			for round := 0; round < 2; round++ {
				got, gotGas, err := evm.runPrecompile(p, tc.addr, input, gas)
				if err != wantErr || gotGas != wantGas || !bytes.Equal(got, want) {
					t.Fatalf("%s round %d: got (%x, %d, %v), want (%x, %d, %v)",
						test.Name, round, got, gotGas, err, want, wantGas, wantErr)
				}
			}
		}
		if n := cache.count.Load(); n != int64(len(distinct)) {
			t.Errorf("%s: %d cache entries, want %d", tc.file, n, len(distinct))
		}
	}
}

func TestPQVerifyCacheHitChargesGas(t *testing.T) {
	f := newMLDSAFixture(t, []byte("cache"))
	input := f.input()
	p := mldsa65Precompile
	cost := p.RequiredGas(input)

	var traced []uint64
	evm := &EVM{}
	evm.Config.PQVerifyCache = NewPQVerifyCache()
	evm.Config.Tracer = &tracing.Hooks{OnGasChange: func(old, new uint64, _ tracing.GasChangeReason) {
		traced = append(traced, old-new)
	}}
	for i := 0; i < 2; i++ {
		ret, left, err := evm.runPrecompile(p, mldsa65VerifyAddr, input, cost+5)
		if err != nil || left != 5 || !bytes.Equal(ret, true32Byte) {
			t.Fatalf("call %d: got (%x, %d, %v)", i, ret, left, err)
		}
	}
	if len(traced) != 2 || traced[0] != cost || traced[1] != cost {
		t.Errorf("traced gas changes %v, want two of %d", traced, cost)
	}
	if _, _, err := evm.runPrecompile(p, mldsa65VerifyAddr, input, cost-1); err != ErrOutOfGas {
		t.Errorf("hit with too little gas: err %v, want ErrOutOfGas", err)
	}
}

// TestPQVerifyCacheOutOfGasNotStored checks that an out-of-gas call does no
// verification and stores nothing.
func TestPQVerifyCacheOutOfGasNotStored(t *testing.T) {
	f := newSLHFixture(t, []byte("oog"))
	cache := NewPQVerifyCache()
	evm := &EVM{}
	evm.Config.PQVerifyCache = cache
	if _, left, err := evm.runPrecompile(slhdsaPrecompile, slhdsaSha2128sVerifyAddr, f.input(), 100); err != ErrOutOfGas || left != 0 {
		t.Fatalf("got (%d, %v), want (0, ErrOutOfGas)", left, err)
	}
	if n := cache.count.Load(); n != 0 {
		t.Errorf("%d cache entries after out-of-gas, want 0", n)
	}
}

// TestPQVerifyCacheKey checks that the key covers the address and every
// input byte: a cached valid result must never answer a different input.
func TestPQVerifyCacheKey(t *testing.T) {
	f := newMLDSAFixture(t, []byte("key"))
	input := f.input()
	cache := NewPQVerifyCache()
	evm := &EVM{}
	evm.Config.PQVerifyCache = cache
	if ret, _, _ := evm.runPrecompile(mldsa65Precompile, mldsa65VerifyAddr, input, 1<<20); !bytes.Equal(ret, true32Byte) {
		t.Fatal("valid signature rejected")
	}
	for _, i := range []int{0, len(input) / 2, len(input) - 1} {
		changed := bytes.Clone(input)
		changed[i] ^= 1
		if ret, _, _ := evm.runPrecompile(mldsa65Precompile, mldsa65VerifyAddr, changed, 1<<20); !bytes.Equal(ret, false32Byte) {
			t.Errorf("byte %d changed: got %x from cache, want 0", i, ret)
		}
	}
	// Same input under another address is a different entry.
	key := pqVerifyCacheKey{addr: slhdsaSha2128sVerifyAddr, input: sha256.Sum256(input)}
	if _, ok := cache.Load(key); ok {
		t.Error("entry stored under the wrong address")
	}
}

// TestPQVerifyCacheOnlyPQ checks that other precompiles bypass the cache.
func TestPQVerifyCacheOnlyPQ(t *testing.T) {
	cache := NewPQVerifyCache()
	evm := &EVM{}
	evm.Config.PQVerifyCache = cache
	p := allPrecompiles[common.BytesToAddress([]byte{2})]
	if _, _, err := evm.runPrecompile(p, common.BytesToAddress([]byte{2}), []byte("abc"), 1000); err != nil {
		t.Fatal(err)
	}
	if n := cache.count.Load(); n != 0 {
		t.Errorf("sha256 call stored %d entries in the PQ cache", n)
	}
}

// TestPQVerifyCacheSkipsLongMessages checks that inputs whose key hash gas
// doesn't pay for are verified without the cache.
func TestPQVerifyCacheSkipsLongMessages(t *testing.T) {
	f := newMLDSAFixture(t, make([]byte, pqVerifyCacheMaxMsg+1))
	cache := NewPQVerifyCache()
	evm := &EVM{}
	evm.Config.PQVerifyCache = cache
	ret, _, err := evm.runPrecompile(mldsa65Precompile, mldsa65VerifyAddr, f.input(), 1<<22)
	if err != nil || !bytes.Equal(ret, true32Byte) {
		t.Fatalf("got (%x, %v)", ret, err)
	}
	if n := cache.count.Load(); n != 0 {
		t.Errorf("%d cache entries for a %d-byte message, want 0", n, len(f.msg))
	}
	short := newMLDSAFixture(t, make([]byte, pqVerifyCacheMaxMsg))
	evm.runPrecompile(mldsa65Precompile, mldsa65VerifyAddr, short.input(), 1<<22)
	if n := cache.count.Load(); n != 1 {
		t.Errorf("%d cache entries for a %d-byte message, want 1", n, len(short.msg))
	}
}
