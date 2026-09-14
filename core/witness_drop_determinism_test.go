package core

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

// The invariant this file defends:
//
//	A block's witness must be a function of the transactions the block
//	CONTAINS, never of what the producer attempted along the way.
//
// miner/worker.go's commitTransactions attempts a transaction, and on several
// paths rolls the state back and drops it:
//
//	vm.ErrInterrupt      -> txs.Pop()    (build deadline fired mid-EVM)
//	core.ErrNonceTooLow  -> txs.Shift()  (pool/miner race)
//	default              -> txs.Pop()    (otherwise invalid)
//
// commitTransaction restores state with RevertToSnapshot, but the witness has
// no journal: AddCode fires at read time, and the reads schedule trie-prefetch
// work whose resolved paths IntermediateRoot later harvests. So the discarded
// transaction leaves its state in the witness of a block that does not contain
// it -- and for the interrupt path, how much it leaves depends on where the
// wall clock cut it.
//
// The consequence is not cosmetic. The BP signs the witness the miner produced
// (eth/handler_wit2.go signs over the locally stored bytes, and the miner's
// witness is what writeTaskBlock stores), while every importing node computes
// the witness from the block's own transactions. If the two differ, no importer
// can reproduce the BP-signed hash, and an importer serving its own -- correct
// -- bytes is struck for a byte mismatch.

// wdDropMode is how the producer abandons an attempted transaction.
type wdDropMode int

const (
	// wdDropInterrupt kills the transaction mid-EVM after a set number of
	// opcodes, the vm.ErrInterrupt path.
	wdDropInterrupt wdDropMode = iota
	// wdDropPreCheck fails the transaction before the EVM runs, the
	// nonce/validity paths.
	wdDropPreCheck
)

// wdGhost is a transaction the producer attempts at position `at` in the build
// and then drops.
type wdGhost struct {
	at       int
	tx       *types.Transaction
	mode     wdDropMode
	afterOps int
}

// wdBuild replays the fixture the way miner/worker.go assembles a block, with
// the given ghosts attempted and dropped along the way.
//
// It follows the production sequence exactly:
//   - vm.Config carries an interrupt flag checked on every opcode
//     (core/vm/interpreter.go:213, interpreter_dispatch.go:50)
//   - tripping it makes the EVM return vm.ErrInterrupt, which
//     core/state_processor.go:275 surfaces as an ApplyTransaction error
//   - miner/worker.go:1644 commitTransaction calls RevertToSnapshot and
//     restores the gas pool
//   - miner/worker.go:1905 matches the error and calls txs.Pop(), so the
//     transaction never enters the block
func (f *witnessDropFixture) wdBuild(t *testing.T, ghosts []wdGhost) *stateless.Witness {
	t.Helper()

	db := state.NewDatabase(f.tdb, nil)
	_, processReader, _, err := db.ReadersWithCacheStatsTriple(f.root)
	if err != nil {
		t.Fatalf("readers: %v", err)
	}
	sdb, err := state.NewWithReader(f.root, db, processReader)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	w, err := stateless.NewWitness(f.header, nil)
	if err != nil {
		t.Fatalf("new witness: %v", err)
	}
	sdb.StartPrefetcher("miner", w, nil)
	defer sdb.StopPrefetcher()

	gp := new(GasPool).AddGas(f.blockCtx.GasLimit)
	var usedGas uint64
	plain := vm.NewEVM(f.blockCtx, sdb, f.config, vm.Config{})

	attempt := func(g wdGhost, idx int) {
		snap := sdb.Snapshot()
		gasBefore := gp.Gas()
		usedBefore := usedGas
		sdb.SetTxContext(g.tx.Hash(), idx)
		// miner/worker.go commitTransaction: open the witness scope alongside
		// the state snapshot, so both roll back together.
		sdb.BeginWitnessTx()

		var err error
		switch g.mode {
		case wdDropInterrupt:
			var interrupt atomic.Bool
			seen := 0
			evm := vm.NewEVM(f.blockCtx, sdb, f.config, vm.Config{Tracer: &tracing.Hooks{
				OnOpcode: func(uint64, byte, uint64, uint64, tracing.OpContext, []byte, int, error) {
					seen++
					if seen >= g.afterOps {
						interrupt.Store(true)
					}
				},
			}})
			evm.SetInterrupt(&interrupt)
			_, err = ApplyTransaction(evm, gp, sdb, f.header, g.tx, &usedGas)
			if err == nil {
				t.Fatalf("ghost at %d ran to completion after %d opcodes; raise afterOps coverage", g.at, seen)
			}
			if !errors.Is(err, vm.ErrInterrupt) {
				t.Fatalf("ghost at %d: want vm.ErrInterrupt, got %v", g.at, err)
			}
		case wdDropPreCheck:
			_, err = ApplyTransaction(plain, gp, sdb, f.header, g.tx, &usedGas)
			if err == nil {
				t.Fatalf("pre-check ghost at %d unexpectedly succeeded", g.at)
			}
			if errors.Is(err, vm.ErrInterrupt) {
				t.Fatalf("pre-check ghost at %d hit the interrupt path", g.at)
			}
		}
		// The producer rolls back and drops the transaction. The witness
		// scope is discarded after the state revert, matching the order
		// miner/worker.go's commitTransaction uses.
		sdb.RevertToSnapshot(snap)
		sdb.DiscardWitnessTx()
		gp.SetGas(gasBefore)
		usedGas = usedBefore
	}

	idx := 0
	for i := range f.txs {
		for _, g := range ghosts {
			if g.at == i {
				attempt(g, idx)
			}
		}
		sdb.SetTxContext(f.txs[i].Hash(), idx)
		sdb.BeginWitnessTx()
		if _, err := ApplyTransaction(plain, gp, sdb, f.header, f.txs[i], &usedGas); err != nil {
			t.Fatalf("included tx %d (%s): %v", i, f.names[i], err)
		}
		sdb.CommitWitnessTx()
		idx++
	}
	for _, g := range ghosts {
		if g.at >= len(f.txs) {
			attempt(g, idx)
		}
	}

	sdb.IntermediateRoot(f.config.IsEIP158(f.blockCtx.BlockNumber))
	return w
}

// wdImport replays the block the way an importing node does: exactly the
// transactions the block contains, nothing else.
func (f *witnessDropFixture) wdImport(t *testing.T) *stateless.Witness {
	t.Helper()

	db := state.NewDatabase(f.tdb, nil)
	_, processReader, _, err := db.ReadersWithCacheStatsTriple(f.root)
	if err != nil {
		t.Fatalf("readers: %v", err)
	}
	sdb, err := state.NewWithReader(f.root, db, processReader)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	w, err := stateless.NewWitness(f.header, nil)
	if err != nil {
		t.Fatalf("new witness: %v", err)
	}
	sdb.StartPrefetcher("chain", w, nil)
	defer sdb.StopPrefetcher()

	evm := vm.NewEVM(f.blockCtx, sdb, f.config, vm.Config{})
	gp := new(GasPool).AddGas(f.blockCtx.GasLimit)
	var usedGas uint64
	for i, tx := range f.txs {
		sdb.SetTxContext(tx.Hash(), i)
		if _, err := ApplyTransaction(evm, gp, sdb, f.header, tx, &usedGas); err != nil {
			t.Fatalf("import tx %d (%s): %v", i, f.names[i], err)
		}
	}
	sdb.IntermediateRoot(f.config.IsEIP158(f.blockCtx.BlockNumber))
	return w
}

// wdDiff describes how two witnesses for the same block differ.
func wdDiff(want, got *stateless.Witness) string {
	var extraNodes, missingNodes, extraCodes, missingCodes int
	for n := range got.State {
		if _, ok := want.State[n]; !ok {
			extraNodes++
		}
	}
	for n := range want.State {
		if _, ok := got.State[n]; !ok {
			missingNodes++
		}
	}
	for c := range got.Codes {
		if _, ok := want.Codes[c]; !ok {
			extraCodes++
		}
	}
	for c := range want.Codes {
		if _, ok := got.Codes[c]; !ok {
			missingCodes++
		}
	}
	return fmt.Sprintf("nodes %d->%d (+%d extra, -%d missing), codes %d->%d (+%d extra, -%d missing)",
		len(want.State), len(got.State), extraNodes, missingNodes,
		len(want.Codes), len(got.Codes), extraCodes, missingCodes)
}

// wdRequireSame asserts two witnesses for the same block are equal in every
// respect a consumer can observe.
//
// The commit hash alone is not enough: BorWitness encodes only
// {Context, Headers, State}, so code-blob contamination is invisible to it
// (core/stateless/encoding.go). Codes still travel with the witness in memory
// and still have to be a function of the block, so they are checked directly.
func wdRequireSame(t *testing.T, what string, want, got *stateless.Witness) {
	t.Helper()

	wantHash, gotHash := wdCommit(t, want), wdCommit(t, got)
	if wantHash != gotHash {
		t.Errorf("%s: witness commitment changed: %x, want %x; %s",
			what, gotHash[:12], wantHash[:12], wdDiff(want, got))
	}
	if len(want.Codes) != len(got.Codes) {
		t.Errorf("%s: witness code set changed: %d codes, want %d; %s",
			what, len(got.Codes), len(want.Codes), wdDiff(want, got))
		return
	}
	for c := range want.Codes {
		if _, ok := got.Codes[c]; !ok {
			t.Errorf("%s: witness is missing a code blob the block needs; %s", what, wdDiff(want, got))
			return
		}
	}
}

func wdCommit(t *testing.T, w *stateless.Witness) common.Hash {
	t.Helper()
	h, err := stateless.WitnessCommitHashFromWitness(w)
	if err != nil {
		t.Fatalf("hashing witness: %v", err)
	}
	return h
}

// TestWitnessIgnoresInterruptedTransaction sweeps the build deadline across an
// attempted transaction. Every cut point is a different wall-clock outcome the
// producer could have had, and all of them must yield the witness of the block
// that was actually produced.
func TestWitnessIgnoresInterruptedTransaction(t *testing.T) {
	f := newWitnessDropFixture(t)

	clean := f.wdBuild(t, nil)
	t.Logf("no transaction dropped: nodes=%d codes=%d commit=%x",
		len(clean.State), len(clean.Codes), wdCommit(t, clean).Bytes()[:12])

	for _, ops := range []int{1, 2, 3, 5, 8, 12, 16, 20, 24, 27} {
		t.Run(fmt.Sprintf("killed_after_%d_opcodes", ops), func(t *testing.T) {
			got := f.wdBuild(t, []wdGhost{{at: 3, tx: f.ghosts[0], mode: wdDropInterrupt, afterOps: ops}})
			wdRequireSame(t, fmt.Sprintf("interrupted after %d opcodes", ops), clean, got)
		})
	}
}

// TestWitnessIgnoresInterruptAtEveryPosition moves the dropped transaction
// through the block. Contamination that a neighbouring transaction happens to
// mask at one position must not reappear at another.
func TestWitnessIgnoresInterruptAtEveryPosition(t *testing.T) {
	f := newWitnessDropFixture(t)

	clean := f.wdBuild(t, nil)

	for _, at := range []int{0, 1, 5, 9, len(f.txs) - 1, len(f.txs)} {
		t.Run(fmt.Sprintf("at_position_%d", at), func(t *testing.T) {
			got := f.wdBuild(t, []wdGhost{{at: at, tx: f.ghosts[0], mode: wdDropInterrupt, afterOps: 20}})
			wdRequireSame(t, fmt.Sprintf("dropped at position %d", at), clean, got)
		})
	}
}

// TestWitnessIgnoresMultipleDroppedTransactions covers a build that loses
// several transactions, which is what a producer under sustained load does.
func TestWitnessIgnoresMultipleDroppedTransactions(t *testing.T) {
	f := newWitnessDropFixture(t)

	clean := f.wdBuild(t, nil)

	got := f.wdBuild(t, []wdGhost{
		{at: 1, tx: f.ghosts[0], mode: wdDropInterrupt, afterOps: 9},
		{at: 4, tx: f.ghosts[1], mode: wdDropInterrupt, afterOps: 22},
		{at: 7, tx: f.ghosts[0], mode: wdDropInterrupt, afterOps: 3},
	})
	wdRequireSame(t, "three dropped transactions", clean, got)
}

// TestWitnessIgnoresDroppedTransactionSharingState is the subtle direction: the
// dropped transaction touches state an included transaction also touches.
// Discarding its contribution must not remove anything the block genuinely
// needs -- a witness that is too small is worse than one that is too large,
// because the consumer fails on a missing node.
func TestWitnessIgnoresDroppedTransactionSharingState(t *testing.T) {
	f := newWitnessDropFixture(t)

	clean := f.wdBuild(t, nil)

	// ghosts[2] targets wdShared, which included transaction "shared" reads.
	got := f.wdBuild(t, []wdGhost{{at: 2, tx: f.ghosts[2], mode: wdDropInterrupt, afterOps: 10}})
	wdRequireSame(t, "dropped transaction sharing state with an included one", clean, got)
}

// TestWitnessIgnoresPreCheckFailedTransaction covers the drop paths that never
// reach the EVM: commitTransactions shifts or pops these too, and the sender
// account they read is still a witness contribution.
func TestWitnessIgnoresPreCheckFailedTransaction(t *testing.T) {
	f := newWitnessDropFixture(t)

	clean := f.wdBuild(t, nil)

	// A nonce far in the future: rejected by the state transition's pre-check
	// after the sender account has been read.
	ghost := f.wdTxNonce(t, len(f.txs), wdGhostA, 42)
	got := f.wdBuild(t, []wdGhost{{at: 6, tx: ghost, mode: wdDropPreCheck}})
	wdRequireSame(t, "pre-check-failed transaction", clean, got)
}

// TestProducerWitnessMatchesImporter is the acceptance test. It asserts the
// property the network actually depends on: the witness a producer publishes
// for a block equals the witness any importing node computes from that block,
// whatever the producer discarded while building it.
func TestProducerWitnessMatchesImporter(t *testing.T) {
	f := newWitnessDropFixture(t)

	imported := f.wdImport(t)

	for _, tc := range []struct {
		name   string
		ghosts []wdGhost
	}{
		{"clean build", nil},
		{"one interrupted transaction", []wdGhost{{at: 3, tx: f.ghosts[0], mode: wdDropInterrupt, afterOps: 18}}},
		{"interrupted early", []wdGhost{{at: 0, tx: f.ghosts[1], mode: wdDropInterrupt, afterOps: 2}}},
		{"pre-check failure", []wdGhost{{at: 5, tx: f.wdTxNonce(t, len(f.txs), wdGhostB, 42), mode: wdDropPreCheck}}},
		{"several drops", []wdGhost{
			{at: 1, tx: f.ghosts[0], mode: wdDropInterrupt, afterOps: 6},
			{at: 8, tx: f.ghosts[1], mode: wdDropInterrupt, afterOps: 25},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			produced := f.wdBuild(t, tc.ghosts)
			wdRequireSame(t, "producer vs importer", imported, produced)
		})
	}
}

// TestWitnessIgnoresDroppedReadOfAlreadyLoadedAccount is the case the other
// tests cannot reach: the dropped transaction reads NEW storage slots of an
// account an INCLUDED transaction already loaded.
//
// Dropping the account wholesale is not an option there -- the block genuinely
// needs it -- so the slot-level read cache has to be rolled back on its own.
// If it is not, the later included transaction that reads those same slots
// gets a silent cache hit, its trie path is never resolved, and the witness
// comes out too SMALL. That direction is far worse than contamination: a
// consumer fails outright on a missing node instead of carrying extra bytes.
func TestWitnessIgnoresDroppedReadOfAlreadyLoadedAccount(t *testing.T) {
	f := newWitnessDropFixture(t)

	// Position 9 loads wdShared and caches slots 0..3; the final transaction
	// reads slots 16..19 of the same account. The ghost sits between them and
	// touches 16..19 first.
	const ghostAt = 10
	if f.names[9] != "shared" || f.names[len(f.names)-1] != "sharedExt" {
		t.Fatalf("fixture layout changed: names[9]=%q last=%q", f.names[9], f.names[len(f.names)-1])
	}

	clean := f.wdBuild(t, nil)

	got := f.wdBuild(t, []wdGhost{{at: ghostAt, tx: f.ghosts[2], mode: wdDropInterrupt, afterOps: 12}})
	wdRequireSame(t, "dropped read of an already-loaded account", clean, got)
}

// TestWitnessScopeLeftOpenFailsSafe pins the direction an unbalanced scope
// errs in. Forgetting to close a scope is a caller bug, but the two ways of
// handling it are not symmetric: keeping the staged nodes yields a witness
// that is merely larger, while dropping them yields one missing nodes the
// block needs, which fails stateless execution. IntermediateRoot must take the
// first option.
func TestWitnessScopeLeftOpenFailsSafe(t *testing.T) {
	f := newWitnessDropFixture(t)

	db := state.NewDatabase(f.tdb, nil)
	_, processReader, _, err := db.ReadersWithCacheStatsTriple(f.root)
	if err != nil {
		t.Fatalf("readers: %v", err)
	}
	sdb, err := state.NewWithReader(f.root, db, processReader)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	w, err := stateless.NewWitness(f.header, nil)
	if err != nil {
		t.Fatalf("new witness: %v", err)
	}
	sdb.StartPrefetcher("miner", w, nil)
	defer sdb.StopPrefetcher()

	evm := vm.NewEVM(f.blockCtx, sdb, f.config, vm.Config{})
	gp := new(GasPool).AddGas(f.blockCtx.GasLimit)
	var usedGas uint64
	for i, tx := range f.txs {
		sdb.SetTxContext(tx.Hash(), i)
		sdb.BeginWitnessTx()
		if _, err := ApplyTransaction(evm, gp, sdb, f.header, tx, &usedGas); err != nil {
			t.Fatalf("tx %d (%s): %v", i, f.names[i], err)
		}
		// Deliberately neither commit nor discard the final transaction's
		// scope: it is still open when the root is computed below.
		if i < len(f.txs)-1 {
			sdb.CommitWitnessTx()
		}
	}
	sdb.IntermediateRoot(f.config.IsEIP158(f.blockCtx.BlockNumber))

	reference := f.wdImport(t)
	for node := range reference.State {
		if _, ok := w.State[node]; !ok {
			t.Fatalf("an open witness scope dropped nodes the block needs: %s", wdDiff(reference, w))
		}
	}
	for code := range reference.Codes {
		if _, ok := w.Codes[code]; !ok {
			t.Fatalf("an open witness scope dropped a code blob the block needs: %s", wdDiff(reference, w))
		}
	}
}
