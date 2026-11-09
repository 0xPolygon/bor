package state

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/types"
)

func writeSlots(pdb *ParallelStateDB, n int) {
	addr := common.HexToAddress("0xc0ffee")
	for i := 0; i < n; i++ {
		pdb.SetState(addr, common.Hash{}, common.BigToHash(common.Big1))
	}
}

func TestPDBJournalChargesWriteBudget(t *testing.T) {
	for _, tc := range []struct {
		name            string
		writes          int
		limit           int64
		wantBeforeFlush bool
		wantAfterFlush  bool
	}{
		{"partial batch only charged on flush", journalChargeBatch - 1, 0, false, true},
		{"full batch charged immediately", journalChargeBatch, journalChargeBatch - 1, true, true},
		{"full batch at limit", journalChargeBatch, journalChargeBatch, false, false},
		{"remainder over limit", journalChargeBatch + 44, journalChargeBatch + 43, false, true},
		{"remainder at limit", journalChargeBatch + 44, journalChargeBatch + 44, false, false},
		{"several batches", 3*journalChargeBatch + 1, 2 * journalChargeBatch, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdb, _, _ := newTestPDB(t, 0)
			pdb.WriteBudget = blockstm.NewWriteBudget(tc.limit, func() {})
			writeSlots(pdb, tc.writes)
			if got := pdb.WriteBudget.Exhausted(); got != tc.wantBeforeFlush {
				t.Errorf("before flush: exhausted = %v, want %v", got, tc.wantBeforeFlush)
			}
			pdb.ChargePendingWrites()
			if got := pdb.WriteBudget.Exhausted(); got != tc.wantAfterFlush {
				t.Errorf("after flush: exhausted = %v, want %v", got, tc.wantAfterFlush)
			}
			if len(pdb.journalEntries) != tc.writes {
				t.Errorf("journal has %d entries, want %d", len(pdb.journalEntries), tc.writes)
			}
		})
	}
}

func TestPDBChargePendingWritesIsIdempotent(t *testing.T) {
	pdb, _, _ := newTestPDB(t, 0)
	pdb.WriteBudget = blockstm.NewWriteBudget(10, func() {})
	writeSlots(pdb, 10)
	pdb.ChargePendingWrites()
	pdb.ChargePendingWrites()
	if pdb.WriteBudget.Exhausted() {
		t.Fatal("a second flush charged the same entries again")
	}
}

func TestPDBWithoutWriteBudget(t *testing.T) {
	pdb, _, _ := newTestPDB(t, 0)
	writeSlots(pdb, journalChargeBatch+1)
	pdb.ChargePendingWrites()
	if len(pdb.journalEntries) != journalChargeBatch+1 {
		t.Fatalf("journal has %d entries, want %d", len(pdb.journalEntries), journalChargeBatch+1)
	}
}

func TestPDBResetDropsWriteBudget(t *testing.T) {
	pdb, store, bals := newTestPDB(t, 0)
	old := blockstm.NewWriteBudget(0, func() {})
	pdb.WriteBudget = old
	writeSlots(pdb, journalChargeBatch-1)

	pdb.Reset(1, pdb.base, store, bals)
	if pdb.WriteBudget != nil {
		t.Fatal("Reset kept the previous incarnation's budget")
	}
	writeSlots(pdb, 2*journalChargeBatch)
	pdb.ChargePendingWrites()
	if old.Exhausted() {
		t.Fatal("writes after Reset were charged to the previous budget")
	}

	pdb.WriteBudget = blockstm.NewWriteBudget(1<<20, func() {})
	writeSlots(pdb, journalChargeBatch-1)
	fresh := blockstm.NewWriteBudget(0, func() {})
	pdb.Reset(2, pdb.base, store, bals)
	pdb.WriteBudget = fresh
	pdb.ChargePendingWrites()
	if fresh.Exhausted() {
		t.Fatal("Reset carried uncharged entries into the next incarnation")
	}
}

func TestPDBJournalingOpsChargeWriteBudget(t *testing.T) {
	addr := common.HexToAddress("0xc0ffee")
	slot := common.HexToHash("0x01")
	for _, tc := range []struct {
		name    string
		op      func(*ParallelStateDB)
		entries int
	}{
		{"AddRefund", func(s *ParallelStateDB) { s.AddRefund(1) }, 1},
		{"SubRefund", func(s *ParallelStateDB) { s.refund = 1; s.SubRefund(1) }, 1},
		{"AddAddressToAccessList", func(s *ParallelStateDB) { s.AddAddressToAccessList(addr) }, 1},
		{"AddSlotToAccessList new address", func(s *ParallelStateDB) { s.AddSlotToAccessList(addr, slot) }, 2},
		{"AddSlotToAccessList known address", func(s *ParallelStateDB) {
			s.accessList.AddAddress(addr)
			s.AddSlotToAccessList(addr, slot)
		}, 1},
		{"AddLog", func(s *ParallelStateDB) { s.AddLog(&types.Log{}) }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdb, _, _ := newTestPDB(t, 0)
			pdb.WriteBudget = blockstm.NewWriteBudget(int64(tc.entries-1), func() {})
			tc.op(pdb)
			pdb.ChargePendingWrites()
			if len(pdb.journalEntries) != tc.entries {
				t.Fatalf("journal has %d entries, want %d", len(pdb.journalEntries), tc.entries)
			}
			if !pdb.WriteBudget.Exhausted() {
				t.Fatalf("%d journal entries were not charged to the write budget", tc.entries)
			}
		})
	}
}

func TestPDBStoreReadsChargeWriteBudget(t *testing.T) {
	addr := common.HexToAddress("0xc0ffee")
	for _, tc := range []struct {
		name string
		read func(*ParallelStateDB, int)
	}{
		{"GetState", func(s *ParallelStateDB, i int) { s.GetState(addr, common.BigToHash(big.NewInt(int64(i)))) }},
		{"GetNonce", func(s *ParallelStateDB, i int) { s.GetNonce(common.BigToAddress(big.NewInt(int64(i + 1)))) }},
		{"GetCode", func(s *ParallelStateDB, i int) { s.GetCode(common.BigToAddress(big.NewInt(int64(i + 1)))) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdb, _, _ := newTestPDB(t, 0)
			pdb.EnableReadTracking()
			pdb.WriteBudget = blockstm.NewWriteBudget(journalChargeBatch, func() {})
			for i := 0; i < journalChargeBatch+44; i++ {
				tc.read(pdb, i)
			}
			pdb.ChargePendingWrites()
			if len(pdb.StoreReads) <= journalChargeBatch {
				t.Fatalf("only %d store reads tracked", len(pdb.StoreReads))
			}
			if !pdb.WriteBudget.Exhausted() {
				t.Fatalf("%d tracked reads were not charged to the budget", len(pdb.StoreReads))
			}
		})
	}
}

func TestPDBSnapshotsChargeWriteBudget(t *testing.T) {
	pdb, _, _ := newTestPDB(t, 0)
	pdb.WriteBudget = blockstm.NewWriteBudget(journalChargeBatch, func() {})
	for i := 0; i <= journalChargeBatch; i++ {
		pdb.Snapshot()
	}
	pdb.ChargePendingWrites()
	if !pdb.WriteBudget.Exhausted() {
		t.Fatalf("%d snapshots were not charged to the budget", journalChargeBatch+1)
	}
}

func TestPDBPayloadBytesChargeWriteBudget(t *testing.T) {
	log := func(n int) func(*ParallelStateDB) {
		return func(s *ParallelStateDB) { s.AddLog(&types.Log{Data: make([]byte, n)}) }
	}
	preimage := func(n int) func(*ParallelStateDB) {
		return func(s *ParallelStateDB) { s.AddPreimage(common.Hash{byte(n)}, make([]byte, n)) }
	}
	for _, tc := range []struct {
		name    string
		op      func(*ParallelStateDB)
		entries int64
	}{
		{"log without data", log(0), 1},
		{"log under one unit", log(budgetBytesPerEntry - 1), 1},
		{"log of one unit", log(budgetBytesPerEntry), 2},
		{"log of 1000 bytes", log(1000), 8},
		{"empty preimage", preimage(0), 1},
		{"preimage of one unit", preimage(budgetBytesPerEntry), 2},
		{"preimage of 2KiB", preimage(2048), 17},
		{"preimage recorded once", func(s *ParallelStateDB) {
			s.AddPreimage(common.Hash{1}, nil)
			s.AddPreimage(common.Hash{1}, make([]byte, 2048))
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdb, _, _ := newTestPDB(t, 0)
			pdb.WriteBudget = blockstm.NewWriteBudget(1<<20, func() {})
			tc.op(pdb)
			pdb.ChargePendingWrites()
			if got := 1<<20 - pdb.WriteBudget.Remaining(); got != tc.entries {
				t.Fatalf("charged %d entries, want %d", got, tc.entries)
			}
		})
	}
}
