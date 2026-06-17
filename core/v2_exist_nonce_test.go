package core

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/triedb"
)

// TestV2_ExistMissesPriorTxNonce is the deterministic root cause of the
// block-77703134 / 77794337 bad blocks (both EIP-7702, both V2 over-charge,
// both engine_validation=0).
//
// Serial StateDB.Exist(addr) == (getStateObject != nil); an account with
// nonce>0 exists. ParallelStateDB.Exist checks own-create, CreatePath, suicide,
// balance, and base — but NEVER the nonce. SetNonce writes only NoncePath (no
// CreateAccount). So an account made to exist by a PRIOR in-block tx's nonce
// bump (a sender increment, or a 7702 delegation clear) is seen as existing by
// serial but non-existent by V2.
//
// Consequence: applyAuthorization gates a 12500-gas refund on
// st.state.Exist(authority); V2 wrongly skips it -> over-charge -> bad block.
// Invisible to validation because Exist never reads/records the nonce.
func TestV2_ExistMissesPriorTxNonce(t *testing.T) {
	e := common.HexToAddress("0x00000000000000000000000000000000000000e0")

	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	db := state.NewDatabase(tdb, nil)
	// base: E does NOT exist pre-block.
	bsdb, err := state.New(types.EmptyRootHash, db)
	if err != nil {
		t.Fatal(err)
	}
	root, err := bsdb.Commit(0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := tdb.Commit(root, false); err != nil {
		t.Fatal(err)
	}

	// --- serial: a prior tx bumps E's nonce (no balance, no code) ---
	serial, err := state.New(root, db)
	if err != nil {
		t.Fatal(err)
	}
	serial.SetNonce(e, 5, tracing.NonceChangeEoACall)
	serial.Finalise(true)
	serialExist := serial.Exist(e)

	// --- V2: prior tx (idx 0) bumps E's nonce via the MVStore ---
	parBase, err := state.New(root, db)
	if err != nil {
		t.Fatal(err)
	}
	sb := state.NewSafeBase(parBase, 2)
	store := blockstm.NewMVStore()
	bals := blockstm.NewMVBalanceStore()

	writer := state.NewParallelStateDB(0, sb, store, bals)
	writer.EnableReadTracking()
	writer.SetNonce(e, 5, tracing.NonceChangeEoACall)
	writer.FlushToMVStore() // publish NoncePath to MVStore (no CreatePath written)

	reader := state.NewParallelStateDB(1, sb, store, bals)
	reader.EnableReadTracking()
	v2Nonce := reader.GetNonce(e) // proves the nonce IS present and readable
	v2Exist := reader.Exist(e)

	t.Logf("E via prior-tx nonce-only: serial.Exist=%v  v2.GetNonce=%d  v2.Exist=%v",
		serialExist, v2Nonce, v2Exist)

	if v2Nonce != 5 {
		t.Fatalf("sanity: expected V2 GetNonce(E)=5 (prior-tx nonce visible), got %d", v2Nonce)
	}
	if v2Exist != serialExist {
		t.Fatalf("ROOT CAUSE REPRODUCED: ParallelStateDB.Exist=%v but serial StateDB.Exist=%v "+
			"for an account existing via prior-tx nonce-only (nonce=%d). Exist ignores the nonce, "+
			"so EIP-7702 applyAuthorization's Exist-gated 12500 refund diverges.", v2Exist, serialExist, v2Nonce)
	}
}
