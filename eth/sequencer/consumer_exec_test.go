package sequencer

import (
	"crypto/ecdsa"
	"math/big"
	"strings"
	"testing"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

// execHarness is a real imported chain whose head state speculative blocks
// execute on: a bor chain config (Rio at genesis, deterministic coinbase,
// burnt contract for the London fee path) with one funded account.
type execHarness struct {
	chain  *core.BlockChain
	config *params.ChainConfig
	key    *ecdsa.PrivateKey
	addr   common.Address
	signer types.Signer
	next   *types.Block // generated but not imported; height CurrentBlock+1
}

func startExecHarness(t *testing.T) *execHarness {
	t.Helper()

	return startExecHarnessBor(t, &params.BorConfig{
		RioBlock: big.NewInt(0),
		Coinbase: map[string]string{
			"0": "0x000000000000000000000000000000000000ba5e",
		},
		BurntContract: map[string]string{
			"0": "0x000000000000000000000000000000000000dead",
		},
	})
}

func startExecHarnessBor(t *testing.T, bor *params.BorConfig) *execHarness {
	t.Helper()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	addr := crypto.PubkeyToAddress(key.PublicKey)

	config := *params.TestChainConfig
	config.Bor = bor

	genesis := &core.Genesis{
		Config:   &config,
		GasLimit: 30_000_000,
		Alloc: types.GenesisAlloc{
			addr: {Balance: new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(params.Ether))},
		},
	}

	// Generate one block more than is imported: tests that need a canonical
	// import event (eviction) insert the spare later.
	_, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 4, nil)

	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), genesis, ethash.NewFaker(), core.DefaultConfig())
	if err != nil {
		t.Fatalf("chain: %v", err)
	}

	t.Cleanup(chain.Stop)

	if _, err := chain.InsertChain(blocks[:3], false); err != nil {
		t.Fatalf("insert: %v", err)
	}

	return &execHarness{
		chain:  chain,
		config: &config,
		key:    key,
		addr:   addr,
		signer: types.LatestSigner(&config),
		next:   blocks[3],
	}
}

func (h *execHarness) session() *session {
	return &session{consumer: &Consumer{chain: h.chain, index: NewIndex()}}
}

func (h *execHarness) transfer(t *testing.T, nonce uint64) *types.Transaction {
	t.Helper()

	return types.MustSignNewTx(h.key, h.signer, &types.LegacyTx{
		Nonce:    nonce,
		To:       &h.addr,
		Gas:      21000,
		GasPrice: big.NewInt(10 * params.GWei),
		Value:    big.NewInt(1),
	})
}

func (h *execHarness) rawTransfer(t *testing.T, nonce uint64) []byte {
	t.Helper()

	raw, err := h.transfer(t, nonce).MarshalBinary()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	return raw
}

// openOn builds a block-open entry extending a parent header.
func openOn(parent *types.Header, config *params.ChainConfig, prefix commitment.Head) *pb.Entry {
	return openEntry(commitment.OpenContext{
		Number:     parent.Number.Uint64() + 1,
		Timestamp:  parent.Time + 2,
		ParentHash: [32]byte(parent.Hash()),
		GasLimit:   parent.GasLimit,
		BaseFee:    eip1559.CalcBaseFee(config, parent),
	}, prefix)
}

// sealedFromEnv builds the sealed header a correct producer would have
// announced for the session's in-progress block: same open context, the
// re-executed gas, receipts root, and state root.
func sealedFromEnv(t *testing.T, s *session) *types.Header {
	t.Helper()

	env := s.env
	if env == nil {
		t.Fatal("no block in progress")
	}

	return &types.Header{
		ParentHash:  env.header.ParentHash,
		Number:      new(big.Int).Set(env.header.Number),
		Time:        env.header.Time,
		GasLimit:    env.header.GasLimit,
		BaseFee:     new(big.Int).Set(env.header.BaseFee),
		GasUsed:     env.header.GasUsed,
		ReceiptHash: types.DeriveSha(types.Receipts(env.receipts), trie.NewStackTrie(nil)),
		Root:        env.statedb.Copy().IntermediateRoot(true),
		Difficulty:  big.NewInt(1),
	}
}

func encodeHeader(t *testing.T, header *types.Header) []byte {
	t.Helper()

	raw, err := rlp.EncodeToBytes(header)
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}

	return raw
}

// handleOK drives one entry through the session, failing the test on a
// position error, and returns the advanced head for the next entry.
func handleOK(t *testing.T, s *session, entry *pb.Entry) commitment.Head {
	t.Helper()

	if err := s.handle(entry); err != nil {
		t.Fatalf("handle: %v", err)
	}

	return s.head
}

// The happy path: a block on the canonical head applies and seals, and the
// next block chains onto the parked speculative state; receipts are served
// pre-seal with a zero block hash and stamped once the seal arrives.
func TestSessionExecutesCanonicalAndParkedBlocks(t *testing.T) {
	h := startExecHarness(t)
	s := h.session()
	head := h.chain.CurrentBlock()

	cur := handleOK(t, s, openOn(head, h.config, commitment.Head{0x01}))

	tx1 := h.transfer(t, 0)
	raw1, _ := tx1.MarshalBinary()
	cur = handleOK(t, s, recordEntry(raw1, cur))

	receipt, _, ok := s.consumer.index.Lookup(tx1.Hash())
	if !ok || receipt.BlockHash != (common.Hash{}) || receipt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("pre-seal receipt wrong: ok=%v receipt=%+v", ok, receipt)
	}

	sealed1 := sealedFromEnv(t, s)
	cur = handleOK(t, s, sealEntry(encodeHeader(t, sealed1), cur))

	if s.env != nil || s.parked == nil || s.tip != sealed1.Hash() {
		t.Fatal("seal must park the post-state and set the speculative tip")
	}

	receipt, _, _ = s.consumer.index.Lookup(tx1.Hash())
	if receipt.BlockHash != sealed1.Hash() {
		t.Fatalf("sealed receipt carries %s, want %s", receipt.BlockHash, sealed1.Hash())
	}

	// Next height chains on the parked state: parent is the speculative
	// tip, not anything the chain has imported.
	cur = handleOK(t, s, openOn(sealed1, h.config, cur))

	if s.env == nil {
		t.Fatal("open on the parked tip must start a block")
	}

	tx2 := h.transfer(t, 1)
	raw2, _ := tx2.MarshalBinary()
	cur = handleOK(t, s, recordEntry(raw2, cur))

	tx3 := h.transfer(t, 2)
	raw3, _ := tx3.MarshalBinary()
	cur = handleOK(t, s, recordEntry(raw3, cur))

	sealed2 := sealedFromEnv(t, s)
	handleOK(t, s, sealEntry(encodeHeader(t, sealed2), cur))

	if _, _, ok := s.consumer.index.Lookup(tx2.Hash()); !ok {
		t.Fatal("second speculative block's receipt missing")
	}

	// The tx context drives the receipt's position in the block.
	if receipt3, _, _ := s.consumer.index.Lookup(tx3.Hash()); receipt3.TransactionIndex != 1 {
		t.Fatalf("second tx in the block must carry index 1, got %d", receipt3.TransactionIndex)
	}

	if s.sealed[sealed1.Number.Uint64()] != sealed1.Hash() {
		t.Fatal("sealed hashes must accumulate for BLOCKHASH resolution")
	}
}

// Application problems void the speculative work and skip until an open
// re-anchors on a canonical parent.
func TestSessionSkipPaths(t *testing.T) {
	h := startExecHarness(t)
	head := h.chain.CurrentBlock()

	t.Run("open with unknown parent", func(t *testing.T) {
		s := h.session()
		unknown := &types.Header{
			Number:   big.NewInt(99),
			Time:     head.Time,
			GasLimit: head.GasLimit,
			BaseFee:  big.NewInt(params.InitialBaseFee),
		}
		handleOK(t, s, openOn(unknown, h.config, commitment.Head{0x01}))

		if s.env != nil {
			t.Fatal("unknown parent must skip")
		}
	})

	t.Run("open height is not parent height plus one", func(t *testing.T) {
		s := h.session()

		// Seal a block first so the skip has parked state to void.
		cur := handleOK(t, s, openOn(head, h.config, commitment.Head{0x01}))
		sealed := sealedFromEnv(t, s)
		cur = handleOK(t, s, sealEntry(encodeHeader(t, sealed), cur))

		open := openEntry(commitment.OpenContext{
			Number:     head.Number.Uint64() + 5,
			Timestamp:  head.Time + 2,
			ParentHash: [32]byte(head.Hash()),
			GasLimit:   head.GasLimit,
			BaseFee:    eip1559.CalcBaseFee(h.config, head),
		}, cur)
		handleOK(t, s, open)

		if s.env != nil {
			t.Fatal("wrong height must skip")
		}

		if s.parked != nil {
			t.Fatal("the skip must void the parked speculative state")
		}
	})

	t.Run("producer rebuild replaces the in-progress block", func(t *testing.T) {
		s := h.session()
		cur := handleOK(t, s, openOn(head, h.config, commitment.Head{0x01}))
		cur = handleOK(t, s, recordEntry(h.rawTransfer(t, 0), cur))

		tx1Hash := h.transfer(t, 0).Hash()
		if _, _, ok := s.consumer.index.Lookup(tx1Hash); !ok {
			t.Fatal("first generation's receipt should be indexed")
		}

		// A second open at the same height: the first generation is voided
		// and the new one re-anchors on the canonical parent.
		cur = handleOK(t, s, openOn(head, h.config, cur))

		if s.env == nil {
			t.Fatal("rebuild on a canonical parent must re-anchor")
		}

		if _, _, ok := s.consumer.index.Lookup(tx1Hash); ok {
			t.Fatal("rebuild must clear the voided generation's receipts")
		}

		cur = handleOK(t, s, recordEntry(h.rawTransfer(t, 0), cur))
		sealed := sealedFromEnv(t, s)
		handleOK(t, s, sealEntry(encodeHeader(t, sealed), cur))

		if _, _, ok := s.consumer.index.Lookup(tx1Hash); !ok {
			t.Fatal("rebuilt generation must serve its receipt")
		}
	})

	t.Run("divergent execution voids the block", func(t *testing.T) {
		s := h.session()
		cur := handleOK(t, s, openOn(head, h.config, commitment.Head{0x01}))
		cur = handleOK(t, s, recordEntry(h.rawTransfer(t, 0), cur))
		// Nonce 9 cannot apply on this state: re-execution diverges.
		handleOK(t, s, recordEntry(h.rawTransfer(t, 9), cur))

		if s.env != nil {
			t.Fatal("failed apply must void the block")
		}

		if _, _, ok := s.consumer.index.Lookup(h.transfer(t, 0).Hash()); ok {
			t.Fatal("voiding must clear the height's receipts")
		}
	})

	t.Run("undecodable transaction bytes", func(t *testing.T) {
		s := h.session()
		cur := handleOK(t, s, openOn(head, h.config, commitment.Head{0x01}))
		handleOK(t, s, recordEntry([]byte{0xff, 0xfe}, cur))

		if s.env != nil {
			t.Fatal("garbage tx bytes must void the block")
		}
	})

	t.Run("undecodable sealed header", func(t *testing.T) {
		s := h.session()
		cur := handleOK(t, s, openOn(head, h.config, commitment.Head{0x01}))
		handleOK(t, s, sealEntry([]byte{0x01, 0x02}, cur))

		if s.env != nil {
			t.Fatal("garbage seal must void the block")
		}
	})

	t.Run("seal cross-check failure", func(t *testing.T) {
		s := h.session()
		cur := handleOK(t, s, openOn(head, h.config, commitment.Head{0x01}))
		cur = handleOK(t, s, recordEntry(h.rawTransfer(t, 0), cur))

		sealed := sealedFromEnv(t, s)
		sealed.GasUsed++
		handleOK(t, s, sealEntry(encodeHeader(t, sealed), cur))

		if s.env != nil || s.parked != nil {
			t.Fatal("mismatched seal must void, not park")
		}
	})

	t.Run("records and seals while skipping are ignored", func(t *testing.T) {
		s := h.session()
		cur := handleOK(t, s, recordEntry(h.rawTransfer(t, 0), commitment.Head{0x01}))
		handleOK(t, s, sealEntry(encodeHeader(t, head), cur))

		if s.env != nil || len(s.consumer.index.byHash) != 0 {
			t.Fatal("entries without an open must be no-ops")
		}
	})
}

// checkSeal distinguishes context, gas, receipts, and state divergence.
func TestCheckSealMismatches(t *testing.T) {
	h := startExecHarness(t)
	s := h.session()
	head := h.chain.CurrentBlock()

	cur := handleOK(t, s, openOn(head, h.config, commitment.Head{0x01}))
	handleOK(t, s, recordEntry(h.rawTransfer(t, 0), cur))

	good := sealedFromEnv(t, s)
	if err := s.env.checkSeal(good); err != nil {
		t.Fatalf("self-consistent seal must pass: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*types.Header)
		want   string
	}{
		{"number", func(h *types.Header) { h.Number.Add(h.Number, big.NewInt(1)) }, "open context mismatch"},
		{"time", func(h *types.Header) { h.Time++ }, "open context mismatch"},
		{"parent", func(h *types.Header) { h.ParentHash[0] ^= 1 }, "open context mismatch"},
		{"gas limit", func(h *types.Header) { h.GasLimit++ }, "open context mismatch"},
		{"base fee", func(h *types.Header) { h.BaseFee.Add(h.BaseFee, big.NewInt(1)) }, "open context mismatch"},
		{"gas used", func(h *types.Header) { h.GasUsed++ }, "gas used"},
		{"receipts root", func(h *types.Header) { h.ReceiptHash[0] ^= 1 }, "receipts root"},
		{"state root", func(h *types.Header) { h.Root[0] ^= 1 }, "state root"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := types.CopyHeader(good)
			tc.mutate(tampered)

			err := s.env.checkSeal(tampered)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q divergence, got %v", tc.want, err)
			}
		})
	}
}

// BLOCKHASH resolves speculative ancestors from the session's sealed map,
// canonical blocks from the chain, and everything else to zero.
func TestBlockEnvSpeculativeBlockhash(t *testing.T) {
	h := startExecHarness(t)
	head := h.chain.CurrentBlock()
	number := head.Number.Uint64()

	statedb, err := h.chain.StateAt(head.Root)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	speculativeHash := common.Hash{0x5e, 0xed}
	open := &pb.BlockOpen{
		BlockNumber:    number + 2,
		BlockTimestamp: head.Time + 4,
		ParentHash:     speculativeHash.Bytes(),
		GasLimit:       head.GasLimit,
		BaseFee:        big.NewInt(params.InitialBaseFee).Bytes(),
	}

	env := newBlockEnv(h.chain, statedb, open, map[uint64]common.Hash{number + 1: speculativeHash})

	if got := env.evm.Context.GetHash(number + 1); got != speculativeHash {
		t.Fatalf("speculative ancestor resolved to %s", got)
	}

	if got := env.evm.Context.GetHash(1); got != h.chain.GetCanonicalHash(1) {
		t.Fatalf("canonical ancestor resolved to %s", got)
	}

	if got := env.evm.Context.GetHash(number + 10); got != (common.Hash{}) {
		t.Fatalf("unknown height resolved to %s", got)
	}
}

func TestEffectiveGasPrice(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := types.LatestSignerForChainID(big.NewInt(1))
	to := common.Address{0x01}

	legacy := types.MustSignNewTx(key, signer, &types.LegacyTx{
		To: &to, Gas: 21000, GasPrice: big.NewInt(40),
	})
	dynamic := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID: big.NewInt(1), To: &to, Gas: 21000,
		GasFeeCap: big.NewInt(100), GasTipCap: big.NewInt(7),
	})

	if got := effectiveGasPrice(legacy, nil); got.Cmp(big.NewInt(40)) != 0 {
		t.Fatalf("nil base fee must fall back to the gas price, got %v", got)
	}

	if got := effectiveGasPrice(dynamic, big.NewInt(30)); got.Cmp(big.NewInt(37)) != 0 {
		t.Fatalf("dynamic fee must be tip plus base, got %v", got)
	}

	if got := effectiveGasPrice(legacy, big.NewInt(30)); got.Cmp(big.NewInt(40)) != 0 {
		t.Fatalf("legacy under base fee must cap at its gas price, got %v", got)
	}
}
