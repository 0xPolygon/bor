package sequencer

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
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

// A committed block that did not match its preconfirmation withdraws, before
// the head write, the entry above it built on another parent; one that
// extends the canonical block survives.
func TestMismatchedCompletionWithdrawsStaleDescendants(t *testing.T) {
	for _, test := range []struct {
		name  string
		stale bool
	}{{"stale parent is withdrawn", true}, {"canonical parent is kept", false}} {
		t.Run(test.name, func(t *testing.T) {
			h := partialReuseHarness(t)
			consumer := h.session().consumer
			block, receipts := buildPartialReuseBlock(t, h, types.Transactions{h.transfer(t, 0)})
			number := block.NumberU64() + 1
			parent := block.Hash()
			if test.stale {
				parent = common.Hash{0xde, 0xad}
			}

			tx := h.transfer(t, 1)
			child := types.NewBlock(&types.Header{Number: new(big.Int).SetUint64(number), ParentHash: parent, GasLimit: block.GasLimit()},
				&types.Body{Transactions: types.Transactions{tx}}, nil, trie.NewStackTrie(nil))
			receipt := &types.Receipt{TxHash: tx.Hash(), BlockNumber: new(big.Int).SetUint64(number), Logs: []*types.Log{}}
			stateDB, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
			if err != nil {
				t.Fatalf("state: %v", err)
			}
			store := consumer.pendingStore()
			if !store.publish(child, types.Receipts{receipt}, stateDB, nil, store.begin(number, parent, false)) {
				t.Fatal("publish child")
			}
			consumer.index.Add(tx, receipt)

			if reason := consumer.CompletePreconf(block, receipts, true); reason != "" {
				t.Fatalf("completion with no entry at the height returned %q", reason)
			}
			_, _, served := consumer.LookupPreconf(tx.Hash())
			store.mu.RLock()
			entry := store.entries[pendingKey{number: number, parent: parent}]
			store.mu.RUnlock()
			records := rawdb.ReadInvalidPreconfsInRange(h.chain.DB(), number, number)
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
