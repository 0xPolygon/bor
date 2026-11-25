package core

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/program"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

// laneTestToken stands in for a listed token: it logs Transfer(caller, to,
// amount) for transfer and Transfer(from, to, amount) otherwise, writes slot
// 0 and stops. The payment lane only credits a call that logged its transfer.
var laneTestToken = common.FromHex("60003560e01c63a9059cbb146047576044356000526024356004357fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a36001600055005b602435600052600435337fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a3600160005500")

var (
	laneSender   = common.HexToAddress("0x5e4de7")
	lanePayee    = common.HexToAddress("0xbee")
	laneCoinbase = common.HexToAddress("0xc0ffee")
	laneToken    = common.HexToAddress("0x70ce0001")
	laneRevToken = common.HexToAddress("0x70ce0002")
	laneOther    = common.HexToAddress("0x70ce0003")
	laneBurner   = common.HexToAddress("0x70ce0004")
	laneWallet   = common.HexToAddress("0x3a11e701")
	laneRevWrap  = common.HexToAddress("0x3a11e702")
	laneSwallow  = common.HexToAddress("0x3a11e703")
	laneDelegate = common.HexToAddress("0x3a11e704")

	laneBaseFee = big.NewInt(250_000_000_000)
	laneTip     = big.NewInt(100_000_000_000)
	laneTipCap  = big.NewInt(30_000_000_000)
)

func laneScale(active bool) uint64 {
	if active {
		return params.PaymentLaneMaxBps
	}

	return 0
}

func laneChainConfig(t *testing.T) *params.ChainConfig {
	t.Helper()

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(0)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{bor.PaymentLaneBlock.String(): {
		Assets:              []common.Address{laneToken, laneRevToken, laneBurner},
		BaseFeeDiscountBps:  5000,
		TipCap:              laneTipCap,
		ActiveGasPercentage: 70,
	}}
	cfg.Bor = &bor
	require.True(t, cfg.IsLondon(common.Big1))

	return &cfg
}

func transferInput(to common.Address, amount int64) []byte {
	out := []byte{0xa9, 0x05, 0x9c, 0xbb}
	out = append(out, common.LeftPadBytes(to[:], 32)...)

	return append(out, common.LeftPadBytes(big.NewInt(amount).Bytes(), 32)...)
}

// forwarder CALLs target with the full calldata, then either stops or reverts.
func forwarder(target common.Address, revertAfter bool) []byte {
	p := program.New().
		Op(vm.CALLDATASIZE).Push(0).Push(0).Op(vm.CALLDATACOPY).
		Push(0).Push(0).Op(vm.CALLDATASIZE).Push(0).Push(0).Push(target).Op(vm.GAS).Op(vm.CALL).
		Op(vm.POP)
	if revertAfter {
		return p.Push(0).Push(0).Op(vm.REVERT).Bytes()
	}

	return p.Op(vm.STOP).Bytes()
}

// delegator DELEGATECALLs a forwarder that reverts, ignores the failure and
// succeeds, so only the failed delegate frame can drop the inner credit.
func delegator(target common.Address) []byte {
	return program.New().
		Op(vm.CALLDATASIZE).Push(0).Push(0).Op(vm.CALLDATACOPY).
		Push(0).Push(0).Op(vm.CALLDATASIZE).Push(0).Push(target).Op(vm.GAS).Op(vm.DELEGATECALL).
		Op(vm.POP).Op(vm.STOP).Bytes()
}

func laneState(t *testing.T) *state.StateDB {
	t.Helper()

	db, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)

	token := laneTestToken
	db.SetCode(laneToken, token, tracing.CodeChangeUnspecified)
	db.SetCode(laneOther, token, tracing.CodeChangeUnspecified)
	db.SetCode(laneRevToken, program.New().Push(0).Push(0).Op(vm.REVERT).Bytes(), tracing.CodeChangeUnspecified)
	db.SetCode(laneBurner, []byte{byte(vm.JUMPDEST), byte(vm.PUSH1), 0, byte(vm.JUMP)}, tracing.CodeChangeUnspecified)
	db.SetCode(laneWallet, forwarder(laneToken, false), tracing.CodeChangeUnspecified)
	db.SetCode(laneRevWrap, forwarder(laneToken, true), tracing.CodeChangeUnspecified)
	db.SetCode(laneSwallow, forwarder(laneRevWrap, false), tracing.CodeChangeUnspecified)
	db.SetCode(laneDelegate, delegator(laneRevWrap), tracing.CodeChangeUnspecified)
	db.AddBalance(laneSender, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)

	return db
}

type laneOutcome struct {
	result      *ExecutionResult
	senderPaid  *big.Int
	burnt       *big.Int
	tipped      *big.Int
	nominalPaid *big.Int
	laneLogs    []*types.Log
}

func runLaneTx(t *testing.T, cfg *params.ChainConfig, active bool, to common.Address, input []byte) laneOutcome {
	t.Helper()

	return runLaneTxWith(t, cfg, active, to, input, nil)
}

func runLaneTxWith(t *testing.T, cfg *params.ChainConfig, active bool, to common.Address, input []byte, mutate func(*Message)) laneOutcome {
	t.Helper()

	db := laneState(t)
	before := db.GetBalance(laneSender).ToBig()

	blockCtx := vm.BlockContext{
		CanTransfer:         CanTransfer,
		Transfer:            Transfer,
		GetHash:             func(uint64) common.Hash { return common.Hash{} },
		Coinbase:            laneCoinbase,
		BlockNumber:         big.NewInt(10),
		Time:                10,
		Difficulty:          big.NewInt(1),
		BaseFee:             new(big.Int).Set(laneBaseFee),
		GasLimit:            30_000_000,
		PaymentLaneScaleBps: laneScale(active),
	}
	evm := vm.NewEVM(blockCtx, db, cfg, vm.Config{})

	gasPrice := new(big.Int).Add(laneBaseFee, laneTip)
	msg := &Message{
		To:              &to,
		From:            laneSender,
		Value:           new(big.Int),
		GasLimit:        200_000,
		GasPrice:        gasPrice,
		GasFeeCap:       gasPrice,
		GasTipCap:       new(big.Int).Set(laneTip),
		Data:            input,
		SkipNonceChecks: true,
	}

	if mutate != nil {
		mutate(msg)
	}

	res, err := ApplyMessage(evm, msg, new(GasPool).AddGas(30_000_000))
	require.NoError(t, err)

	return laneOutcome{
		result:      res,
		senderPaid:  new(big.Int).Sub(before, db.GetBalance(laneSender).ToBig()),
		burnt:       db.GetBalance(res.BurntContractAddress).ToBig(),
		tipped:      db.GetBalance(laneCoinbase).ToBig(),
		nominalPaid: new(big.Int).Mul(new(big.Int).SetUint64(res.UsedGas), gasPrice),
		laneLogs:    laneLogsOf(db.Logs()),
	}
}

func laneLogsOf(logs []*types.Log) []*types.Log {
	var out []*types.Log

	for _, l := range logs {
		if len(l.Topics) > 0 && l.Topics[0] == paymentLaneLogSig {
			out = append(out, l)
		}
	}

	return out
}

// requireLaneLog checks the discount log reconciles exactly with what the
// sender was not charged.
func requireLaneLog(t *testing.T, out laneOutcome, wantGas uint64) {
	t.Helper()

	require.Len(t, out.laneLogs, 1)
	l := out.laneLogs[0]
	require.Equal(t, feeAddress, l.Address)
	require.Equal(t, []common.Hash{paymentLaneLogSig, laneSender.Hash()}, l.Topics)
	require.Len(t, l.Data, 128)

	laneGas := new(big.Int).SetBytes(l.Data[:32])
	refund := new(big.Int).Add(new(big.Int).SetBytes(l.Data[32:64]), new(big.Int).SetBytes(l.Data[64:96]))
	require.GreaterOrEqual(t, new(big.Int).SetBytes(l.Data[96:]).Uint64(), laneGas.Uint64(), "the payment share covers the payment gas")
	require.Equal(t, new(big.Int).Sub(out.nominalPaid, out.senderPaid), refund)

	if wantGas != 0 {
		require.Equal(t, wantGas, laneGas.Uint64())
	} else {
		require.Positive(t, laneGas.Sign())
		require.Less(t, laneGas.Uint64(), out.result.UsedGas)
	}
}

func lanePrice(gas uint64) *big.Int {
	perGas := new(big.Int).Div(laneBaseFee, big.NewInt(2))
	perGas.Add(perGas, laneTipCap)

	return perGas.Mul(perGas, new(big.Int).SetUint64(gas))
}

func TestPaymentLaneSettlement(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)

	tests := []struct {
		name       string
		to         common.Address
		input      []byte
		active     bool
		discounted bool // all gas at lane price
		partial    bool // only the token frame at lane price
	}{
		{"direct transfer", laneToken, transferInput(lanePayee, 1), true, true, false},
		{"direct transfer, lane inactive", laneToken, transferInput(lanePayee, 1), false, false, false},
		{"direct zero amount", laneToken, transferInput(lanePayee, 0), true, false, false},
		{"direct self transfer", laneToken, transferInput(laneSender, 1), true, false, false},
		{"unlisted token", laneOther, transferInput(lanePayee, 1), true, false, false},
		{"listed token reverts", laneRevToken, transferInput(lanePayee, 1), true, false, false},
		{"non-payment selector", laneToken, append([]byte{0x09, 0x5e, 0xa7, 0xb3}, make([]byte, 64)...), true, false, false},
		{"via wallet", laneWallet, transferInput(lanePayee, 1), true, false, true},
		{"via wallet, lane inactive", laneWallet, transferInput(lanePayee, 1), false, false, false},
		{"wallet reverts after payment", laneRevWrap, transferInput(lanePayee, 1), true, false, false},
		{"swallowed reverting wrapper", laneSwallow, transferInput(lanePayee, 1), true, false, false},
		{"swallowed failing delegatecall", laneDelegate, transferInput(lanePayee, 1), true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := runLaneTx(t, cfg, tt.active, tt.to, tt.input)

			// Fees are conserved whatever the lane does.
			require.Equal(t, out.senderPaid, new(big.Int).Add(out.burnt, out.tipped))
			require.Equal(t, out.burnt, out.result.FeeBurnt)
			require.Equal(t, out.tipped, out.result.FeeTipped)

			switch {
			case tt.discounted:
				require.Equal(t, lanePrice(out.result.UsedGas), out.senderPaid)
				requireLaneLog(t, out, out.result.UsedGas)
			case tt.partial:
				require.Positive(t, out.senderPaid.Cmp(lanePrice(out.result.UsedGas)))
				require.Negative(t, out.senderPaid.Cmp(out.nominalPaid))
				requireLaneLog(t, out, 0)
			default:
				require.Equal(t, out.nominalPaid, out.senderPaid)
				require.Empty(t, out.laneLogs)
			}
		})
	}
}

func TestPaymentLaneGasUnchanged(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	for _, to := range []common.Address{laneToken, laneWallet, laneRevWrap} {
		on := runLaneTx(t, cfg, true, to, transferInput(lanePayee, 1))
		off := runLaneTx(t, cfg, false, to, transferInput(lanePayee, 1))
		require.Equal(t, off.result.UsedGas, on.result.UsedGas, to.Hex())
		require.Equal(t, off.result.Err, on.result.Err, to.Hex())
	}
}

func TestPaymentLaneRequiresFork(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	cfg.Bor.PaymentLaneBlock = big.NewInt(11)

	out := runLaneTx(t, cfg, true, laneToken, transferInput(lanePayee, 1))
	require.Equal(t, out.nominalPaid, out.senderPaid)
}

func TestPaymentLaneTipBelowCapUnchanged(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).TipCap = new(big.Int).Add(laneTip, common.Big1)
	cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).BaseFeeDiscountBps = 0

	out := runLaneTx(t, cfg, true, laneToken, transferInput(lanePayee, 1))
	require.Equal(t, out.nominalPaid, out.senderPaid)
}

func TestPaymentLaneNoFeeBurnOrTipParity(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	inline := runLaneTx(t, cfg, true, laneWallet, transferInput(lanePayee, 1))

	db := laneState(t)
	before := db.GetBalance(laneSender).ToBig()
	blockCtx := vm.BlockContext{
		CanTransfer: CanTransfer, Transfer: Transfer,
		GetHash:  func(uint64) common.Hash { return common.Hash{} },
		Coinbase: laneCoinbase, BlockNumber: big.NewInt(10), Time: 10, Difficulty: big.NewInt(1),
		BaseFee: new(big.Int).Set(laneBaseFee), GasLimit: 30_000_000, PaymentLaneScaleBps: params.PaymentLaneMaxBps,
	}
	gasPrice := new(big.Int).Add(laneBaseFee, laneTip)
	msg := &Message{
		To: &laneWallet, From: laneSender, Value: new(big.Int), GasLimit: 200_000,
		GasPrice: gasPrice, GasFeeCap: gasPrice, GasTipCap: new(big.Int).Set(laneTip),
		Data: transferInput(lanePayee, 1), SkipNonceChecks: true,
	}

	res, err := ApplyMessageNoFeeBurnOrTip(vm.NewEVM(blockCtx, db, cfg, vm.Config{}), *msg, new(GasPool).AddGas(30_000_000))
	require.NoError(t, err)

	// Deferred callers credit FeeBurnt/FeeTipped themselves; the sender side
	// (including the lane refund) must already match the inline path.
	require.Equal(t, inline.result.FeeBurnt, res.FeeBurnt)
	require.Equal(t, inline.result.FeeTipped, res.FeeTipped)
	require.Equal(t, inline.senderPaid, new(big.Int).Sub(before, db.GetBalance(laneSender).ToBig()))
	require.True(t, db.GetBalance(res.BurntContractAddress).IsZero())
	require.True(t, db.GetBalance(laneCoinbase).IsZero())
}

func TestPaymentLaneZeroTipStillDiscountsBase(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	db := laneState(t)
	before := db.GetBalance(laneSender).ToBig()

	blockCtx := vm.BlockContext{
		CanTransfer: CanTransfer, Transfer: Transfer,
		GetHash:  func(uint64) common.Hash { return common.Hash{} },
		Coinbase: laneCoinbase, BlockNumber: big.NewInt(10), Time: 10, Difficulty: big.NewInt(1),
		BaseFee: new(big.Int).Set(laneBaseFee), GasLimit: 30_000_000, PaymentLaneScaleBps: params.PaymentLaneMaxBps,
	}
	msg := &Message{
		To: &laneToken, From: laneSender, Value: new(big.Int), GasLimit: 200_000,
		GasPrice: new(big.Int).Set(laneBaseFee), GasFeeCap: new(big.Int).Set(laneBaseFee), GasTipCap: new(big.Int),
		Data: transferInput(lanePayee, 1), SkipNonceChecks: true,
	}

	res, err := ApplyMessage(vm.NewEVM(blockCtx, db, cfg, vm.Config{}), msg, new(GasPool).AddGas(30_000_000))
	require.NoError(t, err)

	paid := new(big.Int).Sub(before, db.GetBalance(laneSender).ToBig())
	want := new(big.Int).Div(laneBaseFee, big.NewInt(2))
	require.Equal(t, want.Mul(want, new(big.Int).SetUint64(res.UsedGas)), paid)
	require.Zero(t, res.FeeTipped.Sign())
}

func TestPaymentLaneAllowancePullNotDiscounted(t *testing.T) {
	t.Parallel()

	input := []byte{0x23, 0xb8, 0x72, 0xdd}
	input = append(input, common.LeftPadBytes(laneWallet[:], 32)...)
	input = append(input, common.LeftPadBytes(lanePayee[:], 32)...)
	input = append(input, common.LeftPadBytes([]byte{1}, 32)...)

	out := runLaneTx(t, laneChainConfig(t), true, laneToken, input)
	require.Equal(t, out.nominalPaid, out.senderPaid)
	require.Empty(t, out.laneLogs)
}

// runLanePricing executes a direct transfer at the given scale with the
// given lane pricing and returns what the sender paid and the gas used.
func runLanePricing(t *testing.T, mutate func(*params.PaymentLaneConfig), scale uint64) (paid *big.Int, gas uint64) {
	t.Helper()

	cfg := laneChainConfig(t)
	mutate(cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock))
	require.NoError(t, cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).Validate())

	db := laneState(t)
	before := db.GetBalance(laneSender).ToBig()
	blockCtx := vm.BlockContext{
		CanTransfer: CanTransfer, Transfer: Transfer,
		GetHash:  func(uint64) common.Hash { return common.Hash{} },
		Coinbase: laneCoinbase, BlockNumber: big.NewInt(10), Time: 10, Difficulty: big.NewInt(1),
		BaseFee: new(big.Int).Set(laneBaseFee), GasLimit: 30_000_000, PaymentLaneScaleBps: scale,
	}
	gasPrice := new(big.Int).Add(laneBaseFee, laneTip)
	msg := &Message{
		To: &laneToken, From: laneSender, Value: new(big.Int), GasLimit: 200_000,
		GasPrice: gasPrice, GasFeeCap: gasPrice, GasTipCap: new(big.Int).Set(laneTip),
		Data: transferInput(lanePayee, 1), SkipNonceChecks: true,
	}

	res, err := ApplyMessage(vm.NewEVM(blockCtx, db, cfg, vm.Config{}), msg, new(GasPool).AddGas(30_000_000))
	require.NoError(t, err)

	burnt := db.GetBalance(res.BurntContractAddress).ToBig()
	tipped := db.GetBalance(laneCoinbase).ToBig()
	paid = new(big.Int).Sub(before, db.GetBalance(laneSender).ToBig())
	require.Zero(t, paid.Cmp(new(big.Int).Add(burnt, tipped)))

	return paid, res.UsedGas
}

func gwei(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1_000_000_000)) }

// The lane's two levers are independent: base-only, tip-only, both, full
// discounts and a partial scale all price per gas exactly as configured.
// Base fee 250 gwei, tip 100 gwei.
func TestPaymentLanePricingMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*params.PaymentLaneConfig)
		scale     uint64
		wantPerGs *big.Int
	}{
		{"no discount configured", func(c *params.PaymentLaneConfig) { c.BaseFeeDiscountBps, c.TipCap = 0, nil }, 10_000, gwei(350)},
		{"base only 75%", func(c *params.PaymentLaneConfig) { c.BaseFeeDiscountBps, c.TipCap = 7500, nil }, 10_000, new(big.Int).Add(gwei(62), big.NewInt(500_000_000+100_000_000_000))},
		{"base only 100%", func(c *params.PaymentLaneConfig) {
			c.BaseFeeDiscountBps, c.TipCap, c.ActiveGasPercentage, c.RampEndGasPercentage = 10_000, nil, 65, 0
		}, 10_000, gwei(100)},
		{"tip cap only 30 gwei", func(c *params.PaymentLaneConfig) { c.BaseFeeDiscountBps = 0 }, 10_000, gwei(280)},
		{"tip discount only 50%", func(c *params.PaymentLaneConfig) { c.BaseFeeDiscountBps, c.TipCap, c.TipDiscountBps = 0, nil, 5000 }, 10_000, gwei(300)},
		{"tip discount 100%", func(c *params.PaymentLaneConfig) { c.BaseFeeDiscountBps, c.TipCap, c.TipDiscountBps = 0, nil, 10_000 }, 10_000, gwei(250)},
		{"tip discount then cap", func(c *params.PaymentLaneConfig) { c.BaseFeeDiscountBps, c.TipDiscountBps = 0, 5000 }, 10_000, gwei(280)},
		{"tip discount below cap", func(c *params.PaymentLaneConfig) { c.BaseFeeDiscountBps, c.TipDiscountBps = 0, 8000 }, 10_000, gwei(270)},
		{"everything free", func(c *params.PaymentLaneConfig) {
			c.BaseFeeDiscountBps, c.TipDiscountBps, c.ActiveGasPercentage, c.RampEndGasPercentage = 10_000, 10_000, 65, 0
		}, 10_000, new(big.Int)},
		{"both (50% base, 30 gwei cap), half scale", func(c *params.PaymentLaneConfig) {}, 5_000, big.NewInt(252_500_000_000)},
		{"both, zero scale", func(c *params.PaymentLaneConfig) {}, 0, gwei(350)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			paid, gas := runLanePricing(t, tt.mutate, tt.scale)
			require.Equal(t, new(big.Int).Mul(tt.wantPerGs, new(big.Int).SetUint64(gas)).String(), paid.String())
		})
	}
}

func TestPaymentLaneAllowancePullCountedWhenConfigured(t *testing.T) {
	t.Parallel()

	input := []byte{0x23, 0xb8, 0x72, 0xdd}
	input = append(input, common.LeftPadBytes(laneWallet[:], 32)...)
	input = append(input, common.LeftPadBytes(lanePayee[:], 32)...)
	input = append(input, common.LeftPadBytes([]byte{1}, 32)...)

	cfg := laneChainConfig(t)
	cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).CountAllowancePulls = true

	out := runLaneTx(t, cfg, true, laneToken, input)
	require.Equal(t, lanePrice(out.result.UsedGas), out.senderPaid)
	requireLaneLog(t, out, out.result.UsedGas)
}

// A direct payment's credit is its base cost, the calldata of the payment
// itself and the token's own execution: trailing calldata, access lists and
// authorizations are paid at full price and count as non-payment gas.
func TestPaymentLaneDirectCreditExcludesExtras(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	input := transferInput(lanePayee, 1)

	plain := runLaneTx(t, cfg, true, laneToken, input)
	require.Equal(t, plain.result.UsedGas, plain.result.PaymentLaneGas, "a canonical direct payment is all payment gas")

	extras := map[string]laneOutcome{
		"a few trailing bytes": runLaneTx(t, cfg, true, laneToken, append(append([]byte{}, input...), bytes.Repeat([]byte{0xff}, 32)...)),
		"access list": runLaneTxWith(t, cfg, true, laneToken, input, func(m *Message) {
			m.AccessList = types.AccessList{{Address: common.HexToAddress("0xdead"), StorageKeys: []common.Hash{{1}, {2}}}}
		}),
		"authorizations": runLaneTxWith(t, cfg, true, laneToken, input, func(m *Message) {
			m.SetCodeAuthorizations = []types.SetCodeAuthorization{{Address: common.HexToAddress("0xdead")}, {Address: common.HexToAddress("0xbeef")}}
		}),
	}

	for name, out := range extras {
		require.Greater(t, out.result.UsedGas, plain.result.UsedGas, name)
		require.Equal(t, plain.result.PaymentLaneGas, out.result.PaymentLaneGas, name)
		requireLaneLog(t, out, plain.result.PaymentLaneGas)
	}

	// Padding beyond that makes the call no payment at all: full price.
	padded := runLaneTx(t, cfg, true, laneToken, append(append([]byte{}, input...), bytes.Repeat([]byte{0xff}, 3_500)...))
	require.Zero(t, padded.result.PaymentLaneGas)
	require.Equal(t, padded.nominalPaid, padded.senderPaid)
}

// Under a non-payment cap, a direct payment call counts toward the payment
// share whether or not it pays: one that reverts is charged in full, gets no
// discount, and is logged with its gas as payment share, up to what a payment
// can use, so the block builder never has to refuse it for free.
func TestPaymentLaneDirectAttemptCountsTowardCapacity(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).NonPaymentGasCapPercentage = 90

	reverted := runLaneTx(t, cfg, true, laneRevToken, transferInput(lanePayee, 1))
	require.Zero(t, reverted.result.PaymentLaneGas)
	require.Equal(t, reverted.result.UsedGas, reverted.result.PaymentLaneCapacityGas)
	require.Equal(t, reverted.nominalPaid, reverted.senderPaid)
	require.Len(t, reverted.laneLogs, 1)
	require.Equal(t, reverted.result.UsedGas, PaymentLaneCapacityFromLogs(reverted.laneLogs))

	_, ok := PaymentLaneFromLogs(reverted.laneLogs)
	require.False(t, ok, "no payment record for an attempt that paid nothing")

	// Only up to a payment's worth of gas: an attempt that burns more counts
	// the rest as non-payment gas.
	burnt := runLaneTxWith(t, cfg, true, laneBurner, transferInput(lanePayee, 1), func(m *Message) { m.GasLimit = 500_000 })
	require.Equal(t, uint64(500_000), burnt.result.UsedGas)
	bound := PaymentLaneAttemptGas(cfg.Bor.PaymentLaneAt(common.Big0), laneSender, &laneBurner, transferInput(lanePayee, 1))
	require.Equal(t, params.TxGas+7*16+61*4+params.PaymentLaneMaxTransferFrameGas, bound)
	require.Equal(t, bound, burnt.result.PaymentLaneCapacityGas)
	require.Equal(t, bound, PaymentLaneCapacityFromLogs(burnt.laneLogs))

	// A call that is not a payment shape gets no payment share at all.
	other := runLaneTx(t, cfg, true, laneRevToken, transferInput(laneSender, 1))
	require.Zero(t, other.result.PaymentLaneCapacityGas)
	require.Empty(t, other.laneLogs)

	// Without a cap the failed attempt leaves no trace.
	cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).NonPaymentGasCapPercentage = 0
	require.Empty(t, runLaneTx(t, cfg, true, laneRevToken, transferInput(lanePayee, 1)).laneLogs)
}

// The builder's budget counts a direct payment attempt's gas beyond what a
// payment can use as non-payment gas, and rejects the tx before its state is
// finalised when that exceeds the budget.
func TestNonPaymentBudgetCountsBoundedAttempt(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	cfg.Bor.PaymentLaneAt(common.Big0).NonPaymentGasCapPercentage = 90

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	from := crypto.PubkeyToAddress(key.PublicKey)
	signer := types.LatestSigner(cfg)
	input := transferInput(lanePayee, 1)
	bound := PaymentLaneAttemptGas(cfg.Bor.PaymentLaneAt(common.Big0), from, &laneBurner, input)

	run := func(budget uint64) error {
		db := laneState(t)
		db.AddBalance(from, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)

		tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID: cfg.ChainID, GasTipCap: laneTip, GasFeeCap: new(big.Int).Add(laneBaseFee, laneTip),
			Gas: 500_000, To: &laneBurner, Data: input,
		})
		require.NoError(t, err)

		msg, err := TransactionToMessage(tx, signer, laneBaseFee)
		require.NoError(t, err)

		blockCtx := vm.BlockContext{
			CanTransfer: CanTransfer, Transfer: Transfer,
			GetHash:  func(uint64) common.Hash { return common.Hash{} },
			Coinbase: laneCoinbase, BlockNumber: big.NewInt(10), Time: 10, Difficulty: big.NewInt(1),
			BaseFee: new(big.Int).Set(laneBaseFee), GasLimit: 30_000_000, PaymentLaneScaleBps: laneScale(true),
		}
		evm := vm.NewEVM(blockCtx, db, cfg, vm.Config{NonPaymentGasBudget: &budget})

		var used uint64
		_, err = ApplyTransactionWithEVM(msg, new(GasPool).AddGas(30_000_000), db, big.NewInt(10), common.Hash{}, 10, tx, &used, evm)

		return err
	}

	require.NoError(t, run(500_000-bound))
	require.ErrorIs(t, run(500_000-bound-1), ErrNonPaymentGasCap)
}

// TestSettlePaymentLaneAppliesCut calls the settlement helper directly, so a
// helper that drops the tip cut, the base cut, the refund or the deferred-fee
// guard fails here even if execute is later rewired.
func TestSettlePaymentLaneAppliesCut(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	input := transferInput(lanePayee, 1)
	gasPrice := new(big.Int).Add(laneBaseFee, laneTip)

	for _, deferFees := range []bool{false, true} {
		name := "inline fees"
		if deferFees {
			name = "deferred fees"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := laneState(t)
			blockCtx := vm.BlockContext{
				CanTransfer: CanTransfer, Transfer: Transfer,
				GetHash:  func(uint64) common.Hash { return common.Hash{} },
				Coinbase: laneCoinbase, BlockNumber: big.NewInt(10), Time: 10, Difficulty: big.NewInt(1),
				BaseFee: new(big.Int).Set(laneBaseFee), GasLimit: 30_000_000, PaymentLaneScaleBps: params.PaymentLaneMaxBps,
			}
			evm := vm.NewEVM(blockCtx, db, cfg, vm.Config{})
			_, left, err := evm.Call(laneSender, laneToken, input, 200_000, uint256.NewInt(0))
			require.NoError(t, err)

			used := 200_000 - left
			msg := &Message{
				To: &laneToken, From: laneSender, Value: new(big.Int), GasLimit: 200_000,
				GasPrice: gasPrice, GasFeeCap: gasPrice, GasTipCap: new(big.Int).Set(laneTip),
				Data: input, SkipNonceChecks: true,
			}
			st := &stateTransition{msg: msg, state: db, evm: evm, initialGas: used}
			amount := new(big.Int).Mul(new(big.Int).SetUint64(used), laneTip)
			burn := new(big.Int).Mul(new(big.Int).SetUint64(used), laneBaseFee)
			nominalTip := new(big.Int).Set(amount)
			nominalBurn := new(big.Int).Set(burn)
			senderBefore := db.GetBalance(laneSender).ToBig()
			burnt := common.HexToAddress("0xb0a")
			st.noFeeBurnAndTip = deferFees

			cut := st.settlePaymentLane(nil, new(big.Int).Set(laneTip), amount, burn, burnt)
			res := cut.annotate(&ExecutionResult{})

			require.Positive(t, cut.refund.Sign())
			require.Negative(t, amount.Cmp(nominalTip))
			require.Negative(t, burn.Cmp(nominalBurn))
			require.Equal(t, cut.refund, new(big.Int).Sub(new(big.Int).Add(nominalTip, nominalBurn), new(big.Int).Add(amount, burn)))
			require.Equal(t, cut.refund, new(big.Int).Sub(db.GetBalance(laneSender).ToBig(), senderBefore))
			require.Equal(t, cut.gas, res.PaymentLaneGas)
			require.Positive(t, res.PaymentLaneGas)
			require.NotEmpty(t, laneLogsOf(db.Logs()))

			if deferFees {
				require.Zero(t, db.GetBalance(burnt).Sign())
				return
			}

			require.Equal(t, burn, db.GetBalance(burnt).ToBig())
		})
	}
}
