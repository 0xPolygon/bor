package state

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestPDB_GetCodeHash_SetCodeTwiceInvalidatesCache(t *testing.T) {
	pdb, _, _ := newTestPDB(t, 0)
	addr := common.HexToAddress("0xdead")

	codeA := []byte{0x60, 0x00, 0x60, 0x00, 0xfd}
	codeB := []byte{0x60, 0x01, 0x60, 0x02, 0x60, 0x03, 0xfd}

	pdb.SetCode(addr, codeA, tracing.CodeChangeUnspecified)
	if got, want := pdb.GetCodeHash(addr), crypto.Keccak256Hash(codeA); got != want {
		t.Fatalf("hash after first SetCode = %x, want %x", got, want)
	}

	pdb.SetCode(addr, codeB, tracing.CodeChangeUnspecified)
	if got, want := pdb.GetCodeHash(addr), crypto.Keccak256Hash(codeB); got != want {
		t.Fatalf("hash after second SetCode = %x, want %x (stale cache from first SetCode)", got, want)
	}
}

func TestPDB_GetCodeHash_RevertInvalidatesCache(t *testing.T) {
	pdb, _, _ := newTestPDB(t, 0)
	addr := common.HexToAddress("0xbeef")

	codeA := []byte{0x60, 0x00, 0x60, 0x00, 0xfd}
	codeB := []byte{0x60, 0x01, 0x60, 0x02, 0x60, 0x03, 0xfd}

	pdb.SetCode(addr, codeA, tracing.CodeChangeUnspecified)
	if got, want := pdb.GetCodeHash(addr), crypto.Keccak256Hash(codeA); got != want {
		t.Fatalf("hash after first SetCode = %x, want %x", got, want)
	}

	snap := pdb.Snapshot()
	pdb.SetCode(addr, codeB, tracing.CodeChangeUnspecified)
	if got, want := pdb.GetCodeHash(addr), crypto.Keccak256Hash(codeB); got != want {
		t.Fatalf("hash after second SetCode = %x, want %x", got, want)
	}

	pdb.RevertToSnapshot(snap)
	if got, want := pdb.GetCode(addr), codeA; string(got) != string(want) {
		t.Fatalf("code after revert = %x, want %x", got, want)
	}
	if got, want := pdb.GetCodeHash(addr), crypto.Keccak256Hash(codeA); got != want {
		t.Fatalf("hash after revert = %x, want %x (stale cache from reverted SetCode)", got, want)
	}
}

func TestPDB_GetCodeHash_LocalCodeCacheHit(t *testing.T) {
	pdb, _, _ := newTestPDB(t, 0)
	addr := common.HexToAddress("0xc0de")
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xfd}

	pdb.SetCode(addr, code, tracing.CodeChangeUnspecified)
	want := crypto.Keccak256Hash(code)

	first := pdb.GetCodeHash(addr)
	if first != want {
		t.Fatalf("hash on first call = %x, want %x", first, want)
	}
	if got, ok := pdb.codeHashCache[addr]; !ok || got != want {
		t.Fatalf("codeHashCache[addr] after first call = (%x, %v), want (%x, true)", got, ok, want)
	}

	second := pdb.GetCodeHash(addr)
	if second != want {
		t.Fatalf("hash on second call = %x, want %x (should be served from cache)", second, want)
	}
}

// Code written by an earlier tx (read via MVStore) is memoized too.
func TestPDB_GetCodeHash_MVStoreCacheHit(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 5)
	addr := common.HexToAddress("0xc0de")
	code := []byte{0x60, 0x01, 0x60, 0x01}
	codeKey := blockstm.NewSubpathKey(addr, CodePath)
	store.WriteInc(codeKey, 2, 0, code)
	want := crypto.Keccak256Hash(code)

	first := pdb.GetCodeHash(addr)
	if first != want {
		t.Fatalf("hash on first call = %x, want %x", first, want)
	}
	if got, ok := pdb.codeHashCache[addr]; !ok || got != want {
		t.Fatalf("codeHashCache[addr] after first call = (%x, %v), want (%x, true)", got, ok, want)
	}

	second := pdb.GetCodeHash(addr)
	if second != want {
		t.Fatalf("hash on second call = %x, want %x (should be served from cache)", second, want)
	}
}

// A lone CreateAccount revert (no paired jkCode entry) must still drop the
// cached hash.
func TestPDB_GetCodeHash_RedundantCreateAccountRevertInvalidatesCache(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 5)
	addr := common.HexToAddress("0xf00d")

	priorCode := []byte{0x60, 0x01, 0x60, 0x01}
	codeKey := blockstm.NewSubpathKey(addr, CodePath)
	store.WriteInc(codeKey, 2, 0, priorCode) // an earlier tx's code at this address

	localCode := []byte{0x60, 0x02, 0x60, 0x02, 0x60, 0x02}
	pdb.SetCode(addr, localCode, tracing.CodeChangeUnspecified)
	if got, want := pdb.GetCodeHash(addr), crypto.Keccak256Hash(localCode); got != want {
		t.Fatalf("hash after SetCode = %x, want %x", got, want)
	}

	snap := pdb.Snapshot()
	pdb.CreateAccount(addr) // redundant re-create; solo jkCreate entry
	pdb.RevertToSnapshot(snap)

	if got, want := pdb.GetCodeHash(addr), crypto.Keccak256Hash(priorCode); got != want {
		t.Fatalf("hash after solo CreateAccount revert = %x, want %x (stale local-code hash leaking onto the earlier tx's code)", got, want)
	}
}
