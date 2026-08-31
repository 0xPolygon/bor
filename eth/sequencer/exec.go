package sequencer

import (
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/trie"
)

// blockEnv re-executes one speculative block on top of a parent state,
// mirroring the producer's environment: same header context from the open
// record, author nil so the EVM coinbase resolves to the producer-independent
// CalculateCoinbase (post-Rio), difficulty constant 1 under VEBLOP, and the
// same pre-transaction system calls the producer runs (EIP-2935 post-Prague).
type blockEnv struct {
	generation   uint64
	cacheable    bool
	interrupt    atomic.Bool
	header       *types.Header
	statedb      *state.StateDB
	evm          *vm.EVM
	gasPool      *core.GasPool
	inputBytes   uint64
	txs          []*types.Transaction
	receipts     []*types.Receipt
	publishedGas uint64
	publishedTxs int
}

// newBlockEnv builds the execution environment. speculative maps heights of
// sealed-but-not-yet-imported ancestors to their sealed hashes, so BLOCKHASH
// resolves them exactly as the producer did — the canonical header walk
// returns zero for blocks the chain hasn't imported.
func newBlockEnv(chain *core.BlockChain, statedb *state.StateDB, open *pb.BlockOpen, speculative map[uint64]common.Hash) *blockEnv {
	header := pendingHeader(open)

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
		cacheable: true,
		header:    header,
		statedb:   statedb,
		evm:       vm.NewEVM(blockCtx, statedb, chain.Config(), vm.Config{}),
		gasPool:   new(core.GasPool).AddGas(header.GasLimit),
	}
	env.evm.SetInterrupt(&env.interrupt)

	if chain.Config().IsPrague(header.Number) {
		core.ProcessParentBlockHash(header.ParentHash, env.evm)
	}
	if chain.GetVMConfig().StatelessSelfValidation {
		witness, err := stateless.NewWitness(header, chain)
		if err != nil {
			log.Warn("Failed to create speculative execution witness", "number", header.Number, "err", err)
		} else {
			statedb.SetWitness(witness)
		}
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
	return env.applyTransaction(tx)
}

func (env *blockEnv) applyTransaction(tx *types.Transaction) (*types.Transaction, *types.Receipt, error) {
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

func newBlockEnvFromPrefix(chain *core.BlockChain, block *types.Block, prefix *pendingPrefix) (*blockEnv, error) {
	if block == nil || prefix == nil || prefix.StateDB == nil || prefix.Result == nil {
		return nil, errors.New("incomplete preconfirmation prefix")
	}
	if prefix.StateDB.Error() != nil || len(prefix.Transactions) != len(prefix.Result.Receipts) ||
		len(prefix.Transactions) > len(block.Transactions()) || prefix.Result.GasUsed > block.GasLimit() {
		return nil, errors.New("invalid preconfirmation prefix")
	}
	header := block.Header()
	header.GasUsed = prefix.Result.GasUsed
	gasPool := new(core.GasPool).AddGas(header.GasLimit)
	if err := gasPool.SubGas(prefix.Result.GasUsed); err != nil {
		return nil, err
	}
	statedb := prefix.StateDB
	env := &blockEnv{
		cacheable: true,
		header:    header,
		statedb:   statedb,
		evm:       vm.NewEVM(core.NewEVMBlockContext(header, chain, nil), statedb, chain.Config(), vm.Config{}),
		gasPool:   gasPool,
		txs:       append(types.Transactions(nil), prefix.Transactions...),
		receipts:  cloneReceipts(prefix.Result.Receipts),
	}
	env.evm.SetInterrupt(&env.interrupt)
	return env, nil
}

func (c *Consumer) completePreconfPrefix(block *types.Block, prefix *pendingPrefix) (*core.PreconfExecution, error) {
	env, err := newBlockEnvFromPrefix(c.chain, block, prefix)
	if err != nil {
		return nil, err
	}
	for _, tx := range block.Transactions()[len(prefix.Transactions):] {
		if tx.Type() == types.StateSyncTxType {
			return nil, errors.New("state-sync transaction cannot use a preconfirmation prefix")
		}
		if _, _, err := env.applyTransaction(tx); err != nil {
			return nil, err
		}
	}
	_, reusable, err := env.finalizeSeal(c.chain, block.Header())
	if err != nil {
		return nil, err
	}
	if reusable == nil {
		return nil, errors.New("completed preconfirmation is not reusable")
	}
	if len(reusable.Result.Receipts) != len(block.Transactions()) {
		return nil, errors.New("completed preconfirmation receipt count mismatch")
	}
	canonicalizeProcessResult(block, reusable.Result)
	return &core.PreconfExecution{StateDB: reusable.StateDB, Result: reusable.Result}, nil
}

func canonicalizeProcessResult(block *types.Block, result *core.ProcessResult) {
	if result == nil {
		return
	}
	logs := make([]*types.Log, 0)
	logIndex := uint(0)
	for txIndex, receipt := range result.Receipts {
		receipt.BlockHash = block.Hash()
		receipt.BlockNumber = block.Number()
		receipt.TransactionIndex = uint(txIndex)
		for _, entry := range receipt.Logs {
			entry.BlockHash = block.Hash()
			entry.BlockNumber = block.NumberU64()
			entry.TxIndex = uint(txIndex)
			entry.Index = logIndex
			logIndex++
			logs = append(logs, entry)
		}
	}
	result.Logs = logs
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
var errCachedFinalizationUnavailable = errors.New("cached sprint finalization input unavailable")

type speculativeFinalizationChain struct {
	*core.BlockChain
}

func (*speculativeFinalizationChain) SetStateSync([]*types.StateSyncData) {}

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

func (env *blockEnv) finalizeSeal(chain *core.BlockChain, sealed *types.Header) (*types.Block, *ReusableExecution, error) {
	return env.finalizeSealWithHeaders(chain, chain, sealed)
}

func (env *blockEnv) finalizeSealWithHeaders(chain *core.BlockChain, headers consensus.ChainHeaderReader, sealed *types.Header) (*types.Block, *ReusableExecution, error) {
	if err := chain.Engine().VerifyHeader(headers, sealed); err != nil {
		return nil, nil, fmt.Errorf("verify sealed header: %w", err)
	}
	return env.finalizeVerifiedSeal(chain, sealed)
}

func (env *blockEnv) finalizeVerifiedSeal(chain *core.BlockChain, sealed *types.Header) (*types.Block, *ReusableExecution, error) {
	switch {
	case sealed.Number.Cmp(env.header.Number) != 0,
		sealed.Time != env.header.Time,
		sealed.ParentHash != env.header.ParentHash,
		sealed.GasLimit != env.header.GasLimit,
		!bigEqual(sealed.BaseFee, env.header.BaseFee):
		return nil, nil, fmt.Errorf("%w: open context mismatch at block %s", errSealMismatch, sealed.Number)
	case sealed.GasUsed != env.header.GasUsed:
		return nil, nil, fmt.Errorf("%w: gas used %d != re-executed %d at block %s",
			errSealMismatch, sealed.GasUsed, env.header.GasUsed, sealed.Number)
	}
	sprint := chain.Config().Bor.CalculateSprint(sealed.Number.Uint64())
	if sprint == 0 || sealed.Number.Uint64()%sprint == 0 {
		return nil, nil, errCachedFinalizationUnavailable
	}
	body := &types.Body{Transactions: append(types.Transactions(nil), env.txs...)}
	workState := env.statedb.Copy()
	finalizationChain := &speculativeFinalizationChain{BlockChain: chain}
	assembled, receipts, _, err := chain.Engine().FinalizeAndAssemble(finalizationChain, types.CopyHeader(sealed), workState, body, cloneReceipts(env.receipts))
	if err != nil {
		return nil, nil, fmt.Errorf("finalize streamed block: %w", err)
	}
	if assembled.Hash() != sealed.Hash() || assembled.TxHash() != sealed.TxHash || assembled.Root() != sealed.Root || assembled.ReceiptHash() != sealed.ReceiptHash || assembled.Bloom() != sealed.Bloom || assembled.GasUsed() != sealed.GasUsed {
		return nil, nil, fmt.Errorf("%w: finalized result mismatch at block %s", errSealMismatch, sealed.Number)
	}
	logs := make([]*types.Log, 0)
	for _, receipt := range receipts {
		logs = append(logs, receipt.Logs...)
	}
	result := &core.ProcessResult{Receipts: cloneReceipts(receipts), Logs: logs, GasUsed: sealed.GasUsed}
	canonicalizeProcessResult(assembled, result)
	var reusable *ReusableExecution
	if env.cacheable {
		reusable = &ReusableExecution{
			HeaderHash: sealed.Hash(),
			TxRoot:     assembled.TxHash(),
			StateDB:    workState.Copy(),
			Result:     result,
		}
	}
	env.header = assembled.Header()
	env.statedb = workState
	env.evm.StateDB = workState
	env.txs = append(types.Transactions(nil), assembled.Transactions()...)
	env.receipts = cloneReceipts(receipts)
	return assembled, reusable, nil
}

func (s *session) executeRecord(record *pb.Record) {
	for _, raw := range record.GetTransactions() {
		if s.env.interrupt.Load() {
			s.clearEnv()
			s.parked = nil
			return
		}
		start := time.Now()
		_, _, err := s.env.applyRaw(raw)
		if err != nil {
			if s.env.interrupt.Load() {
				s.clearEnv()
				s.parked = nil
				return
			}
			s.skip(s.env.header.Number.Uint64(), "re-execution diverged", "err", err)
			return
		}
		preconfApplyTimer.UpdateSince(start)
	}
	if s.env.interrupt.Load() {
		s.clearEnv()
		s.parked = nil
		return
	}
	if !s.env.shouldPublishPending() {
		return
	}
	block, payload, ok := preparePending(s.env, s.env.header, common.Hash{}, nil)
	if !ok {
		s.clearEnv()
		s.parked = nil
		return
	}
	s.consumer.publishMu.Lock()
	if !s.consumer.publishPending(block, payload, s.env.generation) {
		s.clearEnv()
		s.parked = nil
		s.consumer.publishMu.Unlock()
		return
	}
	logs := s.indexPublishedTransactions()
	s.consumer.enqueuePendingLogs(logs)
	s.consumer.publishMu.Unlock()
}

func (env *blockEnv) shouldPublishPending() bool {
	if len(env.txs) == env.publishedTxs {
		return false
	}
	if len(env.txs) <= pendingEagerPublicationTxs {
		return true
	}
	// Keep small pending blocks immediately visible while bounding expensive
	// StateDB snapshots for high-volume blocks to a fixed number of checkpoints.
	step := env.header.GasLimit / pendingRPCPublicationLimit
	if env.header.GasLimit%pendingRPCPublicationLimit != 0 {
		step++
	}
	if step < 21_000 {
		step = 21_000
	}
	return env.header.GasUsed >= env.publishedGas && env.header.GasUsed-env.publishedGas >= step
}

func (s *session) indexPublishedTransactions() []*types.Log {
	var logs []*types.Log
	for index := s.env.publishedTxs; index < len(s.env.txs); index++ {
		s.consumer.index.Add(s.env.txs[index], s.env.receipts[index])
		logs = append(logs, s.env.receipts[index].Logs...)
	}
	s.env.publishedTxs = len(s.env.txs)
	s.env.publishedGas = s.env.header.GasUsed
	return logs
}

func (s *session) sealResult(sealed *types.Header) (*types.Block, *ReusableExecution, bool) {
	headers := &speculativeHeaderChain{ChainHeaderReader: s.consumer.chain, headers: s.verified}
	if err := s.consumer.chain.Engine().VerifyHeader(headers, sealed); err != nil {
		s.skip(s.env.header.Number.Uint64(), "sealed header verification failed", "err", err)
		return nil, nil, false
	}
	assembled, reusable, err := s.env.finalizeVerifiedSeal(s.consumer.chain, sealed)
	if err == nil {
		return assembled, reusable, true
	}
	checkErr := s.env.checkSeal(sealed)
	if checkErr != nil {
		s.skip(s.env.header.Number.Uint64(), "seal cross-check failed", "err", checkErr)
		return nil, nil, false
	}
	assembled, err = blockFromExecution(sealed, s.env.txs, s.env.receipts)
	if err != nil || assembled.Hash() != sealed.Hash() {
		s.skip(s.env.header.Number.Uint64(), "sealed block body mismatch", "err", err)
		return nil, nil, false
	}
	return assembled, nil, true
}

func bigEqual(a, b *big.Int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return a.Cmp(b) == 0
}
