package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

var laneFuzzTargets = []common.Address{laneToken, laneOther, laneRevToken, laneBurner, laneWallet, laneRevWrap, laneSwallow, laneDelegate}

type laneFuzzRun struct {
	paid, burnt, tipped *big.Int
	used                uint64
	failed              bool
	laneGas             uint64
	refundLogged        *big.Int
}

func runLaneFuzzTx(t *testing.T, lane *params.PaymentLaneConfig, scale uint64, baseFee, feeCap, tip *big.Int, to common.Address, input []byte) (laneFuzzRun, bool) {
	t.Helper()

	cfg := laneChainConfig(t)
	cfg.Bor.PaymentLane = map[string]*params.PaymentLaneConfig{"0": lane}

	db := laneState(t)
	before := db.GetBalance(laneSender).ToBig()
	blockCtx := vm.BlockContext{
		CanTransfer: CanTransfer, Transfer: Transfer,
		GetHash:  func(uint64) common.Hash { return common.Hash{} },
		Coinbase: laneCoinbase, BlockNumber: big.NewInt(10), Time: 10, Difficulty: big.NewInt(1),
		BaseFee: baseFee, GasLimit: 30_000_000, PaymentLaneScaleBps: scale,
	}
	// As TransactionToMessage prices a dynamic-fee tx: min(feeCap, baseFee + tip).
	price := new(big.Int).Add(baseFee, tip)
	if price.Cmp(feeCap) > 0 {
		price.Set(feeCap)
	}

	msg := &Message{
		To: &to, From: laneSender, Value: new(big.Int), GasLimit: 200_000,
		GasPrice: price, GasFeeCap: feeCap, GasTipCap: tip,
		Data: input, SkipNonceChecks: true,
	}

	res, err := ApplyMessage(vm.NewEVM(blockCtx, db, cfg, vm.Config{}), msg, new(GasPool).AddGas(30_000_000))
	if err != nil {
		return laneFuzzRun{}, false
	}

	out := laneFuzzRun{
		paid:         new(big.Int).Sub(before, db.GetBalance(laneSender).ToBig()),
		burnt:        db.GetBalance(res.BurntContractAddress).ToBig(),
		tipped:       db.GetBalance(laneCoinbase).ToBig(),
		used:         res.UsedGas,
		failed:       res.Failed(),
		refundLogged: new(big.Int),
	}

	for _, l := range laneLogsOf(db.Logs()) {
		out.laneGas = new(big.Int).SetBytes(l.Data[:32]).Uint64()
		out.refundLogged.Add(new(big.Int).SetBytes(l.Data[32:64]), new(big.Int).SetBytes(l.Data[64:96]))
	}

	return out, true
}

// FuzzPaymentLaneStateTransition runs real state transitions over random lane
// parameters, fees, targets and calldata. Whatever the input: value is
// conserved, the sender is refunded exactly what the log and the quote
// arithmetic say, the refund never exceeds the fee, gas accounting does not
// depend on the discount, and only listed assets are ever discounted.
func FuzzPaymentLaneStateTransition(f *testing.F) {
	f.Add(uint16(8000), uint16(0), false, uint64(0), uint64(250), uint64(100), uint64(50), uint16(10_000), uint8(0), true, false, transferInput(lanePayee, 1))
	f.Add(uint16(10_000), uint16(10_000), false, uint64(0), uint64(1), uint64(1), uint64(0), uint16(10_000), uint8(4), true, true, transferInput(lanePayee, 1))
	f.Add(uint16(5000), uint16(5000), true, uint64(2), uint64(30), uint64(300), uint64(0), uint16(4321), uint8(0), false, false, transferInput(lanePayee, 7))
	f.Add(uint16(5000), uint16(0), false, uint64(0), uint64(30), uint64(1), uint64(9), uint16(10_000), uint8(1), true, false, transferInput(lanePayee, 1))
	f.Add(uint16(9999), uint16(1), true, uint64(0), uint64(1000), uint64(0), uint64(0), uint16(1), uint8(5), true, false, transferInput(lanePayee, 3))
	f.Add(uint16(8000), uint16(0), false, uint64(0), uint64(250), uint64(100), uint64(50), uint16(10_000), uint8(0), true, false, []byte{0xa9, 0x05, 0x9c, 0xbb})

	f.Fuzz(func(t *testing.T, baseBps, tipBps uint16, capped bool, tipCapGwei, baseGwei, tipGwei, headroomGwei uint64, scale uint16, target uint8, pulls, allAssets bool, input []byte) {
		if baseBps > params.PaymentLaneMaxBps || tipBps > params.PaymentLaneMaxBps || scale > params.PaymentLaneMaxBps || len(input) > 1024 {
			return
		}

		g := func(v uint64) *big.Int { return new(big.Int).Mul(new(big.Int).SetUint64(v%2000), big.NewInt(1e9)) }
		baseFee := new(big.Int).Add(g(baseGwei), big.NewInt(1))
		tip := g(tipGwei)
		feeCap := new(big.Int).Add(baseFee, g(headroomGwei))

		lane := &params.PaymentLaneConfig{
			Assets: []common.Address{laneToken, laneRevToken, laneBurner}, BaseFeeDiscountBps: uint64(baseBps),
			TipDiscountBps: uint64(tipBps), ActiveGasPercentage: 50, CountAllowancePulls: pulls,
		}
		if allAssets {
			lane.Assets = append(lane.Assets, laneWallet, laneRevWrap)
		}

		if capped {
			lane.TipCap = g(tipCapGwei)
		}

		to := laneFuzzTargets[int(target)%len(laneFuzzTargets)]

		run, ok := runLaneFuzzTx(t, lane, uint64(scale), baseFee, feeCap, tip, to, input)
		if !ok {
			return
		}

		if run.paid.Cmp(new(big.Int).Add(run.burnt, run.tipped)) != 0 {
			t.Fatalf("value not conserved: paid %v, burnt %v, tipped %v", run.paid, run.burnt, run.tipped)
		}

		effTip := new(big.Int).Sub(feeCap, baseFee)
		if tip.Cmp(effTip) < 0 {
			effTip.Set(tip)
		}

		nominal := new(big.Int).Mul(new(big.Int).SetUint64(run.used), new(big.Int).Add(baseFee, effTip))
		refund := new(big.Int).Sub(nominal, run.paid)

		if refund.Sign() < 0 || refund.Cmp(run.refundLogged) != 0 {
			t.Fatalf("refund %v, logged %v", refund, run.refundLogged)
		}

		if want := PaymentLaneDiscountFor(lane, baseFee, effTip, uint64(scale), run.laneGas); refund.Cmp(want) != 0 {
			t.Fatalf("refund %v, quote arithmetic %v (laneGas %d)", refund, want, run.laneGas)
		}

		if run.laneGas > run.used {
			t.Fatalf("lane gas %d above gas used %d", run.laneGas, run.used)
		}

		if !lane.IsAsset(to) && to != laneWallet && to != laneSwallow && to != laneDelegate && run.laneGas != 0 {
			t.Fatalf("unlisted target %x discounted", to)
		}

		off, ok := runLaneFuzzTx(t, lane, 0, baseFee, feeCap, tip, to, input)
		if !ok || off.used != run.used || off.failed != run.failed {
			t.Fatalf("discount changed execution: used %d/%d, failed %v/%v", run.used, off.used, run.failed, off.failed)
		}

		if off.refundLogged.Sign() != 0 || off.paid.Cmp(nominal) != 0 {
			t.Fatalf("scale 0 refunded %v (paid %v of %v)", off.refundLogged, off.paid, nominal)
		}
	})
}
