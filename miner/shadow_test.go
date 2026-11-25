//go:build lls_shadow

package miner

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

// The local stub makes the worker sequence (LLS poll loop on, every gate
// open) without any store: this is what shadow mode attaches.
func TestShadowSequencerEnablesLLSLoop(t *testing.T) {
	w, b, _ := newSequencerTestWorker(t)
	w.sequencer = &ShadowSequencer{Poll: 50 * time.Millisecond}
	w.running.Store(true)

	parent := b.chain.CurrentBlock()
	number := new(big.Int).Add(parent.Number, big.NewInt(1))

	if got := w.sequencerPoll(number); got != 50*time.Millisecond {
		t.Fatalf("sequencerPoll = %v, want 50ms (continuous fill)", got)
	}

	header := &types.Header{Number: number, ParentHash: parent.Hash(), GasLimit: parent.GasLimit, BaseFee: big.NewInt(1)}
	env := &environment{header: header}

	if !w.sealBarrier(env) {
		t.Fatal("stub must let the seal barrier pass")
	}

	genParams := &generateParams{production: true}
	w.fetchAdoption(genParams, header)

	if genParams.adoption != nil {
		t.Fatal("stub must never hand out a store window")
	}
}

// A fake-signature tx whose sender cache is pre-filled is accepted by the
// pool and committed by the builder with the chosen sender: no ecrecover.
func TestShadowInjectedTxIsBuilt(t *testing.T) {
	w, b, _ := newSequencerTestWorker(t)

	cfg := b.chain.Config()
	head := b.chain.CurrentBlock()
	to := testUserAddress

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: cfg.ChainID, Nonce: b.txPool.PoolNonce(testBankAddress),
		GasTipCap: big.NewInt(30e9), GasFeeCap: new(big.Int).Add(big.NewInt(30e9), new(big.Int).Mul(head.BaseFee, big.NewInt(2))),
		Gas: 21000, To: &to, Value: big.NewInt(1),
		V: big.NewInt(0), R: big.NewInt(1), S: big.NewInt(1),
	})

	// Without the cache the fake signature recovers to some unrelated
	// (unfunded) address, never to the bank: the pool must reject it.
	if errs := b.txPool.Add([]*types.Transaction{types.NewTx(&types.DynamicFeeTx{
		ChainID: cfg.ChainID, Nonce: 0, GasTipCap: big.NewInt(30e9), GasFeeCap: big.NewInt(1e12),
		Gas: 21000, To: &to, V: big.NewInt(0), R: big.NewInt(1), S: big.NewInt(1),
	})}, true); errs[0] == nil {
		t.Fatal("fake signature without the sender cache was accepted")
	}

	types.ShadowSetSender(tx, types.LatestSigner(cfg), testBankAddress)

	if errs := b.txPool.Add([]*types.Transaction{tx}, true); errs[0] != nil {
		t.Fatalf("pool rejected injected tx: %v", errs[0])
	}

	genParams := &generateParams{coinbase: testBankAddress}

	work, err := w.prepareWork(genParams, false)
	if err != nil {
		t.Fatalf("prepareWork: %v", err)
	}

	if err := w.fillTransactions(nil, work, genParams); err != nil {
		t.Fatalf("fill: %v", err)
	}

	found := false
	for _, inc := range work.txs {
		if inc.Hash() == tx.Hash() {
			found = true
		}
	}

	if !found {
		t.Fatalf("injected tx not built into the block (txs=%d)", len(work.txs))
	}

	had := b.txPool.Has(tx.Hash())
	removed := b.txPool.ShadowRemove(tx.Hash())

	if !had || !removed || b.txPool.Has(tx.Hash()) {
		t.Fatalf("ShadowRemove did not retire the injected tx: had=%v removed=%v still=%v",
			had, removed, b.txPool.Has(tx.Hash()))
	}
}
