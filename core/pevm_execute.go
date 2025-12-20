package core

/*
#cgo CFLAGS: -I/Users/raneet/Desktop/pevm/crates/pevm-ffi/include
#cgo LDFLAGS: -L/Users/raneet/Desktop/pevm/target/debug -lpevm_ffi
#include "pevm_ffi.h"
#include <stdlib.h>
PevmStorageVTable pevm_make_vtable(void);
*/
import "C"
import (
	"fmt"
	"math/big"
	"runtime"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/pevmbridge"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// u256ToC converts a big.Int to C.PevmU256 (must be in same package as CGO types)
func u256ToC(x *big.Int) (out C.PevmU256) {
	if x == nil {
		return
	}
	b := x.FillBytes(make([]byte, 32))
	for i := 0; i < 32 && i < len(b); i++ {
		out.be[i] = C.uchar(b[i])
	}
	return
}

// ExecuteBlockWithPEVM executes a block using PEVM parallel execution
// useParallel: parameter is kept for API compatibility but always uses parallel execution (ignored)
// chain and engine can be nil if not needed for finalization
func ExecuteBlockWithPEVM(
	header *types.Header,
	block *types.Block,
	statedb *state.StateDB,
	chain *BlockChain,
	engine consensus.Engine,
	cfg *params.ChainConfig,
	_ bool, // useParallel - kept for API compatibility but always uses parallel execution
) (*ProcessResult, error) {
	// Create storage provider wrapper on-demand for FFI calls
	stProv := pevmbridge.NewStateProvider(statedb)

	// Build block env
	var blockEnv C.PevmBlockEnv
	coinbaseBytes := header.Coinbase.Bytes()
	for i := 0; i < 20 && i < len(coinbaseBytes); i++ {
		blockEnv.coinbase.be[i] = C.uchar(coinbaseBytes[i])
	}
	if header.BaseFee != nil {
		blockEnv.basefee = u256ToC(header.BaseFee)
	}
	// blob_basefee left zero for now
	blockEnv.gas_limit = C.ulonglong(header.GasLimit)
	if header.Number != nil {
		blockEnv.number = C.ulonglong(header.Number.Uint64())
	}
	blockEnv.timestamp = C.ulonglong(header.Time)
	prevRandaoBytes := header.MixDigest.Bytes()
	for i := 0; i < 32 && i < len(prevRandaoBytes); i++ {
		blockEnv.prev_randao.be[i] = C.uchar(prevRandaoBytes[i])
	}
	blockEnv.chain_id = C.ulonglong(cfg.ChainID.Uint64())

	// Build tx envs
	txs := block.Transactions()
	txEnvs := make([]C.PevmTxEnv, 0, len(txs))
	txMessages := make([]*Message, 0, len(txs))        // Store for logging
	processedTxs := make([]*types.Transaction, 0, len(txs)) // Store transactions for fee calculation
	for _, tx := range txs {
		if tx.Type() == types.StateSyncTxType {
			continue
		}
		msg, err := TransactionToMessage(tx, types.MakeSigner(cfg, header.Number, header.Time), header.BaseFee)
		if err != nil {
			return nil, err
		}
		txMessages = append(txMessages, msg)
		processedTxs = append(processedTxs, tx)
		var te C.PevmTxEnv
		callerBytes := msg.From.Bytes()
		for i := 0; i < 20 && i < len(callerBytes); i++ {
			te.caller.be[i] = C.uchar(callerBytes[i])
		}
		if msg.To != nil {
			te.has_to = 1
			toBytes := msg.To.Bytes()
			for i := 0; i < 20 && i < len(toBytes); i++ {
				te.to.be[i] = C.uchar(toBytes[i])
			}
		}
		if len(msg.Data) > 0 {
			te.data.ptr = (*C.uchar)(unsafe.Pointer(&msg.Data[0]))
			te.data.len = C.uintptr_t(len(msg.Data))
		} else {
			te.data.ptr = nil
			te.data.len = 0
		}
		te.value = u256ToC(msg.Value)
		te.gas_limit = C.ulonglong(tx.Gas())
		if tx.Type() == types.LegacyTxType {
			te.gas_price = u256ToC(msg.GasPrice)
		} else {
			te.has_max_fee_per_gas = 1
			te.max_fee_per_gas = u256ToC(msg.GasFeeCap)
			te.has_max_priority_fee_per_gas = 1
			te.max_priority_fee_per_gas = u256ToC(msg.GasTipCap)
		}
		te.has_nonce = 1
		te.nonce = C.ulonglong(tx.Nonce())
		txEnvs = append(txEnvs, te)
	}

	// Prepare vtable and context
	ctxID := pevmbridge.RegisterContext(stProv)
	defer pevmbridge.UnregisterContext(ctxID)

	vtable := C.pevm_make_vtable()

	var out *C.PevmExecuteResult
	var errC C.PevmFfiError
	var ok C.int

	// Always use parallel execution (execute_revm_parallel)
	concurrencyLevel := runtime.NumCPU()
	if concurrencyLevel < 1 {
		concurrencyLevel = 1
	}
	ok = C.pevm_ffi_execute_block_parallel(
		&vtable,
		unsafe.Pointer(uintptr(ctxID)),
		(*C.PevmBlockEnv)(unsafe.Pointer(&blockEnv)),
		(*C.PevmTxEnv)(unsafe.Pointer(&txEnvs[0])),
		C.uintptr_t(len(txEnvs)),
		C.uint32_t(concurrencyLevel),
		(**C.PevmExecuteResult)(unsafe.Pointer(&out)),
		(*C.PevmFfiError)(unsafe.Pointer(&errC)),
	)
	if ok == 0 {
		defer C.pevm_ffi_string_free(errC.message)
		errMsg := C.GoString(errC.message)
		return nil, fmt.Errorf("pevm parallel execution failed: %s", errMsg)
	}
	defer C.pevm_ffi_execute_result_free(out)

	// Apply writes and build receipts/logs
	return applyPEVMResults(out, processedTxs, txMessages, header, block, statedb, chain, engine, cfg)
}

// applyPEVMResults applies PEVM execution results to StateDB and builds receipts/logs
func applyPEVMResults(
	out *C.PevmExecuteResult,
	processedTxs []*types.Transaction,
	txMessages []*Message,
	header *types.Header,
	block *types.Block,
	statedb *state.StateDB,
	chain *BlockChain,
	engine consensus.Engine,
	cfg *params.ChainConfig,
) (*ProcessResult, error) {
	var receipts types.Receipts
	var allLogs []*types.Log
	var usedGas uint64

	count := int(out.txs_len)
	var prevCum uint64
	var cumulativeCoinbaseBalance uint256.Int // Track cumulative coinbase balance across all transactions
	for i := 0; i < count; i++ {
		txRes := (*C.PevmTxResult)(unsafe.Add(unsafe.Pointer(out.txs), i*int(unsafe.Sizeof(*out.txs))))
		coinbase := header.Coinbase

		// Apply account writes
		awCount := int(txRes.account_writes_len)
		for j := 0; j < awCount; j++ {
			aw := (*C.PevmAccountWrite)(unsafe.Add(unsafe.Pointer(txRes.account_writes), j*int(unsafe.Sizeof(*txRes.account_writes))))
			var addr common.Address
			copy(addr[:], C.GoBytes(unsafe.Pointer(&aw.address.be[0]), 20))
			balBytes := C.GoBytes(unsafe.Pointer(&aw.balance.be[0]), 32)
			bal := uint256.MustFromBig(new(big.Int).SetBytes(balBytes))
			if aw.present == 0 {
				statedb.SelfDestruct(addr)
				continue
			}
			// Skip coinbase balance update when balance is 0 (to preserve cumulative fees)
			// But still apply nonce/code/storage to match PEVM's account writes exactly
			skipCoinbaseBalance := (addr == coinbase && bal.IsZero())

			// Ensure account exists before setting balance/nonce
			if !statedb.Exist(addr) {
				statedb.CreateAccount(addr)
			}
			// balance (skip for coinbase when balance is 0 to preserve cumulative fees)
			if !skipCoinbaseBalance {
				statedb.SetBalance(addr, bal, tracing.BalanceChangeUnspecified)
			}
			// nonce
			statedb.SetNonce(addr, uint64(aw.nonce), tracing.NonceChangeUnspecified)
			// code
			if aw.has_code != 0 {
				code := C.GoBytes(unsafe.Pointer(aw.code.ptr), C.int(aw.code.len))
				statedb.SetCode(addr, code)
			}
			// storage writes
			swCount := int(aw.storage_writes_len)
			for k := 0; k < swCount; k++ {
				sw := (*C.PevmStorageWrite)(unsafe.Add(unsafe.Pointer(aw.storage_writes), k*int(unsafe.Sizeof(*aw.storage_writes))))
				var slot common.Hash
				copy(slot[:], C.GoBytes(unsafe.Pointer(&sw.slot.be[0]), 32))
				var val common.Hash
				copy(val[:], C.GoBytes(unsafe.Pointer(&sw.value.be[0]), 32))
				statedb.SetState(addr, slot, val)
			}
		}

		// Receipt
		r := txRes.receipt
		usedGas = uint64(r.cumulative_gas_used)
		gasDelta := usedGas - prevCum
		prevCum = usedGas

		// Always apply transaction fees to coinbase BEFORE Finalise()
		// REVM applies fees through rewards mechanism, but PEVM doesn't capture them in account writes
		// So we need to manually calculate and apply fees
		// Apply fees before Finalise() so coinbase balance is included in state finalization
		if i < len(processedTxs) && gasDelta > 0 {
			tx := processedTxs[i]
			var fee *big.Int
			if tx.Type() == types.LegacyTxType {
				// Legacy: fee = gas_used * gas_price
				fee = new(big.Int).Mul(big.NewInt(int64(gasDelta)), tx.GasPrice())
			} else {
				// EIP-1559: fee = gas_used * effective_gas_price (min(max_fee_per_gas, basefee + priority_fee))
				effectiveGasPrice := tx.GasFeeCap()
				if header.BaseFee != nil {
					priorityFee := tx.GasTipCap()
					effectiveGasPrice = new(big.Int).Add(header.BaseFee, priorityFee)
					if effectiveGasPrice.Cmp(tx.GasFeeCap()) > 0 {
						effectiveGasPrice = tx.GasFeeCap()
					}
				}
				fee = new(big.Int).Mul(big.NewInt(int64(gasDelta)), effectiveGasPrice)
			}
			if fee.Sign() > 0 {
				// Accumulate fees in cumulative balance tracker
				feeU256 := uint256.MustFromBig(fee)
				var newBal uint256.Int
				newBal.Add(&cumulativeCoinbaseBalance, feeU256)
				cumulativeCoinbaseBalance = newBal
				// Apply fee to coinbase account before Finalise()
				// Don't explicitly create account - let SetBalance create it naturally (matching baseline behavior)
				statedb.SetBalance(coinbase, &cumulativeCoinbaseBalance, tracing.BalanceChangeUnspecified)
			}
		}

		// Finalise after each transaction (matching baseline behavior)
		statedb.Finalise(cfg.IsEIP158(header.Number))

		// Restore cumulative coinbase balance after Finalise() (which may have cleared it due to account writes with balance=0)
		if !cumulativeCoinbaseBalance.IsZero() {
			if !statedb.Exist(coinbase) {
				statedb.CreateAccount(coinbase)
			}
			statedb.SetBalance(coinbase, &cumulativeCoinbaseBalance, tracing.BalanceChangeUnspecified)
		}

		receipt := &types.Receipt{
			Status:            uint64(r.status),
			CumulativeGasUsed: usedGas,
			GasUsed:           gasDelta,
		}
		// Logs
		logCount := int(r.logs_len)
		for j := 0; j < logCount; j++ {
			cl := (*C.PevmLog)(unsafe.Add(unsafe.Pointer(r.logs), j*int(unsafe.Sizeof(*r.logs))))
			var addr common.Address
			copy(addr[:], C.GoBytes(unsafe.Pointer(&cl.address.be[0]), 20))
			topics := make([]common.Hash, int(cl.topics_len))
			for k := 0; k < int(cl.topics_len); k++ {
				ct := (*C.PevmTopic)(unsafe.Add(unsafe.Pointer(cl.topics), k*int(unsafe.Sizeof(*cl.topics))))
				copy(topics[k][:], C.GoBytes(unsafe.Pointer(&ct.topic.be[0]), 32))
			}
			data := C.GoBytes(unsafe.Pointer(cl.data.ptr), C.int(cl.data.len))
			log := &types.Log{
				Address:     addr,
				Topics:      topics,
				Data:        data,
				BlockNumber: header.Number.Uint64(),
				// TxHash omitted for FFI path
				TxIndex:   uint(i),
				Index:     uint(j),
				BlockHash: block.Hash(),
			}
			receipt.Logs = append(receipt.Logs, log)
			allLogs = append(allLogs, log)
		}
		receipt.Bloom = types.CreateBloom(receipt)
		receipt.BlockHash = block.Hash()
		receipt.BlockNumber = header.Number
		receipt.TransactionIndex = uint(i)
		receipts = append(receipts, receipt)
	}

	// Finalize state (required for state root calculation)
	statedb.Finalise(cfg.IsEIP158(header.Number))

	// Restore cumulative coinbase balance after final Finalise() (which may have cleared it)
	if !cumulativeCoinbaseBalance.IsZero() {
		if !statedb.Exist(header.Coinbase) {
			statedb.CreateAccount(header.Coinbase)
		}
		statedb.SetBalance(header.Coinbase, &cumulativeCoinbaseBalance, tracing.BalanceChangeUnspecified)
	}

	// Note: engine.Finalize() is NOT called here because it's called by StateProcessor.Process()
	// or ParallelStateProcessor.Process() after ExecuteBlockWithPEVM returns.
	// This ensures block rewards are applied consistently with the baseline execution path.

	return &ProcessResult{
		Receipts: receipts,
		Requests: nil, // PEVM doesn't handle requests yet
		Logs:     allLogs,
		GasUsed:  usedGas,
	}, nil
}

