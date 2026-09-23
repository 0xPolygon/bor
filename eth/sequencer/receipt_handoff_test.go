package sequencer

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// receiptProbeConsumer checks, at each step canonical import takes through
// the provider, that a receipt reader (canonical first, then preconf, as
// eth_getTransactionReceipt does) still finds every watched transaction.
type receiptProbeConsumer struct {
	*Consumer
	watched types.Transactions
	misses  []string
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
		c.probe("after CompletePreconf, before the head write")
	}
	return reason
}

func (c *receiptProbeConsumer) PreconfHeadWritten(block *types.Block) {
	c.probe("after the head write, before eviction")
	c.Consumer.PreconfHeadWritten(block)
	c.probe("after eviction")
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
