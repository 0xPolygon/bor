package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/stretchr/testify/require"
)

func laneLog(gas, base, tip int64) *types.Log {
	data := common.LeftPadBytes(big.NewInt(gas).Bytes(), 32)
	data = append(data, common.LeftPadBytes(big.NewInt(base).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(big.NewInt(tip).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(big.NewInt(gas).Bytes(), 32)...)

	return &types.Log{Address: feeAddress, Topics: []common.Hash{paymentLaneLogSig, laneSender.Hash()}, Data: data}
}

func TestCollectPaymentLaneStats(t *testing.T) {
	t.Parallel()

	other := laneLog(1, 1, 1)
	other.Topics[0] = transferFeeLogSig
	elsewhere := laneLog(1, 1, 1)
	elsewhere.Address = laneToken
	short := laneLog(1, 1, 1)
	short.Data = short.Data[:64]
	noTopics := laneLog(1, 1, 1)
	noTopics.Topics = nil

	receipts := []*types.Receipt{
		{Logs: []*types.Log{laneLog(50_000, 7, 3), other}},
		{Logs: []*types.Log{elsewhere, short, noTopics, nil}},
		{Logs: []*types.Log{laneLog(30_000, 11, 0)}},
	}

	stats := collectPaymentLaneStats(receipts)
	require.Equal(t, int64(2), stats.txs)
	require.Equal(t, uint64(80_000), stats.gas)
	require.Equal(t, int64(18), stats.baseCut.Int64())
	require.Equal(t, int64(3), stats.tipCut.Int64())
}

func TestRecordPaymentLaneMetrics(t *testing.T) {
	if !metrics.Enabled() {
		metrics.Enable()
	}

	resetPaymentLaneCutRemainder(t)

	cfg := laneChainConfig(t)
	parent := &types.Header{Number: big.NewInt(9), GasLimit: 100, GasUsed: 10}
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(10), GasUsed: 100_000})
	receipts := []*types.Receipt{{Logs: []*types.Log{laneLog(40_000, 3_000_000_000, 2_000_000_000)}}}

	blocks, txs, gas := paymentLaneBlocksCounter.Snapshot().Count(), paymentLaneTxsCounter.Snapshot().Count(), paymentLaneGasCounter.Snapshot().Count()
	base, tip, off := paymentLaneBaseCutCounter.Snapshot().Count(), paymentLaneTipCutCounter.Snapshot().Count(), paymentLaneOffCounter.Snapshot().Count()

	recordPaymentLaneMetrics(cfg, block, fixedParent{parent}, receipts)

	require.Equal(t, blocks+1, paymentLaneBlocksCounter.Snapshot().Count())
	require.Equal(t, txs+1, paymentLaneTxsCounter.Snapshot().Count())
	require.Equal(t, gas+40_000, paymentLaneGasCounter.Snapshot().Count())
	require.Equal(t, base+3, paymentLaneBaseCutCounter.Snapshot().Count())
	require.Equal(t, tip+2, paymentLaneTipCutCounter.Snapshot().Count())
	require.Equal(t, off, paymentLaneOffCounter.Snapshot().Count())
	require.Equal(t, int64(params.PaymentLaneMaxBps), paymentLaneScaleGauge.Snapshot().Value())
	require.Equal(t, int64(4_000), paymentLaneBlockShareGauge.Snapshot().Value())

	busy := &types.Header{Number: big.NewInt(9), GasLimit: 100, GasUsed: 90}
	recordPaymentLaneMetrics(cfg, block, fixedParent{busy}, nil)
	require.Equal(t, off+1, paymentLaneOffCounter.Snapshot().Count())
	require.Zero(t, paymentLaneScaleGauge.Snapshot().Value())

	// Sub-gwei cuts add up across blocks instead of being dropped.
	base = paymentLaneBaseCutCounter.Snapshot().Count()
	half := []*types.Receipt{{Logs: []*types.Log{laneLog(1, 600_000_000, 0)}}}
	recordPaymentLaneMetrics(cfg, block, fixedParent{parent}, half)
	require.Equal(t, base, paymentLaneBaseCutCounter.Snapshot().Count())
	recordPaymentLaneMetrics(cfg, block, fixedParent{parent}, half)
	require.Equal(t, base+1, paymentLaneBaseCutCounter.Snapshot().Count())

	// An empty block resets the share instead of keeping the last value.
	recordPaymentLaneMetrics(cfg, types.NewBlockWithHeader(&types.Header{Number: big.NewInt(10)}), fixedParent{parent}, nil)
	require.Zero(t, paymentLaneBlockShareGauge.Snapshot().Value())

	// Before the fork, without a parent or without Bor nothing is recorded.
	recorded := paymentLaneBlocksCounter.Snapshot().Count()
	cfg.Bor.PaymentLaneBlock = big.NewInt(11)
	recordPaymentLaneMetrics(cfg, block, fixedParent{parent}, receipts)
	recordPaymentLaneMetrics(laneChainConfig(t), block, fixedParent{nil}, receipts)
	recordPaymentLaneMetrics(params.TestChainConfig, block, fixedParent{parent}, receipts)
	require.Equal(t, recorded, paymentLaneBlocksCounter.Snapshot().Count())
}

// resetPaymentLaneCutRemainder clears the sub-gwei carry, which outlives a
// test run (e.g. with -count) and blocks imported by other tests.
func resetPaymentLaneCutRemainder(t *testing.T) {
	t.Helper()

	paymentLaneCutRemainder.Lock()
	defer paymentLaneCutRemainder.Unlock()

	paymentLaneCutRemainder.base.SetUint64(0)
	paymentLaneCutRemainder.tip.SetUint64(0)
}

func TestPaymentLaneFromLogs(t *testing.T) {
	t.Parallel()

	_, ok := PaymentLaneFromLogs(nil)
	require.False(t, ok)

	other := laneLog(1, 1, 1)
	other.Topics[0] = transferFeeLogSig

	rec, ok := PaymentLaneFromLogs([]*types.Log{nil, other, laneLog(40_000, 7, 3), laneLog(1, 1, 1)})
	require.True(t, ok)
	require.Equal(t, uint64(40_000), rec.Gas)
	require.Equal(t, int64(7), rec.BaseCut.Int64())
	require.Equal(t, int64(3), rec.TipCut.Int64())
}

func TestPaymentLaneDiscountForGuards(t *testing.T) {
	t.Parallel()

	lane := params.DefaultPaymentLaneConfig([]common.Address{laneToken})
	base, tip := big.NewInt(1_000), big.NewInt(100)

	require.Zero(t, PaymentLaneDiscountFor(nil, base, tip, 10_000, 10).Sign(), "no lane")
	require.Zero(t, PaymentLaneDiscountFor(lane, nil, tip, 10_000, 10).Sign(), "no base fee")
	require.Zero(t, PaymentLaneDiscountFor(lane, base, big.NewInt(-1), 10_000, 10).Sign(), "negative tip")
	require.Zero(t, PaymentLaneDiscountFor(lane, base, tip, 0, 10).Sign(), "zero scale")
	require.Zero(t, PaymentLaneDiscountFor(lane, base, tip, 10_000, 0).Sign(), "no payment gas")

	// 80% of 1000 = 800 per gas; the tip is charged in full.
	require.Equal(t, int64(8_000), PaymentLaneDiscountFor(lane, base, tip, 10_000, 10).Int64())
}

// The chain records lane metrics for canonical blocks it writes.
func TestPaymentLaneMetricsOnImport(t *testing.T) {
	if !metrics.Enabled() {
		metrics.Enable()
	}

	gspec, blocks, _ := laneCapChain(t, 2, 900_000)

	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
	require.NoError(t, err)
	defer chain.Stop()

	before, txs := paymentLaneBlocksCounter.Snapshot().Count(), paymentLaneTxsCounter.Snapshot().Count()

	_, err = chain.InsertChain(blocks, false)
	require.NoError(t, err)

	// Blocks 2 and 3 are past the fork; each carries one logged payment.
	require.GreaterOrEqual(t, paymentLaneBlocksCounter.Snapshot().Count()-before, int64(2))
	require.GreaterOrEqual(t, paymentLaneTxsCounter.Snapshot().Count()-txs, int64(2))
	require.Equal(t, int64(1), paymentLaneBlockTxsGauge.Snapshot().Value())
}

func TestBlockValidatorPaymentLaneCapacity(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).NonPaymentGasCapPercentage = 50
	v := NewBlockValidator(cfg, nil)

	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(10), GasLimit: 1_000, GasUsed: 900})
	over := &ProcessResult{GasUsed: 900, Receipts: types.Receipts{{Logs: []*types.Log{laneLog(300, 0, 0)}}}}

	require.ErrorIs(t, v.ValidateStateCheap(block, nil, over), ErrNonPaymentGasCap)
	require.ErrorIs(t, v.ValidateState(block, nil, over, true), ErrNonPaymentGasCap)

	// The remaining checks need a header that commits to the receipts.
	within := &ProcessResult{GasUsed: 900, Receipts: types.Receipts{{Logs: []*types.Log{laneLog(400, 0, 0)}}}}
	within.Receipts[0].Bloom = types.CreateBloom(within.Receipts[0])
	block = types.NewBlockWithHeader(&types.Header{
		Number: big.NewInt(10), GasLimit: 1_000, GasUsed: 900,
		Bloom:       types.MergeBloom(within.Receipts),
		ReceiptHash: types.DeriveSha(within.Receipts, trie.NewStackTrie(nil)),
	})
	require.NoError(t, v.ValidateStateCheap(block, nil, within))
	require.NoError(t, v.ValidateState(block, nil, within, true))
}

// fixedParent serves one parent header whatever is asked.
type fixedParent struct{ h *types.Header }

func (p fixedParent) GetHeader(common.Hash, uint64) *types.Header { return p.h }
