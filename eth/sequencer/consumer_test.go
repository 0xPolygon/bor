package sequencer

import (
	"math/big"
	"testing"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// foldChain tracks the producer-side running head while rendering blocks as
// entries. Its seed is arbitrary: the session adopts the first entry's
// prefix, per the cold-start rule.
type foldChain struct {
	t    *testing.T
	head commitment.Head
}

func (c *foldChain) foldOpenFromHeader(header *types.Header) {
	c.t.Helper()

	head, err := commitment.FoldOpen(c.head, commitment.OpenContext{
		Number:     header.Number.Uint64(),
		Timestamp:  header.Time,
		ParentHash: header.ParentHash,
		GasLimit:   header.GasLimit,
		BaseFee:    header.BaseFee,
	})
	if err != nil {
		c.t.Fatalf("FoldOpen: %v", err)
	}

	c.head = head
}

func (c *foldChain) foldTx(raw []byte) {
	c.head = commitment.FoldTx(c.head, raw)
}

func (c *foldChain) foldSeal(rawHeader []byte) {
	c.head = commitment.FoldSeal(c.head, commitment.SealedHash(rawHeader))
}

// entriesForBlock renders one canonical block as its stream entries, chaining
// prefixes exactly as a producer would.
func entriesForBlock(t *testing.T, c *foldChain, block *types.Block) []*pb.Entry {
	t.Helper()

	header := block.Header()
	open := &pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{
		BlockNumber:      header.Number.Uint64(),
		BlockTimestamp:   header.Time,
		ParentHash:       header.ParentHash.Bytes(),
		GasLimit:         header.GasLimit,
		BaseFee:          header.BaseFee.Bytes(),
		PrefixCommitment: c.head.Bytes(),
	}}}
	c.foldOpenFromHeader(header)

	entries := []*pb.Entry{open}

	for _, tx := range block.Transactions() {
		raw, err := tx.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal tx: %v", err)
		}

		entries = append(entries, &pb.Entry{Kind: &pb.Entry_Record{Record: &pb.Record{
			Transactions:     [][]byte{raw},
			PrefixCommitment: c.head.Bytes(),
		}}})
		c.foldTx(raw)
	}

	rawHeader, err := rlp.EncodeToBytes(header)
	if err != nil {
		t.Fatalf("rlp header: %v", err)
	}

	entries = append(entries, &pb.Entry{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{
		Header:           rawHeader,
		PrefixCommitment: c.head.Bytes(),
	}}})
	c.foldSeal(rawHeader)

	return entries
}

// The reviewer-mandated gate: real blocks flow through the session and the
// preconf receipts must equal the canonical receipts the chain derives after
// import — field by field.
func TestSessionReceiptsParity(t *testing.T) {
	// MergedTestChainConfig: every fork at 0 (Prague included, so EIP-2935
	// runs on both sides) with a Bor config carrying the burnt-contract map,
	// so bor fee flows execute identically in GenerateChain and the session.
	// A coinbase map is added — the consumer requires it for a deterministic
	// beneficiary — and GenerateChain produces with that same address.
	producerCoinbase := common.HexToAddress("0x00000000000000000000000000000000000000fe")
	config := new(params.ChainConfig)
	*config = *params.MergedTestChainConfig
	bor := *config.Bor
	bor.Coinbase = map[string]string{"0": producerCoinbase.Hex()}
	config.Bor = &bor

	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	recipient := common.Address{0xaa}

	genesis := &core.Genesis{
		Config:   config,
		GasLimit: 30_000_000,
		BaseFee:  big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			sender: {Balance: new(big.Int).Mul(big.NewInt(1000), big.NewInt(params.Ether))},
		},
	}

	engine := beacon.NewFaker()
	db := rawdb.NewMemoryDatabase()

	blockchain, err := core.NewBlockChain(db, genesis, engine, nil)
	if err != nil {
		t.Fatalf("NewBlockChain: %v", err)
	}

	defer blockchain.Stop()

	signer := types.LatestSigner(config)
	nonce := uint64(0)

	blocks, _ := core.GenerateChain(config, blockchain.Genesis(), engine, db, 3, func(i int, g *core.BlockGen) {
		g.SetCoinbase(producerCoinbase)

		for range 2 {
			tx, terr := types.SignNewTx(key, signer, &types.DynamicFeeTx{
				ChainID:   config.ChainID,
				Nonce:     nonce,
				GasTipCap: big.NewInt(params.GWei),
				GasFeeCap: new(big.Int).Add(g.BaseFee(), big.NewInt(params.GWei)),
				Gas:       21000,
				To:        &recipient,
				Value:     big.NewInt(1),
			})
			if terr != nil {
				t.Fatalf("sign: %v", terr)
			}

			g.AddTx(tx)
			nonce++
		}
	})

	// The consumer's anchor: block 1 is canonical, blocks 2..3 arrive only
	// via the stream.
	if _, err := blockchain.InsertChain(blocks[:1], false); err != nil {
		t.Fatalf("insert anchor: %v", err)
	}

	consumer := &Consumer{chain: blockchain, config: config, index: NewIndex()}
	sess := &session{consumer: consumer}
	producerChain := &foldChain{t: t, head: commitment.Seed(1337)}

	for _, block := range blocks[1:] {
		for _, entry := range entriesForBlock(t, producerChain, block) {
			if err := sess.handle(entry); err != nil {
				t.Fatalf("handle block %d entry: %v", block.NumberU64(), err)
			}
		}

		// The seal must have survived checkSeal (a skip would have cleared
		// the index) and stamped the sealed hash.
		for _, tx := range block.Transactions() {
			receipt, _, ok := consumer.index.Lookup(tx.Hash())
			if !ok {
				t.Fatalf("block %d tx %s has no preconf receipt (seal check failed?)", block.NumberU64(), tx.Hash())
			}

			if receipt.BlockHash != block.Hash() {
				t.Fatalf("block %d preconf receipt hash %s != sealed %s", block.NumberU64(), receipt.BlockHash, block.Hash())
			}
		}
	}

	// Import the streamed blocks and compare canonical receipts against the
	// preconf ones.
	if _, err := blockchain.InsertChain(blocks[1:], false); err != nil {
		t.Fatalf("insert streamed blocks: %v", err)
	}

	for _, block := range blocks[1:] {
		canonical := blockchain.GetReceiptsByHash(block.Hash())
		if len(canonical) != len(block.Transactions()) {
			t.Fatalf("block %d: %d canonical receipts for %d txs", block.NumberU64(), len(canonical), len(block.Transactions()))
		}

		for i, tx := range block.Transactions() {
			preconf, _, ok := consumer.index.Lookup(tx.Hash())
			if !ok {
				t.Fatalf("preconf receipt for %s disappeared", tx.Hash())
			}

			assertReceiptsEqual(t, canonical[i], preconf, block.NumberU64(), i)
		}
	}
}

func assertReceiptsEqual(t *testing.T, canonical, preconf *types.Receipt, block uint64, index int) {
	t.Helper()

	check := func(name string, want, got any) {
		if want != got {
			t.Errorf("block %d tx %d: %s = %v, want %v", block, index, name, got, want)
		}
	}

	check("status", canonical.Status, preconf.Status)
	check("gasUsed", canonical.GasUsed, preconf.GasUsed)
	check("cumulativeGasUsed", canonical.CumulativeGasUsed, preconf.CumulativeGasUsed)
	check("txHash", canonical.TxHash, preconf.TxHash)
	check("contractAddress", canonical.ContractAddress, preconf.ContractAddress)
	check("blockHash", canonical.BlockHash, preconf.BlockHash)
	check("blockNumber", canonical.BlockNumber.Uint64(), preconf.BlockNumber.Uint64())
	check("transactionIndex", canonical.TransactionIndex, preconf.TransactionIndex)
	check("bloom", canonical.Bloom, preconf.Bloom)
	check("logs", len(canonical.Logs), len(preconf.Logs))

	if canonical.EffectiveGasPrice != nil &&
		(preconf.EffectiveGasPrice == nil || canonical.EffectiveGasPrice.Cmp(preconf.EffectiveGasPrice) != 0) {
		t.Errorf("block %d tx %d: effectiveGasPrice = %v, want %v", block, index, preconf.EffectiveGasPrice, canonical.EffectiveGasPrice)
	}
}
