package state

import (
	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
)

// parallelJournalEntry is a tagged union stored inline in a slice.
// Eliminates per-entry heap allocation from the interface-based approach.
type parallelJournalEntry struct {
	kind  uint8
	flags uint8          // had, isAdd, etc.
	addr  common.Address // 20 bytes
	key   common.Hash    // 32 bytes — storage/transient key, access list slot
	prev  common.Hash    // 32 bytes — storage/transient prev value
	amt   uint256.Int    // 32 bytes — balance amount
	prevU uint64         // nonce prev, refund prev, log prevLen
	code  []byte         // only for code changes (rare)
}

const (
	jkNonce uint8 = iota
	jkStorage
	jkBalance
	jkRefund
	jkLog
	jkCreate
	jkDestruct
	jkCode
	jkTransient
	jkAccessAddr
	jkAccessSlot
)

func (j *parallelJournalEntry) revert(s *ParallelStateDB) {
	switch j.kind {
	case jkNonce:
		j.revertNonce(s)
	case jkStorage:
		j.revertStorage(s)
	case jkBalance:
		j.revertBalance(s)
	case jkRefund:
		s.refund = j.prevU
	case jkLog:
		s.logs = s.logs[:j.prevU]
		s.logSize = uint(j.prevU)
	case jkCreate:
		j.revertCreate(s)
	case jkDestruct:
		delete(s.destructed, j.addr)
	case jkCode:
		j.revertCode(s)
	case jkTransient:
		s.transientStorage.Set(j.addr, j.key, j.prev)
	case jkAccessAddr:
		delete(s.accessList.addresses, j.addr)
	case jkAccessSlot:
		j.revertAccessSlot(s)
	}
}

func (j *parallelJournalEntry) revertNonce(s *ParallelStateDB) {
	if j.flags&1 != 0 { // had a previous value
		s.localNonces[j.addr] = j.prevU
	} else {
		delete(s.localNonces, j.addr)
	}
}

func (j *parallelJournalEntry) revertStorage(s *ParallelStateDB) {
	if j.flags&1 != 0 { // had a previous value
		s.localStorage[j.addr][j.key] = j.prev
	} else {
		delete(s.localStorage[j.addr], j.key)
	}
}

func (j *parallelJournalEntry) revertBalance(s *ParallelStateDB) {
	if j.flags&1 != 0 { // isAdd
		if s.localBalAdd[j.addr] != nil {
			s.localBalAdd[j.addr].Sub(s.localBalAdd[j.addr], &j.amt)
		}
		return
	}
	if s.localBalSub[j.addr] != nil {
		s.localBalSub[j.addr].Sub(s.localBalSub[j.addr], &j.amt)
	}
}

func (j *parallelJournalEntry) revertCreate(s *ParallelStateDB) {
	// Keeps codeHashCache consistent with localCode even if a future caller
	// reverts a CreateAccount without a matching jkCode entry.
	delete(s.created, j.addr)
	delete(s.newContract, j.addr)
	delete(s.localCode, j.addr)
	delete(s.codeHashCache, j.addr)
	if s.DeferMVWrites {
		return
	}
	s.store.Delete(blockstm.NewSubpathKey(j.addr, CreatePath), s.TxIndex)
	s.store.Delete(blockstm.NewSubpathKey(j.addr, CodePath), s.TxIndex)
}

func (j *parallelJournalEntry) revertCode(s *ParallelStateDB) {
	hadPrev := j.flags&1 != 0
	if hadPrev {
		s.localCode[j.addr] = j.code
	} else {
		delete(s.localCode, j.addr)
	}
	delete(s.codeHashCache, j.addr)
	if s.DeferMVWrites {
		return
	}
	codeKey := blockstm.NewSubpathKey(j.addr, CodePath)
	if hadPrev {
		s.store.WriteInc(codeKey, s.TxIndex, s.Incarnation, j.code)
	} else {
		s.store.Delete(codeKey, s.TxIndex)
	}
}

func (j *parallelJournalEntry) revertAccessSlot(s *ParallelStateDB) {
	idx, ok := s.accessList.addresses[j.addr]
	if ok && idx >= 0 && idx < len(s.accessList.slots) {
		delete(s.accessList.slots[idx], j.key)
	}
}

// journalChargeBatch keeps the shared WriteBudget counter off the per-opcode
// path; in-flight incarnations can each overshoot the bound by less than a batch.
const journalChargeBatch = 256

func (s *ParallelStateDB) appendJournal(e parallelJournalEntry) {
	s.journalEntries = append(s.journalEntries, e)
	s.chargeEntry()
}

func (s *ParallelStateDB) chargeEntry() {
	if s.WriteBudget == nil {
		return
	}
	s.unchargedEntries++
	if s.unchargedEntries == journalChargeBatch {
		s.WriteBudget.Charge(journalChargeBatch)
		s.unchargedEntries = 0
	}
}

// ChargePendingWrites charges the entries of a partial batch, so an
// incarnation's whole journal is accounted once it finishes.
func (s *ParallelStateDB) ChargePendingWrites() {
	if s.WriteBudget == nil {
		return
	}
	s.WriteBudget.Charge(s.unchargedEntries)
	s.unchargedEntries = 0
}

// budgetBytesPerEntry charges log data and preimages, which a tx can make
// large for little gas, at one entry per this many bytes: about the size of
// a journal entry, and far fewer entries per gas than the densest ops.
const budgetBytesPerEntry = 128

func (s *ParallelStateDB) chargeBytes(n int) {
	if s.WriteBudget != nil && n >= budgetBytesPerEntry {
		s.WriteBudget.Charge(int64(n / budgetBytesPerEntry))
	}
}
