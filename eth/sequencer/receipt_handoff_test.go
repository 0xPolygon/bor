package sequencer

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

// receiptProbeConsumer checks, at each step canonical import takes through
// the provider, that a receipt reader (canonical first, then preconf, as
// eth_getTransactionReceipt does) still finds every watched transaction.
type receiptProbeConsumer struct {
	*Consumer
	watched         types.Transactions
	misses          []string
	beforeHeadWrite func()
	afterHeadWrite  func()
}

func (c *receiptProbeConsumer) probe(step string) {
	for _, tx := range c.watched {
		if lookup, _ := c.chain.GetCanonicalTransaction(tx.Hash()); lookup != nil {
			continue
		}
		if _, receipt, ok := c.LookupPreconf(tx.Hash()); ok && receipt != nil {
			continue
		}
		c.misses = append(c.misses, step+": "+tx.Hash().Hex())
	}
}

func (c *receiptProbeConsumer) CompletePreconf(block *types.Block, receipts types.Receipts, committed bool) string {
	reason := c.Consumer.CompletePreconf(block, receipts, committed)
	if committed {
		if c.beforeHeadWrite != nil {
			c.beforeHeadWrite()
		}
		c.probe("after CompletePreconf, before the head write")
	}
	return reason
}

func (c *receiptProbeConsumer) PreconfHeadWritten(block *types.Block) {
	if c.afterHeadWrite != nil {
		c.afterHeadWrite()
	}
	c.probe("after the head write, before eviction")
	c.Consumer.PreconfHeadWritten(block)
	c.probe("after eviction")
}

func TestPreconfLandingSurvivesDelayedHeadEvent(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0)}
	block, _ := buildPartialReuseBlock(t, h, txs)
	consumer := publishPrefix(t, h, txs).consumer
	parentHead := h.chain.CurrentBlock()
	child := newPendingRPCCoverageFixture(t, block.NumberU64()+1, block.Hash())
	store := consumer.pendingStore()
	generation := store.begin(child.block.NumberU64(), child.block.ParentHash(), false)
	if !store.publish(child.block, types.Receipts{child.receipt}, child.state, nil, generation) {
		t.Fatal("publish descendant")
	}
	consumer.index.Add(child.tx, child.receipt)
	probe := &receiptProbeConsumer{Consumer: consumer, watched: txs}
	probe.beforeHeadWrite = func() {
		if consumer.landing.Load() == nil || h.chain.CurrentBlock() != parentHead {
			t.Fatal("expected matched completion before the head write")
		}
		anchor, ok := consumer.pendingReadAnchor()
		if !ok {
			t.Fatal("landing failed to anchor read")
		}
		receipt, _, found := consumer.index.Lookup(txs[0].Hash())
		if !found || receipt == nil {
			t.Fatal("matched receipt missing before delayed head event")
		}
		// Model a queued event running between the receipt lookup and its
		// closing anchor check, while canonical lookup still cannot recover it.
		consumer.handleCanonicalHead()
		if !consumer.pendingReadAnchorValid(anchor) {
			t.Error("delayed head event invalidated an in-flight receipt read")
		}
		if _, _, ok := consumer.LookupPreconf(child.tx.Hash()); !ok {
			t.Error("delayed head event withdrew a valid descendant")
		}
	}
	probe.afterHeadWrite = func() {
		if consumer.landing.Load() == nil {
			t.Fatal("landing cleared before observer")
		}
		consumer.handleCanonicalHead()
		if _, _, ok := consumer.index.Lookup(txs[0].Hash()); ok {
			t.Error("reconciliation deferred after the landing became canonical")
		}
	}
	h.chain.SetPreconfProvider(probe)
	if _, err := h.chain.InsertChain(types.Blocks{block}, false); err != nil {
		t.Fatalf("insert canonical block: %v", err)
	}
	if len(probe.misses) != 0 {
		t.Fatalf("receipt disappeared during import: %v", probe.misses)
	}
	if consumer.landing.Load() != nil {
		t.Fatal("landing survived the head write")
	}
	if _, _, ok := consumer.LookupPreconf(child.tx.Hash()); !ok {
		t.Fatal("valid descendant receipt missing after import")
	}
	if records := rawdb.ReadInvalidPreconfsInRange(h.chain.DB(), child.block.NumberU64(), child.block.NumberU64()); len(records) != 0 {
		t.Fatalf("valid descendant was invalidated: %+v", records)
	}
}

func TestPreconfReceiptNeverVanishesAcrossCanonicalImport(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1), h.transfer(t, 2)}
	block, _ := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs)
	probe := &receiptProbeConsumer{Consumer: s.consumer, watched: txs}
	probe.probe("before import")
	if len(probe.misses) != 0 {
		t.Fatalf("preconf receipts not served before import: %v", probe.misses)
	}
	h.chain.SetPreconfProvider(probe)

	if _, err := h.chain.InsertChain(types.Blocks{block}, false); err != nil {
		t.Fatalf("insert canonical block: %v", err)
	}
	if h.chain.CurrentBlock().Hash() != block.Hash() {
		t.Fatalf("head = %s, want %s", h.chain.CurrentBlock().Hash(), block.Hash())
	}
	if len(probe.misses) != 0 {
		t.Fatalf("receipt reads returned null during import: %v", probe.misses)
	}
	for _, tx := range txs {
		if _, _, ok := s.consumer.index.Lookup(tx.Hash()); ok {
			t.Fatalf("preconf receipt %s outlived the head write", tx.Hash())
		}
	}
	if s.consumer.landing.Load() != nil {
		t.Fatal("landing marker survived the head write")
	}
}

// A block whose parent is the head but that CompletePreconf never matched
// must not anchor reads: only the in-flight matched import may.
func TestPreconfReadAnchorRequiresLandingMatch(t *testing.T) {
	h := partialReuseHarness(t)
	consumer := h.session().consumer
	head := h.chain.CurrentBlock()
	child := &types.Header{Number: new(big.Int).Add(head.Number, common.Big1), ParentHash: head.Hash(), Difficulty: common.Big1}

	consumer.reconciled.Store(child)
	if _, ok := consumer.pendingReadAnchor(); ok {
		t.Fatal("unmatched child anchored preconf reads")
	}
	consumer.landing.Store(child)
	anchor, ok := consumer.pendingReadAnchor()
	if !ok || anchor != child || !consumer.pendingReadAnchorValid(anchor) {
		t.Fatal("landing child did not anchor preconf reads")
	}
	consumer.landing.Store(nil)
	if consumer.pendingReadAnchorValid(anchor) {
		t.Fatal("anchor stayed valid after the landing cleared")
	}
}

// A read that took its anchor on the head stays valid when CompletePreconf
// matches the head's child before the read finishes, and not when the new
// marker is some other block.
func TestPreconfReadSurvivesMatchedHandoffMidRead(t *testing.T) {
	h := partialReuseHarness(t)
	consumer := h.session().consumer
	head := h.chain.CurrentBlock()
	anchor, ok := consumer.pendingReadAnchor()
	if !ok || anchor.Hash() != head.Hash() {
		t.Fatal("head did not anchor reads")
	}
	child := &types.Header{Number: new(big.Int).Add(head.Number, common.Big1), ParentHash: head.Hash(), Difficulty: common.Big1}

	// CompletePreconf's order: landing, then reconciled.
	consumer.landing.Store(child)
	consumer.reconciled.Store(child)
	if !consumer.pendingReadAnchorValid(anchor) {
		t.Fatal("matched handoff mid-read invalidated a read on the parent")
	}

	other := &types.Header{Number: child.Number, ParentHash: common.Hash{0xde, 0xad}, Difficulty: common.Big1}
	consumer.landing.Store(other)
	consumer.reconciled.Store(other)
	if consumer.pendingReadAnchorValid(anchor) {
		t.Fatal("a marker that is not the anchor's child kept the read valid")
	}
}

func TestPreconfReceiptReadConcurrentWithMatchedCompletion(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0)}
	block, receipts := buildPartialReuseBlock(t, h, txs)
	consumer := publishPrefix(t, h, txs).consumer
	captured := make(chan struct{})
	completed := make(chan struct{})
	result := make(chan bool, 1)
	go func() {
		// Pause LookupPreconf's read sequence after capturing its receipt.
		anchor, ok := consumer.pendingReadAnchor()
		receipt, _, found := consumer.index.Lookup(txs[0].Hash())
		close(captured)
		<-completed
		result <- ok && found && receipt != nil && consumer.pendingReadAnchorValid(anchor)
	}()
	<-captured
	reason := consumer.CompletePreconf(block, receipts, true)
	close(completed)
	if reason != "" || consumer.landing.Load() == nil {
		t.Fatalf("completion did not match: reason=%q", reason)
	}
	if h.chain.CurrentBlock().Hash() == block.Hash() {
		t.Fatal("head moved before the read completed")
	}
	select {
	case found := <-result:
		if !found {
			t.Fatal("receipt disappeared while its matched block was landing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receipt read did not finish")
	}
}

// A committed block that did not match its preconfirmation withdraws, before
// the head write, the entry above it built on another parent; one that
// extends the canonical block survives.
func TestMismatchedCompletionWithdrawsStaleDescendants(t *testing.T) {
	for _, test := range []struct {
		name      string
		stale     bool
		importing bool
		reconcile bool
	}{
		{"stale parent is withdrawn", true, false, false},
		{"stale importing child loses its receipts", true, true, false},
		{"canonical parent is kept", false, false, false},
		{"canonical importing child is kept", false, true, false},
		{"reconcile stale importing child", true, true, true},
		{"reconcile canonical importing child", false, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := partialReuseHarness(t)
			consumer := h.session().consumer
			block, receipts := buildPartialReuseBlock(t, h, types.Transactions{h.transfer(t, 0)})
			if test.reconcile {
				if _, err := h.chain.InsertChain(types.Blocks{block}, false); err != nil {
					t.Fatalf("insert canonical block: %v", err)
				}
			}
			number := block.NumberU64() + 1
			parent := block.Hash()
			if test.stale {
				parent = common.Hash{0xde, 0xad}
			}

			fixture := newPendingRPCCoverageFixture(t, number, parent)
			store := consumer.pendingStore()
			if !store.publish(fixture.block, types.Receipts{fixture.receipt}, fixture.state, nil, store.begin(number, parent, false)) {
				t.Fatal("publish child")
			}
			consumer.index.Add(fixture.tx, fixture.receipt)
			if test.importing {
				store.mu.Lock()
				store.entries[pendingKey{number: number, parent: parent}].phase = PendingImporting
				store.mu.Unlock()
			}

			if test.reconcile {
				consumer.publishMu.Lock()
				invalidations := consumer.reconcileCanonicalHeadLocked()
				consumer.publishMu.Unlock()
				store.writeInvalidations(invalidations)
			} else if reason := consumer.CompletePreconf(block, receipts, true); reason != "" {
				t.Fatalf("completion with no entry at the height returned %q", reason)
			}
			_, _, served := consumer.LookupPreconf(fixture.tx.Hash())
			store.mu.RLock()
			entry := store.entries[pendingKey{number: number, parent: parent}]
			store.mu.RUnlock()
			records := rawdb.ReadInvalidPreconfsInRange(h.chain.DB(), number, number)
			if test.importing && test.stale {
				// The import still owns the entry and records its invalidation
				// when it resolves; only its receipts must stop being served.
				if served || entry == nil || entry.deferredInvalidation != "reorged" || len(records) != 0 {
					t.Fatalf("stale importing child: served=%v entry=%v invalidations=%+v", served, entry != nil, records)
				}
				return
			}
			if test.stale {
				if served || entry != nil {
					t.Fatalf("child of a rejected parent survived completion: served=%v entry=%v", served, entry != nil)
				}
				if len(records) != 1 || records[0].Reason != "reorged" {
					t.Fatalf("invalidations at %d = %+v, want one reorged", number, records)
				}
				return
			}
			if !served || entry == nil || len(records) != 0 {
				t.Fatalf("child of the canonical block was withdrawn: served=%v entry=%v invalidations=%+v", served, entry != nil, records)
			}
		})
	}
}
