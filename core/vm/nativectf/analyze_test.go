package nativectf

import (
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func loadHex(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return common.FromHex(strings.TrimSpace(string(b)))
}

// findLadderJumpdest scans for the JUMPDEST that begins a recognized ladder block.
func findLadderJumpdest(t *testing.T, code []byte) (uint64, LadderMeta) {
	for pc := 0; pc < len(code); pc++ {
		if code[pc] != 0x5b { // JUMPDEST
			continue
		}
		if m, ok := AnalyzeLadder(code, uint64(pc)); ok {
			return uint64(pc), m
		}
	}
	t.Fatal("no ladder jumpdest found")
	return 0, LadderMeta{}
}

func TestAnalyzeLadder_InlineVariant(t *testing.T) {
	code := loadHex(t, "testdata/ladder_inline.hex")
	pc, m := findLadderJumpdest(t, code)
	if !m.Pure {
		t.Fatal("inline variant must be Pure (no memory ops)")
	}
	if m.GasStatic < 7000 || m.GasStatic > 7200 {
		t.Fatalf("GasStatic=%d, expected ~7110", m.GasStatic)
	}
	if m.EndPC <= pc {
		t.Fatalf("EndPC=%d must be > startPC=%d", m.EndPC, pc)
	}
	if code[m.EndPC] != 0x56 && code[m.EndPC] != 0x57 { // block ends at JUMP/JUMPI
		t.Fatalf("EndPC opcode = %#x, expected JUMP/JUMPI", code[m.EndPC])
	}
	if m.BaseDepth < 0 {
		t.Fatalf("BaseDepth=%d must be >= 0", m.BaseDepth)
	}
	if m.In <= 0 || m.In > 32 {
		t.Fatalf("In=%d out of range", m.In)
	}
	if m.BaseDepth >= m.In {
		t.Fatalf("BaseDepth=%d must be < In=%d (x is consumed)", m.BaseDepth, m.In)
	}
	// exactly one Out slot is the computed result
	n := 0
	for _, s := range m.Out {
		if s.Kind == OutResult {
			n++
		}
		if s.Kind == OutEntry && (s.Depth < 0 || s.Depth >= m.In) {
			t.Fatalf("Out entry depth %d must be < In=%d", s.Depth, m.In)
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 computed result slot, got %d", n)
	}
	t.Logf("inline ladder: startPC=%d EndPC=%d gas=%d In=%d BaseDepth=%d outLen=%d",
		pc, m.EndPC, m.GasStatic, m.In, m.BaseDepth, len(m.Out))
}

func TestAnalyzeLadder_CanonicalIsImpure(t *testing.T) {
	// The canonical CTF caches the modulus in memory (CODECOPY/MLOAD) -> impure.
	// It must still be recognized as a ladder, but flagged Pure=false (Phase 2).
	code := loadHex(t, "testdata/ctf_code.hex")
	_, m := findLadderJumpdest(t, code)
	if m.Pure {
		t.Fatal("canonical CTF ladder has memory ops; must be Pure=false (Phase 2)")
	}
}

func TestAnalyzeLadder_RejectsNonLadder(t *testing.T) {
	code := loadHex(t, "testdata/ladder_inline.hex")
	if _, ok := AnalyzeLadder(code, 0); ok {
		t.Fatal("AnalyzeLadder accepted PC 0 (not a ladder JUMPDEST)")
	}
}
