package sequencer

import (
	"testing"

	"github.com/0xPolygon/sequence-store-proto/commitment"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// stateSyncTx builds a bor state-sync system transaction (type 0x7f), the
// trailing tx bor appends in Finalize.
func stateSyncTx() *types.Transaction {
	return types.NewTx(&types.StateSyncTx{StateSyncData: []*types.StateSyncData{{ID: 1, Contract: common.Address{0x02}}}})
}

// recordedTxTypes returns the type of every record transaction in the journal.
func recordedTxTypes(t *testing.T, p *Publisher) []byte {
	t.Helper()

	var kinds []byte

	for _, it := range p.journal.items {
		if it.kind != entryRecord {
			continue
		}

		for _, raw := range it.entry.GetRecord().GetTransactions() {
			tx := new(types.Transaction)
			if err := tx.UnmarshalBinary(raw); err != nil {
				t.Fatalf("decode record tx: %v", err)
			}

			kinds = append(kinds, tx.Type())
		}
	}

	return kinds
}

func TestStreamTxsDropsTrailingStateSync(t *testing.T) {
	user := testTx(t, 0)
	all := types.Transactions{user, stateSyncTx()}

	kept := streamTxs(all)
	if len(kept) != 1 || kept[0].Hash() != user.Hash() {
		t.Fatalf("streamTxs kept %d txs, want just the user tx", len(kept))
	}

	if got := streamTxs(types.Transactions{user}); len(got) != 1 {
		t.Fatalf("streamTxs dropped a non-state-sync tx: kept %d", len(got))
	}
}

// The live seal flush must not stream the trailing state-sync tx: the consumer
// cannot re-execute it (maxFeePerGas 0 < baseFee) and would void the block.
func TestRebuildWindowOmitsStateSyncRecord(t *testing.T) {
	p := barePublisher()
	block := blockFor(testHeader(17, common.Hash{0x01}), []*types.Transaction{testTx(t, 0), stateSyncTx()})

	p.mu.Lock()
	ok := p.rebuildWindowLocked(block)
	kinds := recordedTxTypes(t, p)
	p.mu.Unlock()

	if !ok {
		t.Fatal("rebuildWindowLocked failed")
	}

	if len(kinds) != 1 || kinds[0] == types.StateSyncTxType {
		t.Fatalf("rebuilt records = %v, want one non-state-sync record", kinds)
	}
}

// Backfill rebuilds the same content the live path publishes, so it must omit
// the state-sync tx too.
func TestBackfillOmitsStateSyncRecord(t *testing.T) {
	p := barePublisher()
	block := blockFor(testHeader(17, common.Hash{0x01}), []*types.Transaction{testTx(t, 0), stateSyncTx()})

	p.mu.Lock()
	fresh := newJournal()
	_, ok := p.appendBlockLocked(fresh, commitment.Seed(testChainID), block)
	p.journal = fresh
	kinds := recordedTxTypes(t, p)
	p.mu.Unlock()

	if !ok {
		t.Fatal("appendBlockLocked failed")
	}

	if len(kinds) != 1 || kinds[0] == types.StateSyncTxType {
		t.Fatalf("backfilled records = %v, want one non-state-sync record", kinds)
	}
}

// At a sprint boundary the block's state-sync tx is absent from the stream, so
// the seal cross-check can't reproduce the sealed roots. That must defer to
// canonical import (reanchor), not hard-skip the block as an invalid preconf.
func TestSprintSealCrossCheckDefersInsteadOfInvalidating(t *testing.T) {
	h, env, sealed := finalizableEnv(t)
	h.chain.Config().Bor.Sprint = map[string]uint64{"0": 4} // block 4 is a boundary
	sealed.Root = env.statedb.IntermediateRoot(env.evm.ChainConfig().IsEIP158(env.header.Number))
	sealed.Root[0] ^= 0xff // a state-sync tx would change the root the same way
	s := &session{consumer: &Consumer{chain: h.chain, index: NewIndex()}, env: env}

	if _, _, _, ok := s.sealResult(sealed); ok {
		t.Fatal("divergent sprint seal was accepted")
	}
	if !s.deferToCanonical {
		t.Fatal("sprint seal mismatch hard-skipped instead of deferring to canonical")
	}
}

// A non-sprint seal mismatch is a real divergence and must still hard-skip.
func TestNonSprintSealMismatchStillSkips(t *testing.T) {
	h, env, sealed := finalizableEnv(t) // block 4, default sprint 16: not a boundary
	sealed.Root = env.statedb.IntermediateRoot(env.evm.ChainConfig().IsEIP158(env.header.Number))
	sealed.Root[0] ^= 0xff
	s := &session{consumer: &Consumer{chain: h.chain, index: NewIndex()}, env: env}

	if _, _, _, ok := s.sealResult(sealed); ok {
		t.Fatal("divergent seal was accepted")
	}
	if s.deferToCanonical {
		t.Fatal("non-sprint mismatch deferred instead of skipping")
	}
}

// A journal that already holds the user-tx records mirrors a sealed block whose
// only extra tx is the trailing state-sync one — so the seal completes in place
// instead of rebuilding (and re-streaming the state-sync tx).
func TestWindowMirrorsBlockWithTrailingStateSync(t *testing.T) {
	p := barePublisher()
	header := testHeader(17, common.Hash{0x01})
	user := testTx(t, 0)

	p.OpenBlock(header.Number.Uint64(), header.Time, header.ParentHash, header.GasLimit, header.BaseFee)
	p.PublishTx(user)

	block := blockFor(header, []*types.Transaction{user, stateSyncTx()})

	p.mu.Lock()
	mirrors := p.windowMirrorsLocked(block)
	p.mu.Unlock()

	if !mirrors {
		t.Fatal("windowMirrorsLocked = false, want true (seal would needlessly rebuild)")
	}
}
