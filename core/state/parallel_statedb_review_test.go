package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/crypto"
)

// ---------------------------------------------------------------------------
// Fix #1: GetCodeHash MVStore branch must use ESTIMATE/PROVISIONAL-aware
// readStoreWait and record the read for validation.
// ---------------------------------------------------------------------------

// TestPDB_GetCodeHash_FromMVStore_RecordsRead verifies that reading code
// hash from a prior tx's MVStore entry is tracked in StoreReads so
// validation can catch a stale value if the writer is later invalidated.
func TestPDB_GetCodeHash_FromMVStore_RecordsRead(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 5)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0x1")
	code := []byte{0x60, 0x01, 0x60, 0x02}
	codeKey := blockstm.NewSubpathKey(addr, CodePath)
	store.WriteInc(codeKey, 2, 0, code)

	got := pdb.GetCodeHash(addr)
	if want := crypto.Keccak256Hash(code); got != want {
		t.Fatalf("hash mismatch: got %s want %s", got.Hex(), want.Hex())
	}

	// The CodePath read must appear in StoreReads with the correct writer
	// info. GetCodeHash also records a SuicidePath read (Fix #2) so the
	// addressable read set has more than one entry; we just look up the
	// CodePath one.
	var rd *StoreReadDesc
	for i := range pdb.StoreReads {
		if pdb.StoreReads[i].Key == codeKey {
			rd = &pdb.StoreReads[i]
			break
		}
	}
	if rd == nil {
		t.Fatalf("CodePath read not recorded; got reads=%v", pdb.StoreReads)
	}
	if rd.WriterIdx != 2 || rd.WriterInc != 0 {
		t.Fatalf("recorded writer mismatch: got (%d,%d) want (2,0)", rd.WriterIdx, rd.WriterInc)
	}
}

// TestPDB_GetCodeHash_FromBase_RecordsBaseRead verifies that when no
// MVStore writer exists, the base read is still recorded with
// WriterIdx=-1 so a writer that appears later (via re-execution of an
// earlier tx) is caught at validation.
func TestPDB_GetCodeHash_FromBase_RecordsBaseRead(t *testing.T) {
	pdb, _, _ := newTestPDB(t, 5)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0xdead") // not in base, no MVStore entry

	_ = pdb.GetCodeHash(addr)

	// At least one StoreRead should track CodePath with WriterIdx=-1.
	codeKey := blockstm.NewSubpathKey(addr, CodePath)
	found := false
	for _, rd := range pdb.StoreReads {
		if rd.Key == codeKey && rd.WriterIdx == -1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected CodePath base read recorded; got reads=%v", pdb.StoreReads)
	}
}

// TestPDB_GetCodeHash_VFailsOnLateWriter verifies that a base read of
// GetCodeHash now triggers a validation failure when an earlier tx
// publishes new code for the same address — exactly the case the old
// untracked Read() bypassed.
func TestPDB_GetCodeHash_VFailsOnLateWriter(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 5)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0xbeef")

	// First read: nothing in MVStore. Records base read.
	_ = pdb.GetCodeHash(addr)
	if !pdb.Validate() {
		t.Fatal("validate must pass before a writer appears")
	}

	// Now an earlier tx publishes code for this address.
	codeKey := blockstm.NewSubpathKey(addr, CodePath)
	store.WriteInc(codeKey, 3, 0, []byte{0x01, 0x02, 0x03})

	// Validation must now fail with category "code".
	res := pdb.ValidateDetailed()
	if res.Valid {
		t.Fatal("expected validation failure after writer appears for previously-empty CodePath")
	}
	if res.FailKey != "code" {
		t.Fatalf("expected FailKey=code, got %q", res.FailKey)
	}
}

// TestPDB_GetCodeHash_FiltersEstimate verifies that GetCodeHash does NOT
// return a stale value when the only matching MVStore entry is marked
// ESTIMATE. With Incarnation=0 (and no WaitForFinal), the read should
// fall through to base — never expose the stale ESTIMATE bytes.
func TestPDB_GetCodeHash_FiltersEstimate(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 5)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0xfeed")
	staleCode := []byte{0xfe, 0xfe, 0xfe}
	codeKey := blockstm.NewSubpathKey(addr, CodePath)
	// Writer at tx 2, then mark its entry ESTIMATE (re-execution in flight).
	store.WriteInc(codeKey, 2, 0, staleCode)
	store.MarkEstimate(2, []blockstm.Key{codeKey})

	got := pdb.GetCodeHash(addr)
	if got == crypto.Keccak256Hash(staleCode) {
		t.Fatal("GetCodeHash returned the stale ESTIMATE-flagged value")
	}

	// The CodePath read must be recorded as a base read, since ESTIMATE
	// was filtered. Other reads (from Exist's createKey / balance fallback)
	// can also appear; we only constrain the codeKey read here.
	foundCodeRead := false
	for _, rd := range pdb.StoreReads {
		if rd.Key == codeKey {
			if rd.WriterIdx != -1 {
				t.Fatalf("CodePath read: WriterIdx=%d, want -1 (ESTIMATE must not surface as a writer)", rd.WriterIdx)
			}
			foundCodeRead = true
			break
		}
	}
	if !foundCodeRead {
		t.Fatal("expected CodePath read to be recorded")
	}
}

// ---------------------------------------------------------------------------
// Fix #2: FlushToMVStore must skip when the PDB panicked, otherwise it
// pollutes the shared store with partial state and starts a vfail cascade.
// ---------------------------------------------------------------------------

// TestPDB_FlushToMVStore_SkipsWhenPanicked verifies that no local state
// is written to MVStore or MVBalanceStore when Panicked=true, even when
// the PDB has accumulated nonce / storage / code / created / balance
// changes prior to the panic.
//
// The PDB is configured with DeferMVWrites=true to match V2 production —
// in deferred mode SetCode/CreateAccount don't write eagerly, so the
// only path to MVStore is FlushToMVStore, which is what we're guarding.
func TestPDB_FlushToMVStore_SkipsWhenPanicked(t *testing.T) {
	pdb, store, bals := newTestPDB(t, 7)
	pdb.SetDeferMVWrites(true)
	addr := common.HexToAddress("0x9999")

	// Accumulate every flavor of pending write.
	pdb.SetNonce(addr, 5, tracing.NonceChangeUnspecified)
	pdb.SetState(addr, common.HexToHash("0x01"), common.HexToHash("0x02"))
	pdb.SetCode(addr, []byte{0xab, 0xcd}, tracing.CodeChangeUnspecified)
	pdb.AddBalance(addr, uint256.NewInt(11), tracing.BalanceChangeUnspecified)
	pdb.SubBalance(addr, uint256.NewInt(3), tracing.BalanceChangeUnspecified)

	// Simulate a panic mid-execution.
	pdb.Panicked = true
	pdb.FlushToMVStore()

	// MVStore: no entries should exist for any of the keys we touched.
	keys := []blockstm.Key{
		blockstm.NewSubpathKey(addr, NoncePath),
		blockstm.NewStateKey(addr, common.HexToHash("0x01")),
		blockstm.NewSubpathKey(addr, CodePath),
		blockstm.NewSubpathKey(addr, CreatePath), // CreateAccount via SetCode
	}
	for _, k := range keys {
		if val, _, _, found, _ := store.ReadVersionFull(k, 8); found {
			t.Fatalf("MVStore polluted with key %x: %v", k, val)
		}
	}
	// MVBalanceStore: no delta should exist for the address.
	if _, _, found := bals.GetTxDelta(addr, 7); found {
		t.Fatal("MVBalanceStore polluted with balance delta from panicked tx")
	}
}

// TestPDB_FlushToMVStore_NotPanickedStillFlushes is the negative control:
// the same setup without Panicked must still flush normally. Guards
// against a regression that turns FlushToMVStore into a no-op.
func TestPDB_FlushToMVStore_NotPanickedStillFlushes(t *testing.T) {
	pdb, store, bals := newTestPDB(t, 7)
	addr := common.HexToAddress("0x1234")
	pdb.SetNonce(addr, 5, tracing.NonceChangeUnspecified)
	pdb.AddBalance(addr, uint256.NewInt(11), tracing.BalanceChangeUnspecified)

	pdb.FlushToMVStore()

	if _, _, _, found, _ := store.ReadVersionFull(blockstm.NewSubpathKey(addr, NoncePath), 8); !found {
		t.Fatal("expected nonce flushed to MVStore")
	}
	if _, _, found := bals.GetTxDelta(addr, 7); !found {
		t.Fatal("expected balance delta flushed to MVBalanceStore")
	}
}

// ---------------------------------------------------------------------------
// Fix #4: diagnoseBalanceRead must mirror validateBalanceRead — including
// for the coinbase address. The earlier asymmetric coinbase skip caused
// validation diagnostics to under-report balance vfails.
// ---------------------------------------------------------------------------

// TestPDB_DiagnoseValidation_ReportsCoinbaseBalanceFail pins the parity:
// when validateBalanceRead vfails on the coinbase, DiagnoseValidation must
// emit a matching diag entry instead of silently returning none.
func TestPDB_DiagnoseValidation_ReportsCoinbaseBalanceFail(t *testing.T) {
	pdb, _, bals := newTestPDB(t, 5)
	pdb.EnableReadTracking()
	coinbase := common.HexToAddress("0xCB")
	pdb.Coinbase = coinbase

	// Tx 5 reads coinbase balance: priorBalanceDeltas returns (0,0).
	_ = pdb.GetBalance(coinbase)
	if !pdb.Validate() {
		t.Fatal("Validate must pass before any prior writer commits a delta")
	}

	// An earlier tx commits a fee bump on the coinbase.
	bals.WriteDelta(coinbase, 3, uint256New(100), nil)

	if pdb.Validate() {
		t.Fatal("Validate must fail once coinbase delta drifts from recorded read")
	}
	diags := pdb.DiagnoseValidation()
	gotCoinbase := false
	for _, d := range diags {
		if d.Category == "balance" && d.Addr == coinbase {
			gotCoinbase = true
			break
		}
	}
	if !gotCoinbase {
		t.Fatalf("DiagnoseValidation must report coinbase balance vfail; got %#v", diags)
	}
}
