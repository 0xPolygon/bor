package sequencer

import (
	"errors"
	"fmt"
	"math/big"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/trie"
)

// blockEnv re-executes one speculative block on top of a parent state,
// mirroring the producer's environment: same header context from the open
// record, author nil so the EVM coinbase resolves to the producer-independent
// CalculateCoinbase (post-Rio), difficulty constant 1 under VEBLOP, and the
// same pre-transaction system calls the producer runs (EIP-2935 post-Prague).
type blockEnv struct {
	header   *types.Header
	statedb  *state.StateDB
	evm      *vm.EVM
	gasPool  *core.GasPool
	txs      []*types.Transaction
	receipts []*types.Receipt
}

// newBlockEnv builds the execution environment. speculative maps heights of
// sealed-but-not-yet-imported ancestors to their sealed hashes, so BLOCKHASH
// resolves them exactly as the producer did — the canonical header walk
// returns zero for blocks the chain hasn't imported.
func newBlockEnv(chain *core.BlockChain, statedb *state.StateDB, open *pb.BlockOpen, speculative map[uint64]common.Hash) *blockEnv {
	header := &types.Header{
		ParentHash: common.BytesToHash(open.GetParentHash()),
		Number:     new(big.Int).SetUint64(open.GetBlockNumber()),
		GasLimit:   open.GetGasLimit(),
		Time:       open.GetBlockTimestamp(),
		BaseFee:    new(big.Int).SetBytes(open.GetBaseFee()),
		Difficulty: big.NewInt(1),
		Coinbase:   common.Address{},
	}

	blockCtx := core.NewEVMBlockContext(header, chain, nil)

	walk := blockCtx.GetHash
	blockCtx.GetHash = func(n uint64) common.Hash {
		if h, ok := speculative[n]; ok {
			return h
		}

		if h := walk(n); h != (common.Hash{}) {
			return h
		}

		// The default resolver walks parent headers and breaks at the first
		// unimported speculative ancestor; anything at or below the
		// canonical head is still resolvable directly.
		return chain.GetCanonicalHash(n)
	}

	env := &blockEnv{
		header:  header,
		statedb: statedb,
		evm:     vm.NewEVM(blockCtx, statedb, chain.Config(), vm.Config{}),
		gasPool: new(core.GasPool).AddGas(header.GasLimit),
	}

	if chain.Config().IsPrague(header.Number) {
		core.ProcessParentBlockHash(header.ParentHash, env.evm)
	}

	return env
}

// applyRaw executes one streamed raw transaction. The producer only publishes
// transactions it committed, so any failure here is a determinism divergence,
// not a bad transaction.
func (env *blockEnv) applyRaw(raw []byte) (*types.Transaction, *types.Receipt, error) {
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		return nil, nil, fmt.Errorf("decode streamed transaction: %w", err)
	}

	env.statedb.SetTxContext(tx.Hash(), len(env.txs))

	receipt, err := core.ApplyTransaction(env.evm, env.gasPool, env.statedb, env.header, tx, &env.header.GasUsed)
	if err != nil {
		return nil, nil, fmt.Errorf("re-execute tx %s: %w", tx.Hash(), err)
	}

	// The block hash is unknown pre-seal (ApplyTransaction stamped the
	// provisional unsealed header hash); zero it out until the seal record
	// arrives. EffectiveGasPrice is not populated by execution — derive it
	// the way DeriveFields would.
	receipt.BlockHash = common.Hash{}
	for _, l := range receipt.Logs {
		l.BlockHash = common.Hash{}
	}

	receipt.EffectiveGasPrice = effectiveGasPrice(tx, env.header.BaseFee)

	env.txs = append(env.txs, tx)
	env.receipts = append(env.receipts, receipt)

	return tx, receipt, nil
}

func effectiveGasPrice(tx *types.Transaction, baseFee *big.Int) *big.Int {
	if baseFee == nil {
		return tx.GasPrice()
	}

	tip, err := tx.EffectiveGasTip(baseFee)
	if err != nil {
		// Streamed txs executed successfully, so the fee cap covers the
		// base fee; this path is unreachable but must not panic.
		tip = new(big.Int)
	}

	return new(big.Int).Add(tip, baseFee)
}

var errSealMismatch = errors.New("sealed header diverges from re-execution")

// checkSeal cross-checks the sealed header against the open context this
// block was executed under and against the re-execution results, including
// the state root — the catch-all for anything execution missed. State-sync
// transactions are applied by the producer in Finalize and never enter the
// stream, and their gas and receipts live outside the header's GasUsed and
// ReceiptHash — so a sprint-start block with pending events passes the gas
// and receipts comparisons and is caught only by the state root differing.
func (env *blockEnv) checkSeal(sealed *types.Header) error {
	switch {
	case sealed.Number.Cmp(env.header.Number) != 0,
		sealed.Time != env.header.Time,
		sealed.ParentHash != env.header.ParentHash,
		sealed.GasLimit != env.header.GasLimit,
		!bigEqual(sealed.BaseFee, env.header.BaseFee):
		return fmt.Errorf("%w: open context mismatch at block %s", errSealMismatch, sealed.Number)
	case sealed.GasUsed != env.header.GasUsed:
		return fmt.Errorf("%w: gas used %d != re-executed %d at block %s",
			errSealMismatch, sealed.GasUsed, env.header.GasUsed, sealed.Number)
	}

	receiptsRoot := types.DeriveSha(types.Receipts(env.receipts), trie.NewStackTrie(nil))
	if receiptsRoot != sealed.ReceiptHash {
		return fmt.Errorf("%w: receipts root %s != re-executed %s at block %s",
			errSealMismatch, sealed.ReceiptHash, receiptsRoot, sealed.Number)
	}

	root := env.statedb.IntermediateRoot(env.evm.ChainConfig().IsEIP158(env.header.Number))
	if root != sealed.Root {
		return fmt.Errorf("%w: state root %s != re-executed %s at block %s",
			errSealMismatch, sealed.Root, root, sealed.Number)
	}

	return nil
}

func bigEqual(a, b *big.Int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return a.Cmp(b) == 0
}
