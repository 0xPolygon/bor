package state

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/triedb"
)

// These tests pin the read-validation contract for EIP-7702 authority
// accounts — accounts that an earlier SetCodeTx mutates (nonce + code) even
// though they are neither that tx's sender nor its call target. The failure
// mode they guard against was observed on mainnet (block 87714892): a later
// SetCodeTx sharing the same authority ran its authorization nonce/code check
// against the pre-block authority state and its authorization silently failed,
// diverging gas from the canonical block.
//
// The question these answer at the ParallelStateDB layer: once the earlier
// tx's authority write is committed to the store, does the later tx's
// Validate() reject the stale read so the executor re-runs it? If these pass,
// validation is sound and any escaping bad block lives above this layer (the
// executor / real EIP-7702 apply path); if they fail, the version/value
// validation has a hole for writes to non-sender / non-target accounts.

func newAuthorityBase(t *testing.T, authority common.Address, nonce uint64, code []byte) *SafeBase {
	t.Helper()
	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	sdb, err := New(types.EmptyRootHash, NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	// Base state must be populated before SafeBase snapshots the StateDB.
	sdb.SetNonce(authority, nonce, tracing.NonceChangeUnspecified)
	if len(code) != 0 {
		sdb.SetCode(authority, code, tracing.CodeChangeUnspecified)
	}
	return NewSafeBase(sdb, 2)
}

func TestPDB_AuthorityNonceStaleRead_LaterWriterInvalidates(t *testing.T) {
	authority := common.HexToAddress("0x92E2167c379664462C48F10f76a7A5ea26795cE7")
	const baseNonce = 5931
	const writerIdx = 31
	const readerIdx = 32
	const finalNonce = 5933

	base := newAuthorityBase(t, authority, baseNonce, nil)
	store := blockstm.NewMVStore()
	bals := blockstm.NewMVBalanceStore()

	reader := NewParallelStateDB(readerIdx, base, store, bals)
	reader.EnableReadTracking()

	// Later tx checks the authorization nonce before the earlier same-authority
	// tx has published its bump: it observes the stale base nonce and records it.
	if got := reader.GetNonce(authority); got != baseNonce {
		t.Fatalf("pre-write read: got %d, want %d", got, baseNonce)
	}

	// Earlier same-authority tx publishes the bumped nonce.
	store.WriteInc(blockstm.NewSubpathKey(authority, NoncePath), writerIdx, 0, uint64(finalNonce))

	if reader.Validate() {
		t.Fatal("Validate=true after the earlier same-authority tx published the authority nonce bump — " +
			"the later tx would settle a failed authorization while the canonical block applied it")
	}
}

func TestPDB_AuthorityCodeStaleRead_LaterWriterInvalidates(t *testing.T) {
	authority := common.HexToAddress("0x92E2167c379664462C48F10f76a7A5ea26795cE7")
	target := common.HexToAddress("0x55a0ab9e6182f274a9288469af41feb4438c24a7")
	const writerIdx = 31
	const readerIdx = 32

	// Base authority is a plain EOA (no delegation yet).
	base := newAuthorityBase(t, authority, 5931, nil)
	store := blockstm.NewMVStore()
	bals := blockstm.NewMVBalanceStore()

	reader := NewParallelStateDB(readerIdx, base, store, bals)
	reader.EnableReadTracking()

	if got := reader.GetCode(authority); len(got) != 0 {
		t.Fatalf("pre-write code read: got %x, want empty", got)
	}

	// Earlier tx installs the EIP-7702 delegation on the authority.
	store.WriteInc(blockstm.NewSubpathKey(authority, CodePath), writerIdx, 0, types.AddressToDelegation(target))

	if reader.Validate() {
		t.Fatal("Validate=true after the earlier same-authority tx installed the delegation code — " +
			"the later tx read empty code (no delegation) and its execution diverges from canonical")
	}
}

// TestPDB_DiagnoseStaleBaseReads pins the escape detector deployed for the
// pipelined-base investigation: it must report a base read only once a
// committed earlier-tx writer contradicts it by value, and never for a
// same-value writer or before any writer lands.
func TestPDB_DiagnoseStaleBaseReads(t *testing.T) {
	authority := common.HexToAddress("0x92E2167c379664462C48F10f76a7A5ea26795cE7")
	const baseNonce = 5931
	const writerIdx = 31
	const readerIdx = 32
	const finalNonce = 5933

	base := newAuthorityBase(t, authority, baseNonce, nil)
	store := blockstm.NewMVStore()
	bals := blockstm.NewMVBalanceStore()
	reader := NewParallelStateDB(readerIdx, base, store, bals)
	reader.EnableReadTracking()

	// Records a base read (writer=-1) of the authority nonce.
	reader.GetNonce(authority)

	if d := reader.DiagnoseStaleBaseReads(); len(d) != 0 {
		t.Fatalf("no writer committed yet — want 0 escapes, got %+v", d)
	}

	// Same-value earlier writer is harmless — must not be reported.
	nonceKey := blockstm.NewSubpathKey(authority, NoncePath)
	store.WriteInc(nonceKey, writerIdx, 0, uint64(baseNonce))
	if d := reader.DiagnoseStaleBaseReads(); len(d) != 0 {
		t.Fatalf("same-value writer — want 0 escapes, got %+v", d)
	}

	// Different-value earlier writer — the escape we hunt.
	store.WriteInc(nonceKey, writerIdx, 0, uint64(finalNonce))
	d := reader.DiagnoseStaleBaseReads()
	if len(d) != 1 {
		t.Fatalf("want exactly 1 escape, got %d: %+v", len(d), d)
	}
	got := d[0]
	if got.TxIndex != readerIdx || got.Category != "nonce" || got.Addr != authority || got.CurWriter != writerIdx {
		t.Fatalf("escape fields wrong: %+v", got)
	}
	if rv, ok := got.RecVal.(uint64); !ok || rv != baseNonce {
		t.Fatalf("RecVal = %v, want %d", got.RecVal, baseNonce)
	}
	if cv, ok := got.CurVal.(uint64); !ok || cv != finalNonce {
		t.Fatalf("CurVal = %v, want %d", got.CurVal, finalNonce)
	}
}
