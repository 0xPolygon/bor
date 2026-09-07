//go:build devnet_repro

package miner

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

// TestCommitFakeTransactionUsesSpoofedSender verifies that commitFakeTransaction
// attributes the state change to FakeTxSpec.From, not to whatever address would
// be recovered from the wrapping transaction's (irrelevant) signature.
func TestCommitFakeTransactionUsesSpoofedSender(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)
	env := newSizeTestEnv(t, w)
	defer env.discard()

	spoofed := common.HexToAddress("0x0000000000000000000000000000000000009999")
	// Fund the spoofed account directly in the test env's state — on the real
	// clone this balance already exists on-chain; here we seed it explicitly.
	env.state.AddBalance(spoofed, uint256.MustFromBig(big.NewInt(1_000_000_000_000_000_000)), 0)

	value := hexutil.Big(*big.NewInt(0))
	spec := FakeTxSpec{
		From:     spoofed,
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		Data:     nil,
		Value:    &value,
		GasLimit: hexutil.Uint64(21000),
	}

	nonceBefore := env.state.GetNonce(spoofed)
	tcountBefore := env.tcount

	err := commitFakeTransaction(env, spec)
	require.NoError(t, err)

	require.Equal(t, tcountBefore+1, env.tcount, "env.tcount must grow by exactly 1")
	require.Equal(t, nonceBefore+1, env.state.GetNonce(spoofed), "spoofed sender's nonce must be incremented, proving it was used as msg.From")
	require.Len(t, env.receipts, 1)
	require.Equal(t, types.ReceiptStatusSuccessful, env.receipts[0].Status)
}

// TestCommitFakeTransactionInitializesNilGasPool reproduces the real
// buildAndCommitBlock call path: devnetInjectFakeTx runs BEFORE
// commitTransactions, so env.gasPool is still nil at that point (it is
// otherwise lazily created inside commitTransactions). Unlike
// newSizeTestEnv, which pre-populates env.gasPool for convenience and so
// masks this exact gap, this test builds env via prepareWork alone and
// leaves gasPool nil, matching production. Regression test for a nil-pointer
// panic in commitFakeTransaction's env.gasPool.Gas() call.
func TestCommitFakeTransactionInitializesNilGasPool(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)

	env, err := w.prepareWork(&generateParams{
		timestamp: uint64(time.Now().Unix()),
		coinbase:  testBankAddress,
	}, false)
	require.NoError(t, err)
	defer env.discard()

	require.Nil(t, env.gasPool, "precondition: env.gasPool must be nil, matching the real pre-fillTransactions call site")

	spoofed := common.HexToAddress("0x0000000000000000000000000000000000009999")
	env.state.AddBalance(spoofed, uint256.MustFromBig(big.NewInt(1_000_000_000_000_000_000)), 0)

	value := hexutil.Big(*big.NewInt(0))
	spec := FakeTxSpec{
		From:     spoofed,
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		Value:    &value,
		GasLimit: hexutil.Uint64(21000),
	}

	require.NotPanics(t, func() {
		err = commitFakeTransaction(env, spec)
	})
	require.NoError(t, err)
	require.NotNil(t, env.gasPool, "commitFakeTransaction must lazily initialize env.gasPool")
	require.Len(t, env.receipts, 1)
}

// TestCommitFakeTransactionDefaultsNilValue is a regression test for final
// review finding I1: prior to this fix, a nil spec.Value (the state that
// results from omitting "value" from the RPC call — which, per finding C1,
// used to be the ONLY way to get the call to parse at all) flowed through as
// a nil *big.Int into core.Message.Value, and the state transition's buyGas
// does balanceCheck.Add(balanceCheck, st.msg.Value) — big.Int.Add with a nil
// operand panics. commitFakeTransaction must default a nil spec.Value to
// zero instead of ever handing a nil *big.Int downstream.
func TestCommitFakeTransactionDefaultsNilValue(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)
	env := newSizeTestEnv(t, w)
	defer env.discard()

	spoofed := common.HexToAddress("0x0000000000000000000000000000000000009999")
	env.state.AddBalance(spoofed, uint256.MustFromBig(big.NewInt(1_000_000_000_000_000_000)), 0)

	spec := FakeTxSpec{
		From:     spoofed,
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		Value:    nil, // deliberately omitted, matching the pre-fix crash trigger
		GasLimit: hexutil.Uint64(21000),
	}

	var err error
	require.NotPanics(t, func() {
		err = commitFakeTransaction(env, spec)
	})
	require.NoError(t, err)
	require.Len(t, env.receipts, 1)
	require.Equal(t, types.ReceiptStatusSuccessful, env.receipts[0].Status)
}

// TestCommitFakeTransactionRejectsZeroGasLimit verifies commitFakeTransaction
// refuses (returns an error, does not proceed) when spec.GasLimit is zero,
// per the final review's I1 ruling.
func TestCommitFakeTransactionRejectsZeroGasLimit(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)
	env := newSizeTestEnv(t, w)
	defer env.discard()

	spec := FakeTxSpec{
		From:     common.HexToAddress("0x0000000000000000000000000000000000009999"),
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		GasLimit: 0,
	}

	err := commitFakeTransaction(env, spec)
	require.Error(t, err)
	require.Len(t, env.receipts, 0, "no receipt should be created when the spec is rejected")
}

// TestDevnetInjectFakeTxNoopWhenNothingStaged verifies the hook does nothing
// when no fake tx has been staged.
func TestDevnetInjectFakeTxNoopWhenNothingStaged(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)
	env := newSizeTestEnv(t, w)
	defer env.discard()

	tcountBefore := env.tcount
	devnetInjectFakeTx(w, env)
	require.Equal(t, tcountBefore, env.tcount, "no-op when nothing staged")
}

// TestStageFakeTxConsumedOnceByInject is the M1 regression test (final
// review): stages a spec via the public StageFakeTx entry point, confirms
// devnetInjectFakeTx applies it exactly once, and confirms a second call
// (nothing left staged) is a no-op.
func TestStageFakeTxConsumedOnceByInject(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)
	env := newSizeTestEnv(t, w)
	defer env.discard()

	spoofed := common.HexToAddress("0x0000000000000000000000000000000000009999")
	env.state.AddBalance(spoofed, uint256.MustFromBig(big.NewInt(1_000_000_000_000_000_000)), 0)

	value := hexutil.Big(*big.NewInt(0))
	spec := FakeTxSpec{
		From:     spoofed,
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		Value:    &value,
		GasLimit: hexutil.Uint64(21000),
	}
	(&Miner{}).StageFakeTx(spec)

	tcountBefore := env.tcount
	devnetInjectFakeTx(w, env)
	require.Equal(t, tcountBefore+1, env.tcount, "staged spec must be applied on the first inject")

	devnetInjectFakeTx(w, env)
	require.Equal(t, tcountBefore+1, env.tcount, "staged spec must be consumed exactly once — the second inject must be a no-op")
}

// TestDevnetInjectFakeTxRecoversFromPanic verifies the recover() safety net
// added to devnetInjectFakeTx for final review finding I1: a failed or
// malformed fake-tx injection must never take down the real block build.
// devnetCommitFakeTransaction is swapped for a deliberately panicking stub
// for the duration of this test — the nil-Value fix above already closes
// off the one known real panic trigger, so this exercises the backstop
// against ANY future bug of this shape, not a specific reconstructed one.
func TestDevnetInjectFakeTxRecoversFromPanic(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)
	env := newSizeTestEnv(t, w)
	defer env.discard()

	original := devnetCommitFakeTransaction
	devnetCommitFakeTransaction = func(env *environment, spec FakeTxSpec) error {
		panic("simulated commitFakeTransaction panic")
	}
	t.Cleanup(func() { devnetCommitFakeTransaction = original })

	spec := FakeTxSpec{
		From:     common.HexToAddress("0x0000000000000000000000000000000000009999"),
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		GasLimit: hexutil.Uint64(21000),
	}
	(&Miner{}).StageFakeTx(spec)

	require.NotPanics(t, func() {
		devnetInjectFakeTx(w, env)
	}, "a panic inside commitFakeTransaction must never propagate out of devnetInjectFakeTx")
}

// TestCommitFakeTransactionRecordsLogsUnderCorrectTxHash is a regression test
// for final review finding M4: commitFakeTransaction must call
// env.state.SetTxContext(tx.Hash(), env.tcount) immediately before applying
// the message, exactly like the real commitTransactions does — without it,
// any logs emitted by the called contract get filed under a stale/zero tx
// hash and silently vanish from the fake tx's own receipt, which would
// defeat reading back XEN's emitted events (the actual point of the
// experiment). The called "contract" here is a minimal installed bytecode
// that emits one LOG0, not a real XEN deployment.
func TestCommitFakeTransactionRecordsLogsUnderCorrectTxHash(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)
	env := newSizeTestEnv(t, w)
	defer env.discard()

	spoofed := common.HexToAddress("0x0000000000000000000000000000000000009999")
	env.state.AddBalance(spoofed, uint256.MustFromBig(big.NewInt(1_000_000_000_000_000_000)), 0)

	// PUSH1 42, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, LOG0, STOP — writes 42 to
	// memory offset 0, then emits a topicless LOG0 over that 32-byte range.
	// Same instruction sequence exercised in core/vm/dispatch_test.go's
	// "default_path/LOG0" case.
	logEmitter := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	logEmitterCode := common.FromHex("0x602a60005260206000a000")
	env.state.SetCode(logEmitter, logEmitterCode, tracing.CodeChangeGenesis)

	value := hexutil.Big(*big.NewInt(0))
	spec := FakeTxSpec{
		From:     spoofed,
		To:       logEmitter,
		Value:    &value,
		GasLimit: hexutil.Uint64(100000),
	}

	err := commitFakeTransaction(env, spec)
	require.NoError(t, err)
	require.Len(t, env.receipts, 1)
	require.Equal(t, types.ReceiptStatusSuccessful, env.receipts[0].Status)
	require.Len(t, env.receipts[0].Logs, 1, "the fake tx's receipt must contain the log its call emitted")
	require.Equal(t, env.txs[0].Hash(), env.receipts[0].Logs[0].TxHash, "the log must be filed under the fake tx's own hash, not a stale/zero one")
}
