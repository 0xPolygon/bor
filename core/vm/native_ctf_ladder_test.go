package vm_test

// End-to-end tests for the native BN254 ladder superinstruction on the PURE
// (inline-PUSH32) variant. The pure ladder runs only inside other contracts, so we
// synthesize a minimal contract that pushes garbage + x, runs the captured jump-free
// block verbatim, and returns the result slot. Garbage in the deeper slots makes
// this a strict check that the block's result depends only on x.

import (
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/nativectf"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/holiman/uint256"
)

func loadInlineBlock(t *testing.T) (block []byte, meta nativectf.LadderMeta) {
	raw, err := os.ReadFile("nativectf/testdata/ladder_inline.hex")
	if err != nil {
		t.Fatal(err)
	}
	code := common.FromHex(strings.TrimSpace(string(raw)))
	tbl := nativectf.BuildLadderTable(code)
	for pc, m := range tbl {
		if m.Pure {
			return code[pc:m.EndPC], m
		}
	}
	t.Fatal("no pure ladder block in inline variant")
	return nil, nativectf.LadderMeta{}
}

// buildSynthetic wraps the jump-free block in a contract: push 8 garbage words,
// then the In entry items (x at BaseDepth), run the block, drop down to the result
// slot, and RETURN it. The block is position-independent (jump-free).
func buildSynthetic(block []byte, meta nativectf.LadderMeta, x *uint256.Int) []byte {
	var b []byte
	push32 := func(v *uint256.Int) {
		w := v.Bytes32()
		b = append(b, 0x7f) // PUSH32
		b = append(b, w[:]...)
	}
	garbage := new(uint256.Int).SetUint64(0xDEADBEEFCAFE)
	for k := 0; k < 8; k++ { // deep garbage: must not affect the result
		push32(garbage)
	}
	for d := meta.In - 1; d >= 0; d-- { // deepest In item first; top is depth 0
		if d == meta.BaseDepth {
			push32(x)
		} else {
			push32(new(uint256.Int).SetUint64(0xBADBAD)) // non-x consumed slot (e.g. retdest)
		}
	}
	b = append(b, block...) // jump-free ladder block, leaves Out on top
	b = append(b, 0x5b)     // JUMPDEST: bounds the block for the analyzer; no-op at runtime
	rDepth, _ := meta.ResultDepthFromTop()
	for r := 0; r < rDepth; r++ {
		b = append(b, 0x50) // POP down to the result
	}
	b = append(b, 0x5f, 0x52)             // PUSH0 MSTORE  -> mem[0]=result
	b = append(b, 0x60, 0x20, 0x5f, 0xf3) // PUSH1 0x20 PUSH0 RETURN
	return b
}

func runSynthetic(t *testing.T, code []byte, mode vm.NativeCTFMode) (ret []byte, gasUsed uint64) {
	cfg := new(runtime.Config)
	cfg.State, _ = state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	cfg.GasLimit = 50_000_000
	cfg.EVMConfig = vm.Config{NativeCTF: mode}
	addr := common.BytesToAddress([]byte("synthladder"))
	cfg.State.CreateAccount(addr)
	cfg.State.SetCode(addr, code, tracing.CodeChangeUnspecified)
	out, leftover, err := runtime.Call(addr, nil, cfg)
	if err != nil {
		t.Fatalf("mode %d: call error: %v", mode, err)
	}
	return out, cfg.GasLimit - leftover
}

func counterCount(name string) int64 {
	return metrics.GetOrRegisterCounter(name, nil).Snapshot().Count()
}

func TestLadder_ShadowAndActive_PureVariant(t *testing.T) {
	block, meta := loadInlineBlock(t)
	x := new(uint256.Int).SetUint64(0x1234567)
	wantBig := new(big.Int).Exp(x.ToBig(), bigEtest(), bigPtest())
	var want [32]byte
	wantBig.FillBytes(want[:])
	code := buildSynthetic(block, meta, x)

	// OFF: ground truth (real interpreter runs the block).
	retOff, gasOff := runSynthetic(t, code, vm.NativeCTFOff)
	if [32]byte(retOff) != want {
		t.Fatalf("OFF: block result %x != ModSqrtCandidate %x (block uses more than x?)", retOff, want)
	}

	// SHADOW: must not change behavior, and must record a match (no mismatch).
	m0, mm0 := counterCount("vm/nativectf/ladder/match"), counterCount("vm/nativectf/ladder/mismatch")
	retShadow, gasShadow := runSynthetic(t, code, vm.NativeCTFShadow)
	if string(retShadow) != string(retOff) || gasShadow != gasOff {
		t.Fatalf("SHADOW changed behavior: ret/gas off=(%x,%d) shadow=(%x,%d)", retOff, gasOff, retShadow, gasShadow)
	}
	if counterCount("vm/nativectf/ladder/match") <= m0 {
		t.Fatal("SHADOW: match counter did not advance (hook never fired)")
	}
	if counterCount("vm/nativectf/ladder/mismatch") != mm0 {
		t.Fatal("SHADOW: mismatch counter advanced — native disagrees with interpreter")
	}

	// ACTIVE: byte- and gas-identical to OFF (the consensus-exactness gate).
	a0 := counterCount("vm/nativectf/ladder/active")
	retActive, gasActive := runSynthetic(t, code, vm.NativeCTFActive)
	if string(retActive) != string(retOff) {
		t.Fatalf("ACTIVE result %x != OFF %x", retActive, retOff)
	}
	if gasActive != gasOff {
		t.Fatalf("ACTIVE gas %d != OFF gas %d (not gas-exact)", gasActive, gasOff)
	}
	if counterCount("vm/nativectf/ladder/active") <= a0 {
		t.Fatal("ACTIVE: substitution counter did not advance")
	}
}

func TestLadder_ActiveGasStarvedFallsBack(t *testing.T) {
	block, meta := loadInlineBlock(t)
	x := new(uint256.Int).SetUint64(0x99)
	code := buildSynthetic(block, meta, x)

	cfg := new(runtime.Config)
	cfg.State, _ = state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	// Just under what the block needs: setup gas + (GasStatic-1) forces OOG inside
	// the block in BOTH off and active, identically.
	cfg.GasLimit = 100_000
	addrOff := common.BytesToAddress([]byte("gasoff"))
	cfg.State.CreateAccount(addrOff)
	cfg.State.SetCode(addrOff, code, tracing.CodeChangeUnspecified)

	cfg.EVMConfig = vm.Config{NativeCTF: vm.NativeCTFOff}
	_, leftoverOff, errOff := runtime.Call(addrOff, nil, cfg)

	cfg2 := new(runtime.Config)
	cfg2.State, _ = state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	cfg2.GasLimit = 100_000
	cfg2.State.CreateAccount(addrOff)
	cfg2.State.SetCode(addrOff, code, tracing.CodeChangeUnspecified)
	cfg2.EVMConfig = vm.Config{NativeCTF: vm.NativeCTFActive}
	_, leftoverActive, errActive := runtime.Call(addrOff, nil, cfg2)

	if (errOff == nil) != (errActive == nil) || leftoverOff != leftoverActive {
		t.Fatalf("gas-starved divergence: off=(err=%v,left=%d) active=(err=%v,left=%d)",
			errOff, leftoverOff, errActive, leftoverActive)
	}
}

// field constants for the test oracle (BN254 P and (P+1)/4).
func bigPtest() *big.Int {
	p, _ := new(big.Int).SetString("30644e72e131a029b85045b68181585d97816a916871ca8d3c208c16d87cfd47", 16)
	return p
}
func bigEtest() *big.Int {
	e, _ := new(big.Int).SetString("0c19139cb84c680a6e14116da060561765e05aa45a1c72a34f082305b61f3f52", 16)
	return e
}
