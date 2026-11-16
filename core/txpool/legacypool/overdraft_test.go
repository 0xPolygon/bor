// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package legacypool

import (
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// overdraftSetup funds an account for exactly ONE tx of the batch.
func overdraftSetup(t *testing.T, n int) (*LegacyPool, common.Address, []*types.Transaction, *big.Int) {
	pool, key := setupPool()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	txs := make([]*types.Transaction, n)
	for i := range txs {
		txs[i] = pricedTransaction(uint64(i), 100000, big.NewInt(1), key) // cost 100000 each
	}
	balance := new(big.Int).Set(txs[0].Cost()) // can afford 1 tx only
	testAddBalance(pool, addr, balance)
	return pool, addr, txs, balance
}

// Minimal equivalent for geth PR #33492 (the PR adds no new test): a single
// batch (one p2p PooledTransactions / Add call) overdrafts the sender.
func TestOverdraftSingleBatch(t *testing.T) {
	t.Parallel()
	pool, addr, txs, balance := overdraftSetup(t, 16)
	defer pool.Close()

	errs := pool.addRemotesSync(txs)
	accepted := 0
	for _, err := range errs {
		if err == nil {
			accepted++
		}
	}
	pool.mu.RLock()
	var pend *big.Int = new(big.Int)
	if l := pool.pending[addr]; l != nil {
		pend = l.totalcost.ToBig()
	}
	pool.mu.RUnlock()
	t.Logf("accepted=%d pendingCost=%v balance=%v", accepted, pend, balance)
	if pend.Cmp(balance) > 0 {
		t.Fatalf("OVERDRAFT: pending cost %v > balance %v (%d txs accepted, only 1 affordable)", pend, balance, accepted)
	}
}

// healthykim's gap-fill case on PR #33492: pending (1,1,1), gap, queued (2);
// balance fits pending + one (2). The gap-filling tx must still be accepted.
func TestOverdraftGapFillStillAccepted(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, addr, big.NewInt(3*100100+200100+50))
	for n := uint64(0); n < 3; n++ {
		if err := pool.addRemoteSync(pricedTransaction(n, 100000, big.NewInt(1), key)); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.addRemoteSync(pricedTransaction(4, 100000, big.NewInt(2), key)); err != nil {
		t.Fatal(err)
	}
	if err := pool.addRemoteSync(pricedTransaction(3, 100000, big.NewInt(2), key)); err != nil {
		t.Fatalf("gap-filling tx rejected: %v", err)
	}
	pending, queued := pool.Stats()
	pool.mu.RLock()
	pend := pool.pending[addr].totalcost.ToBig()
	pool.mu.RUnlock()
	t.Logf("pending=%d queued=%d pendingCost=%v", pending, queued, pend)
	if pending != 4 || pend.Cmp(big.NewInt(3*100100+200100+50)) > 0 {
		t.Fatalf("unexpected: pending=%d queued=%d cost=%v", pending, queued, pend)
	}
}

// Same overdraft but via SEPARATE Add calls that land before the async
// promotion reorg runs (e.g. two peers / two RPC calls in the same reorg
// window). The per-batch fix in #33492 does not cover this.
func TestOverdraftAcrossBatches(t *testing.T) {
	t.Parallel()
	pool, addr, txs, balance := overdraftSetup(t, 16)
	defer pool.Close()

	all := newAccountSet(pool.signer)
	accepted := 0
	pool.mu.Lock()
	for _, tx := range txs {
		errs, dirty := pool.addTxs([]*types.Transaction{tx}, false) // one batch per tx
		if errs[0] == nil {
			accepted++
		}
		all.merge(dirty)
	}
	pool.mu.Unlock()
	<-pool.requestPromoteExecutables(all)

	pool.mu.RLock()
	var pend *big.Int = new(big.Int)
	if l := pool.pending[addr]; l != nil {
		pend = l.totalcost.ToBig()
	}
	pool.mu.RUnlock()
	t.Logf("accepted=%d pendingCost=%v balance=%v", accepted, pend, balance)
	if pend.Cmp(balance) > 0 {
		t.Fatalf("OVERDRAFT: pending cost %v > balance %v (%d txs accepted, only 1 affordable)", pend, balance, accepted)
	}
}

// A replacement of a promotable queued tx must free the old tx's cost, else a
// sender with an exact balance could not bump the fee of a tx not yet promoted.
func TestOverdraftQueuedReplacement(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	tx0 := pricedTransaction(0, 100000, big.NewInt(1), key)
	tx1 := pricedTransaction(1, 100000, big.NewInt(1), key)
	bump := pricedTransaction(1, 100000, big.NewInt(2), key)
	testAddBalance(pool, addr, new(big.Int).Add(tx0.Cost(), bump.Cost()))

	// Admit both without a reorg, so they stay queued.
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for _, tx := range []*types.Transaction{tx0, tx1} {
		if errs, _ := pool.addTxs([]*types.Transaction{tx}, false); errs[0] != nil {
			t.Fatalf("tx %d rejected: %v", tx.Nonce(), errs[0])
		}
	}
	if have := pool.existingCost(addr, 1); have == nil || have.Cmp(tx1.Cost()) != 0 {
		t.Fatalf("existing cost of queued nonce 1: have %v, want %v", have, tx1.Cost())
	}
	if have := pool.existingCost(addr, 2); have != nil {
		t.Fatalf("existing cost of missing nonce 2: have %v, want nil", have)
	}
	if errs, _ := pool.addTxs([]*types.Transaction{bump}, false); errs[0] != nil {
		t.Fatalf("fee bump of queued tx rejected: %v", errs[0])
	}
	// One more tx does not fit the balance.
	if errs, _ := pool.addTxs([]*types.Transaction{pricedTransaction(2, 100000, big.NewInt(1), key)}, false); errs[0] == nil {
		t.Fatal("tx beyond the balance accepted")
	}
}

// When a block mines a pending tx, the reset that follows must not count the
// mined tx again: the balance is already reduced by it. The queued next tx fits
// the remaining balance and must be promoted at that reset, not one block later.
func TestOverdraftPromoteAfterMinedPending(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	tx0 := pricedTransaction(0, 60000, big.NewInt(1), key)
	tx1 := pricedTransaction(1, 40000, big.NewInt(1), key)
	testAddBalance(pool, addr, new(big.Int).Add(tx0.Cost(), tx1.Cost()))
	if err := pool.addRemoteSync(tx0); err != nil {
		t.Fatalf("tx0 rejected: %v", err)
	}
	pool.mu.Lock()
	errs, _ := pool.addTxs([]*types.Transaction{tx1}, false)
	pool.mu.Unlock()
	if errs[0] != nil {
		t.Fatalf("tx1 rejected: %v", errs[0])
	}
	if pending, queued := pool.Stats(); pending != 1 || queued != 1 {
		t.Fatalf("before block: pending %d, queued %d, want 1 and 1", pending, queued)
	}
	// A block includes tx0: the nonce moves on and tx0's cost leaves the balance.
	testSetNonce(pool, addr, 1)
	testAddBalance(pool, addr, new(big.Int).Neg(tx0.Cost()))
	<-pool.requestReset(nil, nil)

	if pending, queued := pool.Stats(); pending != 1 || queued != 0 {
		t.Fatalf("after block: pending %d, queued %d, want 1 and 0", pending, queued)
	}
	if pool.pending[addr] == nil || pool.pending[addr].txs.Get(1) == nil {
		t.Fatal("tx1 not promoted at the reset after tx0 was mined")
	}
}

// After a reorg, mined txs come back below the still-pending higher nonces. A
// reinjected tx must not pay for the reinjected txs queued before it on top of
// the pending ones, or it is dropped and leaves a nonce gap.
func TestOverdraftReorgReinjection(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	txs := make([]*types.Transaction, 4)
	for i := range txs {
		txs[i] = pricedTransaction(uint64(i), 100000, big.NewInt(1), key)
	}
	cost := txs[0].Cost()
	testSetNonce(pool, addr, 2)
	testAddBalance(pool, addr, new(big.Int).Mul(cost, big.NewInt(2)))
	<-pool.requestReset(nil, nil)
	for _, tx := range txs[2:] {
		if err := pool.addRemoteSync(tx); err != nil {
			t.Fatalf("tx %d rejected: %v", tx.Nonce(), err)
		}
	}
	if pending, _ := pool.Stats(); pending != 2 {
		t.Fatalf("pending before the reorg: have %d, want 2", pending)
	}
	// The reorg drops the block with nonces 0 and 1. The balance is back by
	// less than their max cost, so it covers 3 txs, not 4.
	testSetNonce(pool, addr, 0)
	testAddBalance(pool, addr, cost)

	// Reinject the way reset() does: nonces rebuilt from the state first.
	pool.mu.Lock()
	pool.pendingNonces = newNoncer(pool.currentState)
	errs, _ := pool.addTxs(txs[:2], false)
	pool.mu.Unlock()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("reinjected tx %d rejected: %v", i, err)
		}
	}
	// A fee bump of a still-pending tx pays against pending only, not against the
	// reinjected txs queued below it.
	bump := pricedTransaction(2, 100000, big.NewInt(2), key)
	pool.mu.Lock()
	errs, _ = pool.addTxs([]*types.Transaction{bump}, false)
	pool.mu.Unlock()
	if errs[0] != nil {
		t.Fatalf("fee bump of a pending tx after the reorg rejected: %v", errs[0])
	}
}

// add() releases pool.mu while it evicts underpriced txs, so the balance check
// must run again once the lock is back.
func TestOverdraftRevalidateAfterRelock(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	tx0 := pricedTransaction(0, 100000, big.NewInt(1), key)
	tx1 := pricedTransaction(1, 100000, big.NewInt(1), key)
	testAddBalance(pool, addr, tx0.Cost())

	pool.mu.Lock()
	defer pool.mu.Unlock()
	if err := pool.revalidateAfterRelock(tx0, nil, 0); err != nil {
		t.Fatalf("affordable tx failed the re-check: %v", err)
	}
	if errs, _ := pool.addTxs([]*types.Transaction{tx0}, false); errs[0] != nil {
		t.Fatalf("tx0 rejected: %v", errs[0])
	}
	// tx0 now uses the whole balance, as if a concurrent add had landed it.
	if err := pool.revalidateAfterRelock(tx1, nil, 0); !errors.Is(err, core.ErrInsufficientFunds) {
		t.Fatalf("re-check: have %v, want %v", err, core.ErrInsufficientFunds)
	}
}

// A fee bump of a pending tx frees its old cost, so a balance that covers only
// the new cost is enough.
func TestOverdraftPendingReplacementExactBalance(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	tx0 := pricedTransaction(0, 100000, big.NewInt(1), key)
	bump := pricedTransaction(0, 100000, big.NewInt(2), key)
	testAddBalance(pool, addr, bump.Cost())
	if err := pool.addRemoteSync(tx0); err != nil {
		t.Fatalf("tx0 rejected: %v", err)
	}
	if err := pool.addRemoteSync(bump); err != nil {
		t.Fatalf("fee bump of a pending tx rejected: %v", err)
	}
}

// A fee bump of a queued tx in the middle of the promotable run only pays for
// the txs before it. The txs after it are held back at promotion if they no
// longer fit, not counted at admission.
func TestOverdraftMiddleQueuedReplacement(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	txs := make([]*types.Transaction, 3)
	for i := range txs {
		txs[i] = pricedTransaction(uint64(i), 100000, big.NewInt(1), key)
	}
	bump := pricedTransaction(1, 100000, big.NewInt(2), key)
	full := new(big.Int).Mul(bump.Cost(), big.NewInt(3))
	testAddBalance(pool, addr, full)

	pool.mu.Lock()
	defer pool.mu.Unlock()
	if errs, _ := pool.addTxs(txs, false); errs[0] != nil || errs[1] != nil || errs[2] != nil {
		t.Fatalf("queued txs rejected: %v", errs)
	}
	// Leave exactly enough for tx0 and the bumped tx1.
	pool.currentState.SubBalance(addr, uint256.MustFromBig(new(big.Int).Sub(full, new(big.Int).Add(txs[0].Cost(), bump.Cost()))), tracing.BalanceChangeUnspecified)
	// One wei short: the bump must still pay for tx1's slot in full, so the
	// replaced tx itself is counted before its cost is freed.
	pool.currentState.SubBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	if errs, _ := pool.addTxs([]*types.Transaction{bump}, false); errs[0] == nil {
		t.Fatal("fee bump one wei over the balance accepted")
	}
	pool.currentState.AddBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	if errs, _ := pool.addTxs([]*types.Transaction{bump}, false); errs[0] != nil {
		t.Fatalf("fee bump of a middle queued tx rejected: %v", errs[0])
	}
}

// Only the queued run contiguous from the pending nonce is promoted next, so a
// gapped queued tx below the new nonce is not charged to it.
func TestOverdraftGappedQueuedNotCounted(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	tx0 := pricedTransaction(0, 100000, big.NewInt(1), key)
	tx2 := pricedTransaction(2, 100000, big.NewInt(1), key) // gap at nonce 1
	tx3 := pricedTransaction(3, 100000, big.NewInt(1), key)
	testAddBalance(pool, addr, new(big.Int).Add(tx0.Cost(), tx3.Cost()))

	pool.mu.Lock()
	defer pool.mu.Unlock()
	if errs, _ := pool.addTxs([]*types.Transaction{tx0, tx2}, false); errs[0] != nil || errs[1] != nil {
		t.Fatalf("queued txs rejected: %v", errs)
	}
	if errs, _ := pool.addTxs([]*types.Transaction{tx3}, false); errs[0] != nil {
		t.Fatalf("tx3 rejected although only tx0 is promotable: %v", errs[0])
	}
}

// A fee bump of the queued tx right after the pending ones still pays for the
// pending txs and for its own slot in full.
func TestOverdraftQueuedAfterPendingBump(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	tx0 := pricedTransaction(0, 100000, big.NewInt(1), key)
	tx1 := pricedTransaction(1, 100000, big.NewInt(1), key)
	bump := pricedTransaction(1, 100000, big.NewInt(2), key)
	exact := new(big.Int).Add(tx0.Cost(), bump.Cost())
	testAddBalance(pool, addr, exact)
	if err := pool.addRemoteSync(tx0); err != nil {
		t.Fatalf("tx0 rejected: %v", err)
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if errs, _ := pool.addTxs([]*types.Transaction{tx1}, false); errs[0] != nil {
		t.Fatalf("tx1 rejected: %v", errs[0])
	}
	pool.currentState.SubBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	if errs, _ := pool.addTxs([]*types.Transaction{bump}, false); errs[0] == nil {
		t.Fatal("fee bump one wei over the balance accepted")
	}
	pool.currentState.AddBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	if errs, _ := pool.addTxs([]*types.Transaction{bump}, false); errs[0] != nil {
		t.Fatalf("fee bump at the exact balance rejected: %v", errs[0])
	}
}

// An unmined pending tx still counts at promotion: when the balance drops, the
// queued tx after it stays queued.
func TestOverdraftPromotionCountsUnminedPending(t *testing.T) {
	t.Parallel()
	pool, key := setupPool()
	defer pool.Close()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	tx0 := pricedTransaction(0, 100000, big.NewInt(1), key)
	tx1 := pricedTransaction(1, 100000, big.NewInt(1), key)
	testAddBalance(pool, addr, new(big.Int).Add(tx0.Cost(), tx1.Cost()))
	if err := pool.addRemoteSync(tx0); err != nil {
		t.Fatalf("tx0 rejected: %v", err)
	}
	pool.mu.Lock()
	errs, _ := pool.addTxs([]*types.Transaction{tx1}, false)
	pool.currentState.SubBalance(addr, uint256.MustFromBig(tx1.Cost()), tracing.BalanceChangeUnspecified)
	pool.mu.Unlock()
	if errs[0] != nil {
		t.Fatalf("tx1 rejected: %v", errs[0])
	}
	<-pool.requestPromoteExecutables(newAccountSet(pool.signer, addr))
	if pending, queued := pool.Stats(); pending != 1 || queued != 1 {
		t.Fatalf("pending %d, queued %d, want 1 and 1", pending, queued)
	}
}
