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

package runtime

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
)

// aotTestCode loads the CTF Exchange V2 runtime bytecode used by the
// transpiled-body prototype.
func aotTestCode(t testing.TB) []byte {
	raw, err := os.ReadFile("../aotexp/ctfexchange.hex")
	if err != nil {
		t.Skipf("bytecode fixture missing: %v", err)
	}
	code := common.FromHex(strings.TrimSpace(string(raw)))
	if len(code) == 0 {
		t.Fatal("empty bytecode fixture")
	}
	return code
}

// aotCallOnce runs a single call against the contract with or without the
// AOT dispatch and reports (ret, gasLeft, err).
func aotCallOnce(t testing.TB, code, input []byte, gas uint64, aot bool) ([]byte, uint64, error) {
	statedb, err := state.New(common.Hash{}, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	addr := common.HexToAddress("0xE111180000d2663C0091e4f400237545B87B996B")
	statedb.CreateAccount(addr)
	statedb.SetCode(addr, code, tracing.CodeChangeUnspecified)
	// A couple of storage slots so shallow SLOAD-dependent paths take
	// non-trivial branches deterministically.
	for i := byte(0); i < 8; i++ {
		statedb.SetState(addr, common.BytesToHash([]byte{i}), common.BytesToHash([]byte{0x01, i}))
	}
	sender := common.HexToAddress("0x1000000000000000000000000000000000000001")
	statedb.CreateAccount(sender)
	statedb.SetBalance(sender, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)

	cfg := &Config{
		State:       statedb,
		GasLimit:    gas,
		Origin:      sender,
		BlockNumber: big.NewInt(75_000_000),
		Time:        1_750_000_000,
		ChainConfig: nil, // setDefaults: all forks active
	}
	cfg.EVMConfig = vm.Config{EnableAOT: aot}
	return Call(addr, input, cfg)
}

// aotInputs returns a deterministic corpus of calldata: every 4-byte selector
// found in the bytecode's PUSH4 immediates, each with several argument
// payload shapes. This exercises the dispatcher, calldata decoding, guard
// clauses and revert paths of essentially every external function.
func aotInputs(code []byte) [][]byte {
	// Collect PUSH4 immediates (function selectors + a few false positives,
	// which are fine — they just produce fallback/revert paths).
	var selectors [][]byte
	seen := make(map[string]bool)
	for pc := 0; pc < len(code); {
		op := code[pc]
		if op >= 0x60 && op <= 0x7F {
			n := int(op-0x60) + 1
			if op == 0x63 && pc+5 <= len(code) { // PUSH4
				sel := code[pc+1 : pc+5]
				if !seen[string(sel)] && !bytes.Equal(sel, []byte{0xff, 0xff, 0xff, 0xff}) {
					seen[string(sel)] = true
					selectors = append(selectors, sel)
				}
			}
			pc += 1 + n
		} else {
			pc++
		}
	}
	var inputs [][]byte
	inputs = append(inputs, nil, []byte{0x01}, []byte{0x00, 0x00, 0x00})
	for i, sel := range selectors {
		// Shape A: selector only (short calldata).
		inputs = append(inputs, append([]byte{}, sel...))
		// Shape B: selector + 4 zero words.
		in := append([]byte{}, sel...)
		in = append(in, make([]byte, 128)...)
		inputs = append(inputs, in)
		// Shape C: selector + patterned words (nonzero offsets/lengths).
		in = append([]byte{}, sel...)
		for w := 0; w < 6; w++ {
			word := make([]byte, 32)
			binary.BigEndian.PutUint64(word[24:], uint64(i*7+w*32))
			in = append(in, word...)
		}
		inputs = append(inputs, in)
	}
	return inputs
}

// TestAOTDifferential asserts that the transpiled body and the interpreter
// produce identical (ret, gasUsed, err) for a broad deterministic corpus.
func TestAOTDifferential(t *testing.T) {
	code := aotTestCode(t)
	inputs := aotInputs(code)
	t.Logf("corpus: %d inputs", len(inputs))

	var mismatches int
	for i, input := range inputs {
		for _, gas := range []uint64{60_000, 300_000, 5_000_000} {
			retI, gasI, errI := aotCallOnce(t, code, input, gas, false)
			retA, gasA, errA := aotCallOnce(t, code, input, gas, true)
			if !bytes.Equal(retI, retA) || gasI != gasA || fmt.Sprint(errI) != fmt.Sprint(errA) {
				mismatches++
				t.Errorf("input %d (%x… gas=%d): interp(ret=%x gasLeft=%d err=%v) != aot(ret=%x gasLeft=%d err=%v)",
					i, firstN(input, 8), gas, firstN(retI, 16), gasI, errI, firstN(retA, 16), gasA, errA)
				if mismatches > 10 {
					t.Fatal("too many mismatches, aborting")
				}
			}
		}
	}
}

func firstN(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}

// BenchmarkAOTvsInterp measures per-call wall time for both paths over the
// same corpus, with state setup hoisted out of the loop (synthetic calldata;
// real recorded frames land later).
func BenchmarkAOTvsInterp(b *testing.B) {
	code := aotTestCode(b)
	inputs := aotInputs(code)
	for _, mode := range []struct {
		name string
		aot  bool
	}{{"interp", false}, {"aot", true}} {
		b.Run(mode.name, func(b *testing.B) {
			statedb, err := state.New(common.Hash{}, state.NewDatabaseForTesting())
			if err != nil {
				b.Fatal(err)
			}
			addr := common.HexToAddress("0xE111180000d2663C0091e4f400237545B87B996B")
			statedb.CreateAccount(addr)
			statedb.SetCode(addr, code, tracing.CodeChangeUnspecified)
			for i := byte(0); i < 8; i++ {
				statedb.SetState(addr, common.BytesToHash([]byte{i}), common.BytesToHash([]byte{0x01, i}))
			}
			sender := common.HexToAddress("0x1000000000000000000000000000000000000001")
			statedb.CreateAccount(sender)
			statedb.SetBalance(sender, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
			cfg := &Config{
				State:       statedb,
				GasLimit:    300_000,
				Origin:      sender,
				BlockNumber: big.NewInt(75_000_000),
				Time:        1_750_000_000,
			}
			cfg.EVMConfig = vm.Config{EnableAOT: mode.aot}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				in := inputs[i%len(inputs)]
				_, _, _ = Call(addr, in, cfg)
			}
		})
	}
}
