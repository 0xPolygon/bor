// Copyright 2024 The go-ethereum Authors
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
	"crypto/rand"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/nativectf"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// ctfDispatchChainConfig mirrors core/vm/runtime's default chain config — the fork
// under which the native gas constants (nativectf.ExternalCallGas) were derived.
// The native gas charge must equal the interpreter's under the SAME rules, so the
// dispatch tests pin this config.
func ctfDispatchChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:                 big.NewInt(1),
		HomesteadBlock:          new(big.Int),
		DAOForkBlock:            new(big.Int),
		EIP150Block:             new(big.Int),
		EIP155Block:             new(big.Int),
		EIP158Block:             new(big.Int),
		ByzantiumBlock:          new(big.Int),
		ConstantinopleBlock:     new(big.Int),
		PetersburgBlock:         new(big.Int),
		IstanbulBlock:           new(big.Int),
		MuirGlacierBlock:        new(big.Int),
		BerlinBlock:             new(big.Int),
		LondonBlock:             new(big.Int),
		TerminalTotalDifficulty: big.NewInt(0),
		ShanghaiBlock:           new(big.Int),
		CancunBlock:             new(big.Int),
	}
}

func ctfTestCode(t testing.TB) []byte {
	raw, err := os.ReadFile("nativectf/testdata/ctf_code.hex")
	if err != nil {
		t.Fatalf("read ctf code: %v", err)
	}
	return common.FromHex(strings.TrimSpace(string(raw)))
}

// newCTFEVM builds an EVM with the canonical CTF runtime code installed at a
// known address, under the pinned chain config.
func newCTFEVM(t testing.TB, mode NativeCTFMode, tracer *tracing.Hooks) (*EVM, common.Address) {
	t.Helper()
	db, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	addr := common.BytesToAddress([]byte("ctf"))
	caller := common.BytesToAddress([]byte("caller"))
	db.CreateAccount(addr)
	db.SetCode(addr, ctfTestCode(t), tracing.CodeChangeUnspecified)
	db.CreateAccount(caller)
	db.Finalise(true)

	bctx := BlockContext{
		CanTransfer: func(StateDB, common.Address, *uint256.Int) bool { return true },
		Transfer:    func(StateDB, common.Address, common.Address, *uint256.Int) {},
		GetHash:     func(n uint64) common.Hash { return common.Hash{} },
		BlockNumber: big.NewInt(0),
		Time:        0,
		Difficulty:  new(big.Int),
		GasLimit:    50_000_000,
		BaseFee:     new(big.Int),
		BlobBaseFee: new(big.Int),
	}
	cfg := Config{NativeCTF: mode, Tracer: tracer}
	evm := NewEVM(bctx, db, ctfDispatchChainConfig(), cfg)
	return evm, addr
}

// ctfCalldata builds getCollectionId(parent, conditionId, indexSet) calldata.
func ctfCalldata(parent, conditionId [32]byte, indexSet *big.Int) []byte {
	var idx [32]byte
	indexSet.FillBytes(idx[:])
	data := make([]byte, 0, 4+32*3)
	data = append(data, nativectf.GetCollectionIdSelector[:]...)
	data = append(data, parent[:]...)
	data = append(data, conditionId[:]...)
	data = append(data, idx[:]...)
	return data
}

func sampleCalldata(t testing.TB) []byte {
	var ci [32]byte
	copy(ci[:], crypto.Keccak256([]byte("samplecondition")))
	return ctfCalldata([32]byte{}, ci, big.NewInt(1))
}

func TestTryNativeCTF_FallsBackWhenOff(t *testing.T) {
	evm, addr := newCTFEVM(t, NativeCTFOff, nil)
	if _, _, ok := evm.tryNativeCTF(addr, sampleCalldata(t), 1_000_000); ok {
		t.Fatal("must not fast-path when flag is Off")
	}
}

func TestTryNativeCTF_FallsBackWhenShadow(t *testing.T) {
	evm, addr := newCTFEVM(t, NativeCTFShadow, nil)
	if _, _, ok := evm.tryNativeCTF(addr, sampleCalldata(t), 1_000_000); ok {
		t.Fatal("must not fast-path (active substitution) when flag is Shadow")
	}
}

func TestTryNativeCTF_FallsBackWhenTracerAttached(t *testing.T) {
	evm, addr := newCTFEVM(t, NativeCTFActive, &tracing.Hooks{})
	if _, _, ok := evm.tryNativeCTF(addr, sampleCalldata(t), 1_000_000); ok {
		t.Fatal("must not fast-path when a tracer is attached")
	}
}

func TestTryNativeCTF_FallsBackWhenUnknownCodehash(t *testing.T) {
	evm, _ := newCTFEVM(t, NativeCTFActive, nil)
	other := common.BytesToAddress([]byte("not-ctf"))
	evm.StateDB.CreateAccount(other)
	evm.StateDB.SetCode(other, []byte{0x60, 0x00}, tracing.CodeChangeUnspecified)
	if _, _, ok := evm.tryNativeCTF(other, sampleCalldata(t), 1_000_000); ok {
		t.Fatal("must not fast-path for an unrecognized codehash")
	}
}

func TestTryNativeCTF_FallsBackWhenWrongSelector(t *testing.T) {
	evm, addr := newCTFEVM(t, NativeCTFActive, nil)
	data := sampleCalldata(t)
	data[0] ^= 0xff // corrupt the selector
	if _, _, ok := evm.tryNativeCTF(addr, data, 1_000_000); ok {
		t.Fatal("must not fast-path for a non-getCollectionId selector")
	}
}

func TestTryNativeCTF_FallsBackWhenShortInput(t *testing.T) {
	evm, addr := newCTFEVM(t, NativeCTFActive, nil)
	data := sampleCalldata(t)[:99] // one byte short
	if _, _, ok := evm.tryNativeCTF(addr, data, 1_000_000); ok {
		t.Fatal("must not fast-path for malformed calldata length")
	}
}

func TestTryNativeCTF_FallsBackWhenParentNonZero(t *testing.T) {
	evm, addr := newCTFEVM(t, NativeCTFActive, nil)
	var parent, ci [32]byte
	parent[31] = 1
	copy(ci[:], crypto.Keccak256([]byte("samplecondition")))
	if _, _, ok := evm.tryNativeCTF(addr, ctfCalldata(parent, ci, big.NewInt(1)), 1_000_000); ok {
		t.Fatal("must not fast-path for parent != 0 (handled in a later task)")
	}
}

func TestTryNativeCTF_FallsBackWhenInsufficientGas(t *testing.T) {
	evm, addr := newCTFEVM(t, NativeCTFActive, nil)
	data := sampleCalldata(t)
	if _, _, ok := evm.tryNativeCTF(addr, data, 10); ok {
		t.Fatal("must not fast-path when gas is insufficient")
	}
}

func TestTryNativeCTF_SuccessReturnsResultAndGas(t *testing.T) {
	evm, addr := newCTFEVM(t, NativeCTFActive, nil)
	var ci [32]byte
	copy(ci[:], crypto.Keccak256([]byte("samplecondition")))
	idx := big.NewInt(1)
	const gas = 1_000_000

	ret, leftover, ok := evm.tryNativeCTF(addr, ctfCalldata([32]byte{}, ci, idx), gas)
	if !ok {
		t.Fatal("fast-path must fire for a well-formed recognized call with ample gas")
	}
	want, iters, pf, b254 := nativectf.GetCollectionId([32]byte{}, ci, idx)
	if string(ret) != string(want[:]) {
		t.Fatalf("ret = %x, want %x", ret, want)
	}
	if exp := gas - nativectf.ExternalCallGas(iters, pf, b254); leftover != exp {
		t.Fatalf("leftover = %d, want %d", leftover, exp)
	}
}

// TestNativeCTF_ActiveMatchesInterpreter is the consensus-equivalence gate at the
// dispatch layer: for many random inputs, evm.Call with NativeCTF=Active must
// return byte-identical ret AND identical leftover gas to evm.Call with the flag
// Off (stock interpreter), under the same rules and state.
func TestNativeCTF_ActiveMatchesInterpreter(t *testing.T) {
	off, addr := newCTFEVM(t, NativeCTFOff, nil)
	on, _ := newCTFEVM(t, NativeCTFActive, nil)
	caller := common.BytesToAddress([]byte("caller"))
	maxU := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	const gas = 5_000_000

	check := func(ci [32]byte, idx *big.Int) {
		data := ctfCalldata([32]byte{}, ci, idx)
		retOff, leftOff, errOff := off.Call(caller, addr, data, gas, uint256.NewInt(0))
		retOn, leftOn, errOn := on.Call(caller, addr, data, gas, uint256.NewInt(0))
		if errOff != nil || errOn != nil {
			t.Fatalf("call error: off=%v on=%v (ci=%x idx=%s)", errOff, errOn, ci, idx)
		}
		if string(retOff) != string(retOn) {
			t.Fatalf("ret MISMATCH ci=%x idx=%s: interp=%x native=%x", ci, idx, retOff, retOn)
		}
		if leftOff != leftOn {
			t.Fatalf("gas MISMATCH ci=%x idx=%s: interp=%d native=%d", ci, idx, leftOff, leftOn)
		}
		// And the native bytes equal the standalone native implementation.
		want, _, _, _ := nativectf.GetCollectionId([32]byte{}, ci, idx)
		if string(retOn) != string(want[:]) {
			t.Fatalf("native ret MISMATCH ci=%x idx=%s: call=%x impl=%x", ci, idx, retOn, want)
		}
	}

	before := nativeCTFFastPathCounter.Snapshot().Count()

	var kc [32]byte
	copy(kc[:], crypto.Keccak256([]byte("samplecondition")))
	check(kc, big.NewInt(1))
	check([32]byte{}, big.NewInt(0))
	check([32]byte{}, maxU)
	const n = 500
	for i := 0; i < n; i++ {
		var ci [32]byte
		rand.Read(ci[:])
		idx, _ := rand.Int(rand.Reader, maxU)
		check(ci, idx)
	}

	// The fast-path counter must have advanced by exactly the number of Active
	// calls — proving evm.Call actually routed through the native path (and not
	// that native merely happens to equal the interpreter).
	if got := nativeCTFFastPathCounter.Snapshot().Count() - before; got != int64(n+3) {
		t.Fatalf("fast-path fired %d times, want %d (native dispatch not wired into Call?)", got, n+3)
	}
}

// TestNativeCTF_StaticCallActiveMatchesInterpreter mirrors the above through the
// StaticCall entrypoint (getCollectionId is a view function).
func TestNativeCTF_StaticCallActiveMatchesInterpreter(t *testing.T) {
	off, addr := newCTFEVM(t, NativeCTFOff, nil)
	on, _ := newCTFEVM(t, NativeCTFActive, nil)
	caller := common.BytesToAddress([]byte("caller"))
	maxU := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	const gas = 5_000_000

	for i := 0; i < 200; i++ {
		var ci [32]byte
		rand.Read(ci[:])
		idx, _ := rand.Int(rand.Reader, maxU)
		data := ctfCalldata([32]byte{}, ci, idx)
		retOff, leftOff, errOff := off.StaticCall(caller, addr, data, gas)
		retOn, leftOn, errOn := on.StaticCall(caller, addr, data, gas)
		if errOff != nil || errOn != nil {
			t.Fatalf("staticcall error: off=%v on=%v", errOff, errOn)
		}
		if string(retOff) != string(retOn) || leftOff != leftOn {
			t.Fatalf("staticcall MISMATCH ci=%x idx=%s: interp=(%x,%d) native=(%x,%d)", ci, idx, retOff, leftOff, retOn, leftOn)
		}
	}
}
