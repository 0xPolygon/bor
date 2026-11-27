package miner

import (
	"crypto/ecdsa"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

// paymentLog mirrors the PaymentLaneDiscount log the state transition emits.
func paymentLog(gas int64) *types.Log {
	data := common.LeftPadBytes(big.NewInt(gas).Bytes(), 32)
	data = append(data, make([]byte, 64)...)
	data = append(data, common.LeftPadBytes(big.NewInt(gas).Bytes(), 32)...)

	return &types.Log{
		Address: core.GetFeeAddress(),
		Topics:  []common.Hash{common.HexToHash("0x0b5685dffe5490f24515c0abaaf545f7393b5308690bfe2029a17419c97a2f06")},
		Data:    data,
	}
}

// laneTestToken stands in for a listed token: it logs Transfer(caller, to,
// amount) for transfer and Transfer(from, to, amount) otherwise, writes slot
// 0 and stops.
var laneTestToken = common.FromHex("60003560e01c63a9059cbb146047576044356000526024356004357fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a36001600055005b602435600052600435337fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a3600160005500")

// laneMinerEnv builds a worker environment on a fresh state with a funded
// sender, a burner contract (INVALID: uses all gas) and a listed token.
func laneMinerEnv(t *testing.T, capPct uint64) (*worker, *environment, *ecdsa.PrivateKey, common.Address, common.Address) {
	t.Helper()

	var (
		key, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		sender = crypto.PubkeyToAddress(key.PublicKey)
		token  = common.HexToAddress("0x70ce0001")
		burner = common.HexToAddress("0xb0b0")
	)

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(0)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{bor.PaymentLaneBlock.String(): params.DefaultPaymentLaneConfig([]common.Address{token})}
	bor.PaymentLaneAt(bor.PaymentLaneBlock).NonPaymentGasCapPercentage = capPct
	cfg.Bor = &bor

	db, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	db.AddBalance(sender, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
	db.SetCode(burner, []byte{byte(vm.INVALID)}, tracing.CodeChangeUnspecified)
	db.SetCode(token, laneTestToken, tracing.CodeChangeUnspecified)

	header := &types.Header{Number: big.NewInt(1), GasLimit: 1_000_000, BaseFee: big.NewInt(params.InitialBaseFee), Difficulty: common.Big1, Coinbase: common.HexToAddress("0xc0ffee")}
	blockCtx := vm.BlockContext{
		CanTransfer: core.CanTransfer, Transfer: core.Transfer,
		GetHash:  func(uint64) common.Hash { return common.Hash{} },
		Coinbase: header.Coinbase, BlockNumber: header.Number, Difficulty: header.Difficulty,
		BaseFee: header.BaseFee, GasLimit: header.GasLimit, PaymentLaneScaleBps: params.PaymentLaneMaxBps,
	}

	env := &environment{
		signer:  types.LatestSigner(&cfg),
		state:   db,
		header:  header,
		gasPool: new(core.GasPool).AddGas(header.GasLimit),
		evm:     vm.NewEVM(blockCtx, db, &cfg, vm.Config{}),
	}

	return &worker{chainConfig: &cfg}, env, key, token, burner
}

func laneMinerTx(t *testing.T, env *environment, key *ecdsa.PrivateKey, to common.Address, gas uint64, data []byte) *types.Transaction {
	t.Helper()

	sender := crypto.PubkeyToAddress(key.PublicKey)
	tx, err := types.SignNewTx(key, env.signer, &types.DynamicFeeTx{
		ChainID: env.signer.ChainID(), Nonce: env.state.GetNonce(sender), GasTipCap: common.Big1,
		GasFeeCap: new(big.Int).Mul(env.header.BaseFee, big.NewInt(2)), Gas: gas, To: &to, Data: data,
	})
	require.NoError(t, err)

	return tx
}

// A transaction that would break the non-payment cap must be skipped without
// touching the block being built (it used to panic reverting a finalised
// state, halting every node).
func TestCommitTransactionNonPaymentCap(t *testing.T) {
	t.Parallel()

	w, env, key, token, burner := laneMinerEnv(t, 50)
	sender := crypto.PubkeyToAddress(key.PublicKey)

	payment := []byte{0xa9, 0x05, 0x9c, 0xbb}
	payment = append(payment, common.LeftPadBytes(common.HexToAddress("0xbee").Bytes(), 32)...)
	payment = append(payment, common.LeftPadBytes([]byte{1}, 32)...)

	commit := func(tx *types.Transaction) error {
		env.state.SetTxContext(tx.Hash(), env.tcount)
		_, err := w.commitTransaction(env, tx)

		return err
	}

	require.NoError(t, commit(laneMinerTx(t, env, key, burner, 400_000, nil)))
	require.Equal(t, uint64(400_000), env.header.GasUsed)

	nonce, balance, pool := env.state.GetNonce(sender), env.state.GetBalance(sender).Clone(), env.gasPool.Gas()

	over := laneMinerTx(t, env, key, burner, 200_000, nil)
	require.ErrorIs(t, commit(over), core.ErrNonPaymentGasCap)
	require.Equal(t, uint64(400_000), env.header.GasUsed)
	require.Equal(t, nonce, env.state.GetNonce(sender))
	require.Equal(t, balance, env.state.GetBalance(sender))
	require.Equal(t, pool, env.gasPool.Gas())
	require.Len(t, env.txs, 1)

	// Payments still fit: their gas does not count against the cap.
	require.NoError(t, commit(laneMinerTx(t, env, key, token, 100_000, payment)))
	require.Len(t, env.txs, 2)

	// A non-payment tx that fits the remaining budget is still accepted.
	require.NoError(t, commit(laneMinerTx(t, env, key, burner, 90_000, nil)))
	require.Len(t, env.txs, 3)
	require.NoError(t, core.ValidatePaymentLaneCapacity(w.chainConfig, env.header, env.receipts))
}

func TestRecordPaymentLaneGas(t *testing.T) {
	t.Parallel()

	w, env, _, _, _ := laneMinerEnv(t, 50)
	w.recordPaymentLaneGas(env, &types.Receipt{Logs: []*types.Log{paymentLog(300), nil}})
	w.recordPaymentLaneGas(env, &types.Receipt{})
	require.Equal(t, uint64(300), env.paymentLaneGas)

	w, env, _, _, _ = laneMinerEnv(t, 0)
	w.recordPaymentLaneGas(env, &types.Receipt{Logs: []*types.Log{paymentLog(300)}})
	require.Zero(t, env.paymentLaneGas)
}

func TestPaymentLaneBuilderReceiptCapacityParity(t *testing.T) {
	t.Parallel()

	w, env, key, token, burner := laneMinerEnv(t, 90)
	failed := common.HexToAddress("0xfa11ed")
	env.state.SetCode(failed, []byte{byte(vm.INVALID)}, tracing.CodeChangeUnspecified)
	w.chainConfig.Bor.PaymentLaneAt(env.header.Number).Assets = append(
		w.chainConfig.Bor.PaymentLaneAt(env.header.Number).Assets, failed,
	)

	payment := []byte{0xa9, 0x05, 0x9c, 0xbb}
	payment = append(payment, common.LeftPadBytes(common.HexToAddress("0xbee").Bytes(), 32)...)
	payment = append(payment, common.LeftPadBytes([]byte{1}, 32)...)

	require.NoError(t, commitLane(w, env, laneMinerTx(t, env, key, token, 100_000, payment)))
	require.NoError(t, commitLane(w, env, laneMinerTx(t, env, key, failed, 80_000, payment)))
	require.NoError(t, commitLane(w, env, burnerTx(t, env, key, burner, 100_000)))
	require.Len(t, env.receipts, 3)
	require.Equal(t, types.ReceiptStatusSuccessful, env.receipts[0].Status)
	require.Equal(t, types.ReceiptStatusFailed, env.receipts[1].Status)
	require.Positive(t, core.PaymentLaneCapacityFromLogs(env.receipts[0].Logs))
	require.Positive(t, core.PaymentLaneCapacityFromLogs(env.receipts[1].Logs))
	require.Zero(t, core.PaymentLaneCapacityFromLogs(env.receipts[2].Logs))

	var receiptPayment uint64
	for _, receipt := range env.receipts {
		receiptPayment += core.PaymentLaneCapacityFromLogs(receipt.Logs)
	}
	require.Equal(t, receiptPayment, env.paymentLaneGas)
	require.Equal(t,
		core.NonPaymentGas(env.header.GasUsed, receiptPayment),
		core.NonPaymentGas(env.header.GasUsed, env.paymentLaneGas),
	)
	require.NoError(t, core.ValidatePaymentLaneCapacity(w.chainConfig, env.header, env.receipts))
}

// Once the non-payment budget is spent, only direct payment calls are
// admitted, and a payment-shaped call that is not a payment is refused.
func TestNonPaymentCapBudgetExhausted(t *testing.T) {
	t.Parallel()

	w, env, key, token, burner := laneMinerEnv(t, 50)

	transferTo := func(amount byte) []byte {
		out := []byte{0xa9, 0x05, 0x9c, 0xbb}
		out = append(out, common.LeftPadBytes(common.HexToAddress("0xbee").Bytes(), 32)...)

		return append(out, common.LeftPadBytes([]byte{amount}, 32)...)
	}

	commit := func(tx *types.Transaction) error {
		env.state.SetTxContext(tx.Hash(), env.tcount)
		_, err := w.commitTransaction(env, tx)

		return err
	}

	require.NoError(t, commit(laneMinerTx(t, env, key, burner, 500_000, nil)))
	require.ErrorIs(t, commit(laneMinerTx(t, env, key, burner, 21_000, nil)), core.ErrNonPaymentGasCap)
	require.ErrorIs(t, commit(laneMinerTx(t, env, key, token, 100_000, transferTo(0))), core.ErrNonPaymentGasCap, "zero amount is not a payment")
	require.NoError(t, commit(laneMinerTx(t, env, key, token, 100_000, transferTo(1))))
	require.Len(t, env.txs, 2)
	require.NoError(t, core.ValidatePaymentLaneCapacity(w.chainConfig, env.header, env.receipts))
}

// A payment with a gas limit above what it can count toward the payment
// share still runs once the budget is spent, and is included as long as it
// uses no more than a payment.
func TestNonPaymentCapRunsPaddedPayments(t *testing.T) {
	t.Parallel()

	w, env, key, token, burner := laneMinerEnv(t, 50)
	env.header.GasLimit, env.gasPool = 4_000_000, new(core.GasPool).AddGas(4_000_000)

	transfer := []byte{0xa9, 0x05, 0x9c, 0xbb}
	transfer = append(transfer, common.LeftPadBytes(common.HexToAddress("0xbee").Bytes(), 32)...)
	transfer = append(transfer, common.LeftPadBytes([]byte{1}, 32)...)

	require.NoError(t, commitLane(w, env, burnerTx(t, env, key, burner, 2_000_000)))
	require.ErrorIs(t, commitLane(w, env, burnerTx(t, env, key, burner, 21_000)), core.ErrNonPaymentGasCap)
	require.NoError(t, commitLane(w, env, laneMinerTx(t, env, key, token, 300_000, transfer)))
	require.Zero(t, env.paymentLaneRefusedGas)
	require.Len(t, env.txs, 2)
	require.NoError(t, core.ValidatePaymentLaneCapacity(w.chainConfig, env.header, env.receipts))
}

func burnerTx(t *testing.T, env *environment, key *ecdsa.PrivateKey, burner common.Address, gas uint64) *types.Transaction {
	t.Helper()
	return laneMinerTx(t, env, key, burner, gas, nil)
}

func commitLane(w *worker, env *environment, tx *types.Transaction) error {
	env.state.SetTxContext(tx.Hash(), env.tcount)
	_, err := w.commitTransaction(env, tx)

	return err
}

// A tx that might break the cap runs once, with the remaining budget
// enforced by its own execution; a refused one costs the build its gas, and
// once refusals have used the build's allowance, non-payment txs beyond the
// budget are refused without running. Not parallel: the counters are
// process-global.
func TestNonPaymentCapSingleExecution(t *testing.T) {
	if !metrics.Enabled() {
		metrics.Enable()
	}

	refusedGas := func() int64 { return paymentLaneRefusedGasCounter.Snapshot().Count() }

	w, env, key, token, burner := laneMinerEnv(t, 50)
	require.NoError(t, commitLane(w, env, burnerTx(t, env, key, burner, 300_000)))

	// Fits at its gas limit: no budget is set, nothing is refused.
	before := refusedGas()
	require.NoError(t, commitLane(w, env, burnerTx(t, env, key, burner, 150_000)))
	require.Equal(t, before, refusedGas())

	// 50k left: a 60k burner runs once and is refused with its gas counted.
	require.EqualError(t, commitLane(w, env, burnerTx(t, env, key, burner, 60_000)), "non-payment gas above payment lane cap: 60000 > 50000")
	require.Equal(t, before+60_000, refusedGas())
	require.Equal(t, uint64(60_000), env.paymentLaneRefusedGas)

	// A tx whose gas limit exceeds the budget but whose usage fits is kept.
	transfer := []byte{0xa9, 0x05, 0x9c, 0xbb}
	transfer = append(transfer, common.LeftPadBytes(common.HexToAddress("0xbee").Bytes(), 32)...)
	transfer = append(transfer, common.LeftPadBytes([]byte{1}, 32)...)
	require.NoError(t, commitLane(w, env, laneMinerTx(t, env, key, token, 100_000, transfer)))

	// Past the refusal allowance (10% of 1M), non-payment txs beyond the
	// budget are refused without running.
	env.paymentLaneRefusedGas = 100_000
	err := commitLane(w, env, burnerTx(t, env, key, burner, 60_000))
	require.EqualError(t, err, "non-payment gas above payment lane cap: budget exhausted")
	require.Equal(t, before+60_000, refusedGas(), "refused without running")

	// Direct payments are still tried.
	require.NoError(t, commitLane(w, env, laneMinerTx(t, env, key, token, 100_000, transfer)))
	require.NoError(t, core.ValidatePaymentLaneCapacity(w.chainConfig, env.header, env.receipts))
}

// The budgeted execution is the block's real one, so the build timeout
// interrupts it like any other tx.
func TestNonPaymentCapHonoursInterrupt(t *testing.T) {
	t.Parallel()

	w, env, key, _, burner := laneMinerEnv(t, 50)
	require.NoError(t, commitLane(w, env, burnerTx(t, env, key, burner, 450_000)))

	loop := common.HexToAddress("0x1009")
	env.state.SetCode(loop, []byte{byte(vm.JUMPDEST), byte(vm.PUSH1), 0, byte(vm.JUMP)}, tracing.CodeChangeUnspecified)

	var interrupt atomic.Bool
	interrupt.Store(true)
	env.evm.SetInterrupt(&interrupt)

	nonce, pool := env.state.GetNonce(crypto.PubkeyToAddress(key.PublicKey)), env.gasPool.Gas()
	// Above the 50k left, within the 100k refusal allowance: it runs.
	err := commitLane(w, env, laneMinerTx(t, env, key, loop, 90_000, nil))
	require.ErrorIs(t, err, vm.ErrInterrupt)
	require.Equal(t, nonce, env.state.GetNonce(crypto.PubkeyToAddress(key.PublicKey)))
	require.Equal(t, pool, env.gasPool.Gas())
	require.Nil(t, env.evm.Config.NonPaymentGasBudget, "the budget does not outlive the tx")
}

func TestNonPaymentCapRefusesInvalidTxsWithTheirOwnError(t *testing.T) {
	t.Parallel()

	w, env, key, token, burner := laneMinerEnv(t, 50)
	require.NoError(t, commitLane(w, env, burnerTx(t, env, key, burner, 450_000)))

	sender := crypto.PubkeyToAddress(key.PublicKey)
	bad, err := types.SignNewTx(key, env.signer, &types.DynamicFeeTx{
		ChainID: env.signer.ChainID(), Nonce: env.state.GetNonce(sender) + 5, GasTipCap: common.Big1,
		GasFeeCap: new(big.Int).Mul(env.header.BaseFee, big.NewInt(2)), Gas: 100_000, To: &token,
	})
	require.NoError(t, err)
	require.ErrorIs(t, commitLane(w, env, bad), core.ErrNonceTooHigh)
	require.Zero(t, env.paymentLaneRefusedGas)
}

// Without a cap configured, non-payment txs are committed as usual.
func TestCommitTransactionWithoutCap(t *testing.T) {
	t.Parallel()

	w, env, key, _, burner := laneMinerEnv(t, 0)
	require.NoError(t, commitLane(w, env, burnerTx(t, env, key, burner, 900_000)))
	require.NoError(t, commitLane(w, env, burnerTx(t, env, key, burner, 100_000)))
	require.Len(t, env.txs, 2)
}

// A direct payment call counts toward the payment share up to the gas a
// payment can use, so within that it is never refused by the cap: one that
// burns gas in a callback is included, pays full price for what is not
// credited, and leaves the refusal allowance untouched. One that may burn
// more goes through the budget like any other tx. Not parallel: the
// counters are process-global.
func TestNonPaymentCapNeverRefusesDirectPayments(t *testing.T) {
	if !metrics.Enabled() {
		metrics.Enable()
	}

	w, env, key, token, burner := laneMinerEnv(t, 70)
	sig := common.FromHex("ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

	// The token logs the transfer, then lets a hook burn the rest of the gas.
	hook := common.HexToAddress("0x400c")
	env.state.SetCode(hook, []byte{byte(vm.JUMPDEST), byte(vm.PUSH1), 0, byte(vm.JUMP)}, tracing.CodeChangeUnspecified)
	code := []byte{byte(vm.PUSH1), 0x24, byte(vm.CALLDATALOAD), byte(vm.PUSH1), 0, byte(vm.MSTORE), byte(vm.PUSH1), 4, byte(vm.CALLDATALOAD), byte(vm.CALLER), byte(vm.PUSH32)}
	code = append(code, sig...)
	code = append(code, byte(vm.PUSH1), 0x20, byte(vm.PUSH1), 0, byte(vm.LOG3),
		byte(vm.PUSH1), 0, byte(vm.PUSH1), 0, byte(vm.PUSH1), 0, byte(vm.PUSH1), 0, byte(vm.PUSH20))
	code = append(code, hook[:]...)
	code = append(code, byte(vm.GAS), byte(vm.STATICCALL), byte(vm.POP), byte(vm.STOP))
	env.state.SetCode(token, code, tracing.CodeChangeUnspecified)

	require.NoError(t, commitLane(w, env, burnerTx(t, env, key, burner, 700_000)), "fill the non-payment budget")

	transfer := []byte{0xa9, 0x05, 0x9c, 0xbb}
	transfer = append(transfer, common.LeftPadBytes(common.HexToAddress("0xbee").Bytes(), 32)...)
	transfer = append(transfer, common.LeftPadBytes([]byte{1}, 32)...)

	// A direct payment call whose gas limit leaves room for more than a
	// payment goes through the budget and the refusal allowance like any
	// other tx (here a refusal would not fit the allowance).
	require.ErrorIs(t, commitLane(w, env, laneMinerTx(t, env, key, token, 200_000, transfer)), core.ErrNonPaymentGasCap)
	refusedGas := env.paymentLaneRefusedGas

	refused := paymentLaneRefusedCounter.Snapshot().Count()
	for range 2 {
		require.NoError(t, commitLane(w, env, laneMinerTx(t, env, key, token, 120_000, transfer)))
	}

	require.Equal(t, refused, paymentLaneRefusedCounter.Snapshot().Count())
	require.Equal(t, refusedGas, env.paymentLaneRefusedGas)
	require.Len(t, env.txs, 3)
	require.NoError(t, core.ValidatePaymentLaneCapacity(w.chainConfig, env.header, env.receipts))

	// Each was credited only its own transfer work; the rest paid in full.
	for _, r := range env.receipts[1:] {
		lane, ok := core.PaymentLaneFromLogs(r.Logs)
		require.True(t, ok)
		require.LessOrEqual(t, lane.Gas, uint64(params.PaymentLaneMaxTransferFrameGas))
		require.Greater(t, r.GasUsed, lane.Gas+50_000, "the hook's gas is not credited")
	}

	// A non-payment tx past the budget is still refused.
	require.ErrorContains(t, commitLane(w, env, burnerTx(t, env, key, burner, 60_000)), "budget exhausted")
}
