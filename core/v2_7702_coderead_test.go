package core

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/triedb"
)

// TestV2_DelegateCodeReadConsistency reproduces the root of bor PR #2264 at the
// state layer: the EVM resolves a 7702-delegated account's code and hash via two
// separate GetCode(addr) reads (resolveCode + resolveCodeHash). Under blockstm v2
// there is no snapshot isolation, so a concurrent writer landing between the two
// reads makes them follow DIFFERENT delegation targets → the Contract gets a
// (Code, CodeHash) pair from different targets → poisons the shared jumpdest cache
// (panic "index out of range" in validJumpdest, or gas mismatch on reverts).
//
// The state-layer fix (alternative to changing the EVM): a per-tx code read cache,
// so repeated GetCode(addr) within one tx/incarnation return a consistent value —
// mirroring the existing balCache/committedCache/destructedCache. With the cache
// the two reads agree; without it they diverge and this test fails.
func TestV2_DelegateCodeReadConsistency(t *testing.T) {
	addrE := common.HexToAddress("0x000000000000000000000000000000000000e0a7") // delegated EOA
	target1 := common.HexToAddress("0x0000000000000000000000000000000000007001")
	target2 := common.HexToAddress("0x0000000000000000000000000000000000007002")
	designator := func(tgt common.Address) []byte {
		return append([]byte{0xef, 0x01, 0x00}, tgt.Bytes()...)
	}
	d1, d2 := designator(target1), designator(target2)

	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	base, err := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	sb := state.NewSafeBase(base, 0)
	store := blockstm.NewMVStore()
	bals := blockstm.NewMVBalanceStore()
	codeKey := blockstm.NewSubpathKey(addrE, state.CodePath)

	// A prior tx (idx 0) delegated E -> target1.
	store.WriteInc(codeKey, 0, 0, d1)

	r := state.NewParallelStateDB(5, sb, store, bals)
	r.EnableReadTracking()

	// EVM's resolveCode: first GetCode(E) reads the delegation designator.
	codeRead := r.GetCode(addrE)

	// A concurrent writer (tx 0 re-executing) rewrites E's delegation -> target2,
	// landing between the EVM's two resolution reads.
	store.WriteInc(codeKey, 0, 1, d2)

	// EVM's resolveCodeHash: second GetCode(E) reads the designator again.
	hashRead := r.GetCode(addrE)

	t.Logf("GetCode#1(resolveCode)=%x  GetCode#2(resolveCodeHash)=%x", codeRead, hashRead)
	if !bytes.Equal(codeRead, hashRead) {
		t.Fatalf("MISMATCH: two GetCode(E) reads within one tx returned different delegation "+
			"designators (%x vs %x). resolveCode and resolveCodeHash would build a Contract whose "+
			"Code and CodeHash point at different targets, poisoning the shared jumpdest cache. "+
			"Fix: a per-tx code read cache so repeated GetCode(addr) is snapshot-consistent.", codeRead, hashRead)
	}
}
