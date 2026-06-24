package vm_test

// Detection-overhead benchmark (the cost gate): a NORMAL contract with many
// JUMPDESTs and no ladder must run essentially as fast with the feature ACTIVE
// as OFF — otherwise recognition overhead on ordinary traffic is a net loss.

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/nativectf"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
)

// loopCode builds a contract that spins a JUMPDEST-per-iteration countdown loop.
// No ladder marker -> exercises the hook's empty-table fast path on every JUMPDEST.
func loopCode(iters uint32) []byte {
	// PUSH3 iters; JUMPDEST(@4); PUSH1 1; SWAP1; SUB; DUP1; PUSH1 0x04; JUMPI; STOP
	return []byte{
		0x62, byte(iters >> 16), byte(iters >> 8), byte(iters), // PUSH3 iters
		0x5b,       // JUMPDEST @4
		0x60, 0x01, // PUSH1 1
		0x90,       // SWAP1
		0x03,       // SUB -> c-1
		0x80,       // DUP1
		0x60, 0x04, // PUSH1 4
		0x57, // JUMPI
		0x00, // STOP
	}
}

func benchLoop(b *testing.B, mode vm.NativeCTFMode) {
	code := loopCode(20000) // 20k JUMPDEST executions per call
	addr := common.BytesToAddress([]byte("loopbench"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cfg := new(runtime.Config)
		cfg.State, _ = state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
		cfg.GasLimit = 1_000_000_000
		cfg.EVMConfig = vm.Config{NativeCTF: mode}
		cfg.State.CreateAccount(addr)
		cfg.State.SetCode(addr, code, tracing.CodeChangeUnspecified)
		if _, _, err := runtime.Call(addr, nil, cfg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLadderOverhead_Off(b *testing.B)    { benchLoop(b, vm.NativeCTFOff) }
func BenchmarkLadderOverhead_Active(b *testing.B) { benchLoop(b, vm.NativeCTFActive) }

// BenchmarkLadderTableBuild measures the one-time per-codehash analysis cost on a
// large NON-matching contract (the bytes.Contains pre-filter should make it cheap).
func BenchmarkLadderTableBuild_NonMatching(b *testing.B) {
	big := make([]byte, 24000)
	for i := range big {
		big[i] = 0x5b // all JUMPDESTs: worst case for the scan, but no marker
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if t := nativectf.BuildLadderTable(big); len(t) != 0 {
			b.Fatal("unexpected ladder in junk code")
		}
	}
}
