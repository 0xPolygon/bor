package core

import (
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

var (
	paymentLaneBlocksCounter   = metrics.NewRegisteredCounter("chain/paymentlane/blocks", nil)
	paymentLaneOffCounter      = metrics.NewRegisteredCounter("chain/paymentlane/blocks/off", nil)
	paymentLaneScaleGauge      = metrics.NewRegisteredGauge("chain/paymentlane/scale", nil)
	paymentLaneTxsCounter      = metrics.NewRegisteredCounter("chain/paymentlane/txs", nil)
	paymentLaneGasCounter      = metrics.NewRegisteredCounter("chain/paymentlane/gas", nil)
	paymentLaneBaseCutCounter  = metrics.NewRegisteredCounter("chain/paymentlane/basecut/gwei", nil)
	paymentLaneTipCutCounter   = metrics.NewRegisteredCounter("chain/paymentlane/tipcut/gwei", nil)
	paymentLaneBlockTxsGauge   = metrics.NewRegisteredGauge("chain/paymentlane/block/txs", nil)
	paymentLaneBlockShareGauge = metrics.NewRegisteredGauge("chain/paymentlane/block/gasshare", nil) // payment gas per 10k of block gas
)

var gweiDivisor = big.NewInt(1_000_000_000)

// paymentLaneCutRemainder carries the sub-gwei part of the cut counters from
// block to block, so the gwei counters do not drift down over time.
var paymentLaneCutRemainder = struct {
	sync.Mutex
	base, tip *big.Int
}{base: new(big.Int), tip: new(big.Int)}

func addCutGwei(counter *metrics.Counter, remainder, wei *big.Int) {
	remainder.Add(remainder, wei)
	gwei, rest := new(big.Int).QuoRem(remainder, gweiDivisor, new(big.Int))
	remainder.Set(rest)
	counter.Inc(gwei.Int64())
}

// paymentLaneBlockStats sums the PaymentLaneDiscount logs of a block. It reads
// receipts only, so it is the same on every node and for every executor.
type paymentLaneBlockStats struct {
	txs     int64
	gas     uint64
	baseCut *big.Int
	tipCut  *big.Int
}

func collectPaymentLaneStats(receipts []*types.Receipt) paymentLaneBlockStats {
	stats := paymentLaneBlockStats{baseCut: new(big.Int), tipCut: new(big.Int)}

	for _, r := range receipts {
		for _, l := range r.Logs {
			// With a cap, failed payment attempts are logged too, with
			// no payment gas: they are not payments.
			if !isPaymentLaneLog(l) || laneLogWord(l.Data, 0) == 0 {
				continue
			}

			stats.txs++
			stats.gas += laneLogWord(l.Data, 0)
			stats.baseCut.Add(stats.baseCut, new(big.Int).SetBytes(l.Data[32:64]))
			stats.tipCut.Add(stats.tipCut, new(big.Int).SetBytes(l.Data[64:96]))
		}
	}

	return stats
}

// parentReader resolves the parent header that gates the lane.
type parentReader interface {
	GetHeader(hash common.Hash, number uint64) *types.Header
}

// recordPaymentLaneMetrics is called once per canonical block written. The
// parent is only looked up once the fork and metrics are known to apply.
func recordPaymentLaneMetrics(config *params.ChainConfig, block *types.Block, chain parentReader, receipts []*types.Receipt) {
	if !metrics.Enabled() || config == nil || config.Bor == nil || block.NumberU64() == 0 {
		return
	}

	lane := config.Bor.PaymentLaneAt(block.Number())
	if lane == nil {
		return
	}

	parent := chain.GetHeader(block.ParentHash(), block.NumberU64()-1)
	if parent == nil {
		return
	}

	scale := lane.ScaleForParent(parent.GasUsed, parent.GasLimit)
	paymentLaneBlocksCounter.Inc(1)
	paymentLaneScaleGauge.Update(int64(scale))

	if scale == 0 {
		paymentLaneOffCounter.Inc(1)
	}

	stats := collectPaymentLaneStats(receipts)
	paymentLaneTxsCounter.Inc(stats.txs)
	paymentLaneGasCounter.Inc(int64(stats.gas))
	paymentLaneCutRemainder.Lock()
	addCutGwei(paymentLaneBaseCutCounter, paymentLaneCutRemainder.base, stats.baseCut)
	addCutGwei(paymentLaneTipCutCounter, paymentLaneCutRemainder.tip, stats.tipCut)
	paymentLaneCutRemainder.Unlock()

	paymentLaneBlockTxsGauge.Update(stats.txs)

	var share int64
	if block.GasUsed() > 0 {
		share = int64(stats.gas * params.PaymentLaneMaxBps / block.GasUsed())
	}

	paymentLaneBlockShareGauge.Update(share)
}
