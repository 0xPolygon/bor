package state

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/crypto"
)

// This file pins the two structural invariants that keep V2 reads safe
// under concurrency. Every intra-tx read must be EITHER:
//
//	(1) snapshot-stable — repeated reads of the same key within one tx
//	    incarnation return one value even if a concurrent writer lands
//	    between them, so derived artifacts that escape re-execution (the
//	    shared jumpdest cache, keyed by codehash) can't be poisoned; or
//	(2) validation-visible — the read is recorded so that if the value it
//	    observed is later contradicted by a prior tx's write, the tx fails
//	    validation and re-executes.
//
// The three V2 divergences fixed on this branch each violated one of
// these: the 7702 code double-read was neither stable nor self-consistent
// (invariant 1), and Exist's missing nonce check read state it never
// recorded (invariant 2). A read that satisfies neither is a latent
// consensus bug, so both are asserted generically here.

func delegationDesignator(target common.Address) []byte {
	return append([]byte{0xef, 0x01, 0x00}, target.Bytes()...)
}

// TestParallelCodeReadSnapshotConsistency is invariant (1) for CodePath:
// within one tx, GetCode and GetCodeHash must observe one consistent value
// even when a concurrent writer rewrites the address's delegation between
// the calls. This is the exact EVM sequence behind the fix — resolveCode
// reads the code, resolveCodeHash reads it again — and the failure it
// prevents is a Contract whose Code and CodeHash come from different
// delegation targets, poisoning the shared jumpdest cache.
func TestParallelCodeReadSnapshotConsistency(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 5)
	pdb.EnableReadTracking()

	addr := common.HexToAddress("0x00000000000000000000000000000000000000e0")
	d1 := delegationDesignator(common.HexToAddress("0x7001"))
	d2 := delegationDesignator(common.HexToAddress("0x7002"))
	codeKey := blockstm.NewSubpathKey(addr, CodePath)

	// A prior tx delegated addr -> target1.
	store.WriteInc(codeKey, 0, 0, d1)

	code1 := pdb.GetCode(addr) // resolveCode's read

	// A concurrent re-exec of the prior tx redirects the delegation,
	// landing between the EVM's two resolution reads.
	store.WriteInc(codeKey, 0, 1, d2)

	hash := pdb.GetCodeHash(addr) // resolveCodeHash's read
	code2 := pdb.GetCode(addr)

	if !bytes.Equal(code1, code2) {
		t.Errorf("GetCode not snapshot-stable: first=%x second=%x", code1, code2)
	}
	if want := crypto.Keccak256Hash(code1); hash != want {
		t.Errorf("GetCodeHash disagrees with GetCode within one tx: hash=%s keccak(code)=%s\n"+
			"a Contract built from this (Code, CodeHash) pair points at two different "+
			"delegation targets and poisons the shared jumpdest cache", hash.Hex(), want.Hex())
	}
}

// TestParallelReadRecordingCompleteness is invariant (2): for every read
// API, after the read observes a value, a prior tx writing a different
// value on the same path must make the tx fail validation. A read that
// leaves no record is invisible to the conflict detector, so a stale
// observation is silently accepted — exactly how Exist's missing nonce
// read shipped EIP-7702 over-charges as bad blocks rather than triggering
// a re-execution.
func TestParallelReadRecordingCompleteness(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000e0")
	slot := common.HexToHash("0x01")

	cases := []struct {
		name string
		read func(*ParallelStateDB)
		// write applies a prior-tx (idx 2) write that contradicts what the
		// read observed on an empty base.
		write func(*blockstm.MVStore, *blockstm.MVBalanceStore)
	}{
		{
			name: "Exist", // the fix #2 safety-net gap
			read: func(p *ParallelStateDB) { p.Exist(addr) },
			write: func(s *blockstm.MVStore, _ *blockstm.MVBalanceStore) {
				s.WriteInc(blockstm.NewSubpathKey(addr, NoncePath), 2, 0, uint64(7))
			},
		},
		{
			name: "Empty",
			read: func(p *ParallelStateDB) { p.Empty(addr) },
			write: func(s *blockstm.MVStore, _ *blockstm.MVBalanceStore) {
				s.WriteInc(blockstm.NewSubpathKey(addr, NoncePath), 2, 0, uint64(7))
			},
		},
		{
			name: "GetNonce",
			read: func(p *ParallelStateDB) { p.GetNonce(addr) },
			write: func(s *blockstm.MVStore, _ *blockstm.MVBalanceStore) {
				s.WriteInc(blockstm.NewSubpathKey(addr, NoncePath), 2, 0, uint64(7))
			},
		},
		{
			name: "GetCode",
			read: func(p *ParallelStateDB) { p.GetCode(addr) },
			write: func(s *blockstm.MVStore, _ *blockstm.MVBalanceStore) {
				s.WriteInc(blockstm.NewSubpathKey(addr, CodePath), 2, 0, []byte{0x60, 0x00})
			},
		},
		{
			name: "GetCodeHash",
			read: func(p *ParallelStateDB) { p.GetCodeHash(addr) },
			write: func(s *blockstm.MVStore, _ *blockstm.MVBalanceStore) {
				s.WriteInc(blockstm.NewSubpathKey(addr, CodePath), 2, 0, []byte{0x60, 0x00})
			},
		},
		{
			name: "GetState",
			read: func(p *ParallelStateDB) { p.GetState(addr, slot) },
			write: func(s *blockstm.MVStore, _ *blockstm.MVBalanceStore) {
				s.WriteInc(blockstm.NewStateKey(addr, slot), 2, 0, common.HexToHash("0x2a"))
			},
		},
		{
			name: "GetBalance",
			read: func(p *ParallelStateDB) { p.GetBalance(addr) },
			write: func(_ *blockstm.MVStore, b *blockstm.MVBalanceStore) {
				b.WriteDelta(addr, 2, uint256.NewInt(13), uint256.NewInt(0))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pdb, store, bals := newTestPDB(t, 5)
			pdb.EnableReadTracking()

			tc.read(pdb)
			tc.write(store, bals)

			if res := pdb.ValidateDetailed(); res.Valid {
				t.Fatalf("%s left no validation-visible record: a prior-tx write that "+
					"changed the observed value did not invalidate the tx, so a stale read "+
					"would be silently accepted", tc.name)
			}
		})
	}
}
