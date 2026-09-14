package stateless

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// These tests pin the staging contract BeginTx/CommitTx/DiscardTx offer to a
// block producer: everything a transaction contributes is held back until the
// producer knows whether the transaction made it into the block. Without it a
// dropped transaction's reads stay in the witness -- the witness has no
// journal even though the state does -- and the producer publishes bytes no
// importing node can reproduce.

// wtChain builds a witness whose context sits at height n with a mock chain
// carrying its ancestors, so AddBlockHash has real headers to pull in.
func wtChain(t *testing.T, n uint64) (*Witness, *MockHeaderReader) {
	t.Helper()

	chain := NewMockHeaderReader()
	var prev common.Hash
	for i := uint64(0); i < n; i++ {
		h := &types.Header{Number: new(big.Int).SetUint64(i), ParentHash: prev, GasLimit: 1000 + i}
		chain.AddHeader(h)
		prev = h.Hash()
	}
	ctx := &types.Header{Number: new(big.Int).SetUint64(n), ParentHash: prev}
	w, err := NewWitness(ctx, chain)
	if err != nil {
		t.Fatalf("new witness: %v", err)
	}
	return w, chain
}

func TestWitnessTxWithoutScopeIsPassThrough(t *testing.T) {
	w, _ := wtChain(t, 4)

	w.AddCode([]byte{0x01})
	w.AddState(map[string][]byte{"k": {0x02}})

	if len(w.Codes) != 1 || len(w.State) != 1 {
		t.Fatalf("additions outside a scope must land immediately: codes=%d state=%d", len(w.Codes), len(w.State))
	}
	// Commit and Discard with no open scope must not disturb anything.
	w.CommitTx()
	w.DiscardTx()
	if len(w.Codes) != 1 || len(w.State) != 1 {
		t.Fatalf("no-op Commit/Discard changed the witness: codes=%d state=%d", len(w.Codes), len(w.State))
	}
}

func TestWitnessTxCommitApplies(t *testing.T) {
	w, _ := wtChain(t, 4)

	w.BeginTx()
	w.AddCode([]byte{0xaa})
	w.AddState(map[string][]byte{"a": {0xbb}})
	if len(w.Codes) != 0 || len(w.State) != 0 {
		t.Fatalf("staged additions leaked before CommitTx: codes=%d state=%d", len(w.Codes), len(w.State))
	}
	w.CommitTx()
	if len(w.Codes) != 1 || len(w.State) != 1 {
		t.Fatalf("CommitTx did not apply the staged additions: codes=%d state=%d", len(w.Codes), len(w.State))
	}
}

func TestWitnessTxDiscardDropsAdditions(t *testing.T) {
	w, _ := wtChain(t, 4)

	// Pre-existing content must survive an unrelated discarded transaction.
	w.AddCode([]byte{0x01})
	w.AddState(map[string][]byte{"keep": {0x02}})

	w.BeginTx()
	w.AddCode([]byte{0xaa})
	w.AddState(map[string][]byte{"drop": {0xbb}})
	w.DiscardTx()

	if len(w.Codes) != 1 {
		t.Errorf("discarded code survived: %d codes", len(w.Codes))
	}
	if _, ok := w.Codes[string([]byte{0x01})]; !ok {
		t.Error("DiscardTx removed a code added before the scope")
	}
	if len(w.State) != 1 {
		t.Errorf("discarded state node survived: %d nodes", len(w.State))
	}
	if _, ok := w.State[string([]byte{0x02})]; !ok {
		t.Error("DiscardTx removed a node added before the scope")
	}
}

// TestWitnessTxDiscardRollsBackHeaders covers the channel the state-level
// buffer cannot see: BLOCKHASH extends the witness's ancestor chain directly
// from the EVM, and Headers are part of the canonical wire encoding, so a
// dropped transaction that reads a block hash would otherwise change the
// witness commitment.
func TestWitnessTxDiscardRollsBackHeaders(t *testing.T) {
	w, _ := wtChain(t, 8)

	before := len(w.Headers)

	w.BeginTx()
	w.AddBlockHash(3) // reaches back several ancestors
	if len(w.Headers) <= before {
		t.Fatalf("fixture did not exercise header extension: %d headers before and after", before)
	}
	staged := len(w.Headers)
	w.DiscardTx()

	if len(w.Headers) != before {
		t.Errorf("DiscardTx left %d headers, want %d (extended to %d inside the scope)",
			len(w.Headers), before, staged)
	}
}

func TestWitnessTxCommitKeepsHeaders(t *testing.T) {
	w, _ := wtChain(t, 8)

	w.BeginTx()
	w.AddBlockHash(3)
	extended := len(w.Headers)
	w.CommitTx()

	if len(w.Headers) != extended {
		t.Errorf("CommitTx rolled back headers: %d, want %d", len(w.Headers), extended)
	}
}

// TestWitnessTxDiscardRestoresCommitHash is the property that matters on the
// wire: a discarded transaction must leave the witness commitment untouched.
func TestWitnessTxDiscardRestoresCommitHash(t *testing.T) {
	w, _ := wtChain(t, 8)
	w.AddState(map[string][]byte{"a": {0x01}, "b": {0x02}})

	want, err := WitnessCommitHashFromWitness(w)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	w.BeginTx()
	w.AddState(map[string][]byte{"ghost": {0x99}})
	w.AddCode([]byte{0x77})
	w.AddBlockHash(4)
	w.DiscardTx()

	got, err := WitnessCommitHashFromWitness(w)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	if got != want {
		t.Errorf("discarded transaction changed the witness commitment: %x, want %x", got[:12], want[:12])
	}
}

// TestWitnessTxBeginReplacesOpenScope pins the documented non-nesting
// behaviour so a caller that forgets to close a scope fails loudly in tests
// rather than silently accumulating.
func TestWitnessTxBeginReplacesOpenScope(t *testing.T) {
	w, _ := wtChain(t, 4)

	w.BeginTx()
	w.AddCode([]byte{0xaa})
	w.BeginTx() // replaces the scope; the first transaction's staging is dropped
	w.AddCode([]byte{0xbb})
	w.CommitTx()

	if len(w.Codes) != 1 {
		t.Fatalf("expected only the second scope's code, got %d", len(w.Codes))
	}
	if _, ok := w.Codes[string([]byte{0xbb})]; !ok {
		t.Error("the reopened scope's code did not commit")
	}
}
