package vm_test

// Differential validation across every distinct ladder bytecode captured from
// mainnet. For each PURE block: run the real interpreter (OFF) and the active
// substitution over fuzzed x and assert byte+gas identity, and that the result
// equals ModSqrtCandidate(x). Memory-touching variants (canonical CTF) are
// asserted recognized-but-Pure=false (Phase 2) so coverage gaps are explicit.

import (
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/nativectf"
	"github.com/holiman/uint256"
)

func TestLadder_DifferentialAcrossAllVariants(t *testing.T) {
	files, err := os.ReadDir("nativectf/testdata")
	if err != nil {
		t.Fatal(err)
	}
	p, e := bigPtest(), bigEtest()
	totalPure, totalImpure := 0, 0
	xs := []*uint256.Int{
		new(uint256.Int).SetUint64(1),
		new(uint256.Int).SetUint64(0xC0FFEE),
		new(uint256.Int).SetUint64(0x1234567890ABCDEF),
	}
	// a few large, structured values too
	big1, _ := uint256.FromHex("0x30644e72e131a029b85045b68181585d97816a916871ca8d3c208c16d87cfd46") // P-1
	big2, _ := uint256.FromHex("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	xs = append(xs, big1, big2)

	for _, f := range files {
		name := f.Name()
		if !strings.HasPrefix(name, "ladder_") && name != "ctf_code.hex" {
			continue
		}
		raw, err := os.ReadFile("nativectf/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		code := common.FromHex(strings.TrimSpace(string(raw)))
		tbl := nativectf.BuildLadderTable(code)
		if len(tbl) == 0 {
			t.Errorf("%s: no ladder block recognized", name)
			continue
		}
		for pc, m := range tbl {
			if !m.Pure {
				totalImpure++
				t.Logf("%s: ladder@%d recognized, Pure=false (Phase 2)", name, pc)
				continue
			}
			totalPure++
			block := code[pc:m.EndPC]
			for _, x := range xs {
				synth := buildSynthetic(block, m, x)
				retOff, gasOff := runSynthetic(t, synth, vm.NativeCTFOff)
				retAct, gasAct := runSynthetic(t, synth, vm.NativeCTFActive)
				want := new(big.Int).Exp(x.ToBig(), e, p)
				var wb [32]byte
				want.FillBytes(wb[:])
				if [32]byte(retOff) != wb {
					t.Fatalf("%s@%d x=%s: OFF result %x != native %x", name, pc, x, retOff, wb)
				}
				if string(retAct) != string(retOff) || gasAct != gasOff {
					t.Fatalf("%s@%d x=%s: ACTIVE (%x,%d) != OFF (%x,%d)", name, pc, x, retAct, gasAct, retOff, gasOff)
				}
			}
		}
	}
	t.Logf("differential: %d pure blocks verified (result+gas exact), %d impure deferred to Phase 2", totalPure, totalImpure)
	if totalPure == 0 {
		t.Fatal("no pure ladder blocks exercised")
	}
}
