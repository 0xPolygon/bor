package sequencer

import (
	"math/big"
	"testing"

	"github.com/0xPolygon/sequence-store-proto/commitment"

	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

// A speculative block builds on a sealed parent the chain has not imported
// yet; its payment lane price must come from that parent, as the producer's
// did, not from a canonical lookup that misses it.
func TestBlockEnvPricesPaymentLaneFromSpeculativeParent(t *testing.T) {
	if !metrics.Enabled() {
		metrics.Enable()
	}

	config := *params.TestChainConfig
	config.Bor = finalizationConfig()
	config.Bor.PaymentLaneBlock = big.NewInt(0)
	config.Bor.PaymentLane = map[string]*params.PaymentLaneConfig{"0": params.DefaultPaymentLaneConfig(params.MainnetPaymentLaneAssets)}

	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), &core.Genesis{Config: &config, GasLimit: 30_000_000}, ethash.NewFaker(), core.DefaultConfig())
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	t.Cleanup(chain.Stop)

	genesis := chain.CurrentBlock()
	statedb, err := chain.StateAt(genesis.Root)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	sealed := func(gasUsed uint64) *types.Header {
		h := types.CopyHeader(genesis)
		h.ParentHash, h.Number, h.Time = genesis.Hash(), big.NewInt(1), genesis.Time+2
		h.GasUsed = gasUsed

		return h
	}

	for _, tc := range []struct {
		name   string
		parent *types.Header
		want   uint64
	}{
		{"quiet sealed parent", sealed(0), params.PaymentLaneMaxBps},
		{"busy sealed parent", sealed(genesis.GasLimit), 0},
	} {
		open := openOn(tc.parent, &config, commitment.Head{}).GetBlockOpen()

		noParent := metrics.GetOrRegisterCounter("chain/paymentlane/noparent", nil).Snapshot().Count()
		env := newBlockEnv(chain, statedb.Copy(), open, nil, tc.parent)
		if got := env.evm.Context.PaymentLaneScaleBps; got != tc.want {
			t.Fatalf("%s: scale = %d, want %d", tc.name, got, tc.want)
		}
		if got := metrics.GetOrRegisterCounter("chain/paymentlane/noparent", nil).Snapshot().Count(); got != noParent {
			t.Fatalf("%s: a known parent was reported missing", tc.name)
		}

		// Without the parent the chain cannot resolve it: full price.
		if got := newBlockEnv(chain, statedb.Copy(), open, nil, nil).evm.Context.PaymentLaneScaleBps; got != 0 {
			t.Fatalf("%s: unresolved parent scale = %d, want 0", tc.name, got)
		}
	}
}

func enablePaymentLane(config *params.ChainConfig, lane *params.PaymentLaneConfig) {
	config.Bor.PaymentLaneBlock = big.NewInt(0)
	config.Bor.PaymentLane = map[string]*params.PaymentLaneConfig{"0": lane}
}

// The session builds a block on a speculatively sealed parent from the parked
// state, so the lane price must come from that parent's header.
func TestSessionPricesPaymentLaneFromSpeculativeParent(t *testing.T) {
	h := startExecHarness(t)
	enablePaymentLane(h.chain.Config(), params.DefaultPaymentLaneConfig(params.MainnetPaymentLaneAssets))
	s := h.session()

	cur := handleOK(t, s, openOn(h.chain.CurrentBlock(), h.config, commitment.Head{0x01}))
	cur = handleOK(t, s, recordEntry(h.rawTransfer(t, 0), cur))
	sealed := sealedFromEnv(t, s)
	cur = handleOK(t, s, sealEntry(encodeHeader(t, sealed), cur))
	if s.parked == nil || h.chain.GetHeaderByHash(sealed.Hash()) != nil {
		t.Fatal("sealed parent must be speculative only")
	}

	handleOK(t, s, openOn(sealed, h.config, cur))
	if s.env == nil {
		t.Fatal("open on the speculative parent did not start execution")
	}
	if got := s.env.evm.Context.PaymentLaneScaleBps; got != params.PaymentLaneMaxBps {
		t.Fatalf("scale = %d, want %d", got, params.PaymentLaneMaxBps)
	}
}

// The cap verdict is final even at a sprint start: state-sync txs are not in
// GasUsed and carry no lane log, so the block is voided rather than deferred.
func TestSessionInvalidatesOverCapSprintSeal(t *testing.T) {
	h := startExecHarnessBor(t, finalizationConfig())
	h.chain.Config().Bor.Sprint = map[string]uint64{"0": 4} // block 4 is a boundary
	lane := params.DefaultPaymentLaneConfig(nil)
	lane.NonPaymentGasCapPercentage = 65
	enablePaymentLane(h.chain.Config(), lane)
	s := h.session()

	tx := h.transfer(t, 0)
	cur := handleOK(t, s, openOn(h.chain.CurrentBlock(), h.config, commitment.Head{0x01}))
	cur = handleOK(t, s, recordEntry(h.rawTransfer(t, 0), cur))
	if _, _, ok := s.consumer.index.Lookup(tx.Hash()); !ok {
		t.Fatal("pre-seal receipt not indexed")
	}

	sealed := sealedFromEnv(t, s)
	s.env.header.GasUsed, sealed.GasUsed = sealed.GasLimit*66/100, sealed.GasLimit*66/100
	sealed.Root = s.env.statedb.IntermediateRoot(s.env.evm.ChainConfig().IsEIP158(s.env.header.Number))
	handleOK(t, s, sealEntry(encodeHeader(t, sealed), cur))

	if _, _, ok := s.consumer.index.Lookup(tx.Hash()); ok {
		t.Fatal("over-cap sprint seal still serves its receipts")
	}
	if s.deferToCanonical || s.tip == sealed.Hash() {
		t.Fatal("over-cap sprint seal was deferred to canonical")
	}
	if block := s.consumer.PendingBlock(); block != nil && block.NumberU64() == sealed.Number.Uint64() {
		t.Fatal("over-cap sprint seal still serves its pending block")
	}
}
