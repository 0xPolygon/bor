package nativectf

import (
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestBuildLadderTable_FindsCanonical(t *testing.T) {
	code := loadHex(t, "testdata/ctf_code.hex")
	tbl := BuildLadderTable(code)
	if len(tbl) == 0 {
		t.Fatal("expected >=1 ladder block in canonical CTF code")
	}
}

func TestLadderTableFor_CachesByCodeHash(t *testing.T) {
	code := loadHex(t, "testdata/ctf_code.hex")
	h := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	a := LadderTableFor(h, code)
	b := LadderTableFor(h, nil) // second call must hit cache (code arg ignored on hit)
	if reflect.ValueOf(a).Pointer() != reflect.ValueOf(b).Pointer() {
		t.Fatal("LadderTableFor did not return the cached map instance")
	}
	if len(a) == 0 {
		t.Fatal("cached table unexpectedly empty")
	}
}

func TestLadderTableFor_NonLadderCodeIsEmptyNotNil(t *testing.T) {
	// Simple non-CTF bytecode: PUSH1 1 PUSH1 2 ADD STOP — no marker.
	code := []byte{0x60, 0x01, 0x60, 0x02, 0x01, 0x00}
	h := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	tbl := LadderTableFor(h, code)
	if tbl == nil {
		t.Fatal("expected non-nil empty table")
	}
	if len(tbl) != 0 {
		t.Fatalf("expected empty table, got %d entries", len(tbl))
	}
}

func TestLadderTableFor_ZeroHashNotCached(t *testing.T) {
	code := loadHex(t, "testdata/ctf_code.hex")
	a := LadderTableFor(common.Hash{}, code)
	if len(a) == 0 {
		t.Fatal("zero-hash build should still analyze")
	}
}
