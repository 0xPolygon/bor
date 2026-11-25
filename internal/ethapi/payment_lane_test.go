package ethapi

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

// laneTestToken stands in for a listed token: it logs Transfer(caller, to,
// amount) for transfer and Transfer(from, to, amount) otherwise, writes slot
// 0 and stops. The payment lane only credits a call that logged its transfer.
var laneTestToken = common.FromHex("60003560e01c63a9059cbb146047576044356000526024356004357fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a36001600055005b602435600052600435337fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a3600160005500")

func laneTransferInput(to common.Address) []byte {
	out := []byte{0xa9, 0x05, 0x9c, 0xbb}
	out = append(out, common.LeftPadBytes(to[:], 32)...)

	return append(out, common.LeftPadBytes([]byte{1}, 32)...)
}

// quoteDiscount is the discount bor_paymentLaneQuote should report. It reads
// the preset in force and the scale that parent produces, so a retune of the
// base discount, the tip cap, or the ramp does not need a new expected constant.
func quoteDiscount(lane *params.PaymentLaneConfig, parent *types.Header, baseFee, tip *big.Int, gas uint64) (*big.Int, uint64) {
	scale := lane.ScaleForParent(parent.GasUsed, parent.GasLimit)

	return core.PaymentLaneDiscountFor(lane, baseFee, tip, scale, gas), scale
}

func TestPaymentLaneReceiptAndQuote(t *testing.T) {
	t.Parallel()

	var (
		key, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		sender = crypto.PubkeyToAddress(key.PublicKey)
		token  = common.HexToAddress("0x70ce0001")
		other  = common.HexToAddress("0x70ce0002")
		payee  = common.HexToAddress("0xbee")
		tip    = big.NewInt(100_000_000_000)
	)

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(0)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{bor.PaymentLaneBlock.String(): params.DefaultPaymentLaneConfig([]common.Address{token})}
	cfg.Bor = &bor

	code := laneTestToken
	gspec := &core.Genesis{
		Config:  &cfg,
		BaseFee: big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			sender: {Balance: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(100))},
			token:  {Code: code},
			other:  {Code: code},
		},
	}
	signer := types.LatestSigner(&cfg)

	backend := newTestBackend(t, 2, gspec, ethash.NewFaker(), func(i int, b *core.BlockGen) {
		tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID: cfg.ChainID, Nonce: b.TxNonce(sender), GasTipCap: tip,
			GasFeeCap: new(big.Int).Add(new(big.Int).Mul(b.BaseFee(), big.NewInt(2)), tip), Gas: 100_000, To: &token,
			Data: laneTransferInput(payee),
		})
		require.NoError(t, err)
		b.AddTx(tx)
	})

	t.Run("receipt", func(t *testing.T) {
		block, err := backend.BlockByNumber(context.Background(), 2)
		require.NoError(t, err)
		paid := block.Transactions()[0]

		receipts, err := backend.GetReceipts(context.Background(), block.Hash())
		require.NoError(t, err)

		for _, marshal := range []func() map[string]interface{}{
			func() map[string]interface{} {
				return marshalReceipt(receipts[0], block.Hash(), 2, signer, paid, 0, false)
			},
			func() map[string]interface{} {
				return MarshalReceipt(receipts[0], block.Hash(), 2, signer, paid, 0)
			},
		} {
			receipt := marshal()
			lane, ok := receipt["paymentLane"].(map[string]interface{})
			require.True(t, ok, "paymentLane field")

			gasUsed := uint64(receipt["gasUsed"].(hexutil.Uint64))
			price := receipt["effectiveGasPrice"].(*hexutil.Big).ToInt()
			discount := new(big.Int).Add(lane["baseFeeDiscount"].(*hexutil.Big).ToInt(), lane["tipDiscount"].(*hexutil.Big).ToInt())
			nominal := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), price)

			require.Equal(t, hexutil.Uint64(gasUsed), lane["gas"])
			require.Positive(t, discount.Sign())
			require.Equal(t, new(big.Int).Sub(nominal, discount), lane["fee"].(*hexutil.Big).ToInt())
		}
	})

	t.Run("quote", func(t *testing.T) {
		api := NewBorAPI(backend)
		tipArg := (*hexutil.Big)(tip)

		quote := func(to common.Address) map[string]interface{} {
			q, err := api.PaymentLaneQuote(context.Background(), TransactionArgs{
				From: &sender, To: &to, MaxPriorityFeePerGas: tipArg,
				Input: func() *hexutil.Bytes { b := hexutil.Bytes(laneTransferInput(payee)); return &b }(),
			})
			require.NoError(t, err)

			return q
		}

		q := quote(token)
		require.Equal(t, q["gasUsed"], q["paymentGas"])
		require.Equal(t, tipArg, q["tip"])

		parent, err := backend.HeaderByNumber(context.Background(), rpc.LatestBlockNumber)
		require.NoError(t, err)
		lane := cfg.Bor.PaymentLaneAt(new(big.Int).Add(parent.Number, common.Big1))
		gasUsed := uint64(q["gasUsed"].(hexutil.Uint64))
		baseFee := q["baseFee"].(*hexutil.Big).ToInt()
		wantDiscount, scale := quoteDiscount(lane, parent, baseFee, tip, gasUsed)

		require.Equal(t, hexutil.Uint64(scale), q["scaleBps"])

		nominal, discount, fee := q["nominalFee"].(*hexutil.Big).ToInt(), q["discount"].(*hexutil.Big).ToInt(), q["fee"].(*hexutil.Big).ToInt()
		require.Positive(t, discount.Sign())
		require.Equal(t, wantDiscount, discount)
		require.Equal(t, new(big.Int).Sub(nominal, discount), fee)

		u := quote(other)
		require.Zero(t, uint64(u["paymentGas"].(hexutil.Uint64)))
		require.Zero(t, u["discount"].(*hexutil.Big).ToInt().Sign())
	})
}

func TestAddPaymentLaneFieldsWithoutLog(t *testing.T) {
	t.Parallel()

	fields := map[string]interface{}{}
	addPaymentLaneFields(fields, &types.Receipt{Logs: []*types.Log{nil, {Address: common.HexToAddress("0x1010")}}})
	require.NotContains(t, fields, "paymentLane")
}

// At block N-1 the quote must already price the transfer as block N (the
// fork block) will, metering included.
func TestPaymentLaneQuoteAtForkBoundary(t *testing.T) {
	t.Parallel()

	var (
		key, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		sender = crypto.PubkeyToAddress(key.PublicKey)
		token  = common.HexToAddress("0x70ce0001")
	)

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(3)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{bor.PaymentLaneBlock.String(): params.DefaultPaymentLaneConfig([]common.Address{token})}
	cfg.Bor = &bor

	gspec := &core.Genesis{
		Config:  &cfg,
		BaseFee: big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			sender: {Balance: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(100))},
			token:  {Code: laneTestToken},
		},
	}

	backend := newTestBackend(t, 2, gspec, ethash.NewFaker(), func(int, *core.BlockGen) {})
	input := hexutil.Bytes(laneTransferInput(common.HexToAddress("0xbee")))

	q, err := NewBorAPI(backend).PaymentLaneQuote(context.Background(), TransactionArgs{From: &sender, To: &token, Input: &input})
	require.NoError(t, err)
	require.Equal(t, q["gasUsed"], q["paymentGas"])
	require.Equal(t, hexutil.Uint64(params.PaymentLaneMaxBps), q["scaleBps"])
	require.Positive(t, q["discount"].(*hexutil.Big).ToInt().Sign())
}

type noHeaderBackend struct{ *testBackend }

func (noHeaderBackend) HeaderByNumber(context.Context, rpc.BlockNumber) (*types.Header, error) {
	return nil, errors.New("no header")
}

type noStateBackend struct{ *testBackend }

func (noStateBackend) StateAndHeaderByNumberOrHash(context.Context, rpc.BlockNumberOrHash) (*state.StateDB, *types.Header, error) {
	return nil, nil, errors.New("no state")
}

type noTipBackend struct{ *testBackend }

func (noTipBackend) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return nil, errors.New("no tip suggestion")
}

type nilHeaderBackend struct{ *testBackend }

func (nilHeaderBackend) HeaderByNumber(context.Context, rpc.BlockNumber) (*types.Header, error) {
	return nil, nil
}

func TestPaymentLaneQuoteErrors(t *testing.T) {
	t.Parallel()

	var (
		key, _    = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		sender    = crypto.PubkeyToAddress(key.PublicKey)
		reverter  = common.HexToAddress("0x7e7e")
		invalidOp = common.HexToAddress("0x7e7f")
		withData  = common.HexToAddress("0x7e80")
	)

	cfg := *params.BorUnittestChainConfig
	gspec := &core.Genesis{
		Config:  &cfg,
		BaseFee: big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			sender:    {Balance: big.NewInt(1e18)},
			reverter:  {Code: []byte{byte(vm.PUSH1), 0, byte(vm.PUSH1), 0, byte(vm.REVERT)}},
			invalidOp: {Code: []byte{byte(vm.INVALID)}},
			// mstore(0, 0x2a); revert(0, 32)
			withData: {Code: []byte{byte(vm.PUSH1), 0x2a, byte(vm.PUSH1), 0, byte(vm.MSTORE), byte(vm.PUSH1), 32, byte(vm.PUSH1), 0, byte(vm.REVERT)}},
		},
	}
	backend := newTestBackend(t, 1, gspec, ethash.NewFaker(), func(int, *core.BlockGen) {})

	_, err := NewBorAPI(backend).PaymentLaneQuote(context.Background(), TransactionArgs{From: &sender, To: &reverter})
	require.ErrorContains(t, err, "execution reverted")

	_, err = NewBorAPI(backend).PaymentLaneQuote(context.Background(), TransactionArgs{From: &sender, To: &withData})
	var revErr *revertError
	require.ErrorAs(t, err, &revErr)
	require.Equal(t, "0x000000000000000000000000000000000000000000000000000000000000002a", revErr.ErrorData())

	_, err = NewBorAPI(backend).PaymentLaneQuote(context.Background(), TransactionArgs{From: &sender, To: &invalidOp})
	require.ErrorContains(t, err, "invalid opcode")
	require.NotContains(t, err.Error(), "execution reverted")

	_, err = NewBorAPI(noHeaderBackend{backend}).PaymentLaneQuote(context.Background(), TransactionArgs{From: &sender, To: &reverter})
	require.ErrorContains(t, err, "latest header unavailable: no header")

	_, err = NewBorAPI(nilHeaderBackend{backend}).PaymentLaneQuote(context.Background(), TransactionArgs{From: &sender, To: &reverter})
	require.EqualError(t, err, "latest header unavailable")

	_, err = NewBorAPI(noStateBackend{backend}).PaymentLaneQuote(context.Background(), TransactionArgs{From: &sender, To: &sender})
	require.ErrorContains(t, err, "no state")

	_, err = NewBorAPI(noTipBackend{backend}).PaymentLaneQuote(context.Background(), TransactionArgs{From: &sender, To: &sender})
	require.EqualError(t, err, "no tip suggestion")

	// No lane on this chain: the quote is the nominal fee.
	q, err := NewBorAPI(backend).PaymentLaneQuote(context.Background(), TransactionArgs{From: &sender, To: &sender})
	require.NoError(t, err)
	require.Zero(t, uint64(q["scaleBps"].(hexutil.Uint64)))
	require.Equal(t, q["nominalFee"], q["fee"])
}

type fixedTipBackend struct{ *testBackend }

func (fixedTipBackend) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return big.NewInt(50), nil
}

func TestPaymentLaneQuoteTip(t *testing.T) {
	t.Parallel()

	backend := newTestBackend(t, 1, &core.Genesis{Config: params.BorUnittestChainConfig, BaseFee: big.NewInt(params.InitialBaseFee), Alloc: types.GenesisAlloc{}}, ethash.NewFaker(), func(int, *core.BlockGen) {})
	api := NewBorAPI(fixedTipBackend{backend})
	base := big.NewInt(100)
	h := func(v int64) *hexutil.Big { return (*hexutil.Big)(big.NewInt(v)) }

	suggested, err := fixedTipBackend{backend}.SuggestGasTipCap(context.Background())
	require.NoError(t, err)

	tests := []struct {
		name string
		args TransactionArgs
		want *big.Int
	}{
		{"priority fee only", TransactionArgs{MaxPriorityFeePerGas: h(30)}, big.NewInt(30)},
		{"priority fee within fee cap", TransactionArgs{MaxPriorityFeePerGas: h(30), MaxFeePerGas: h(130)}, big.NewInt(30)},
		{"fee cap leaves less headroom", TransactionArgs{MaxPriorityFeePerGas: h(30), MaxFeePerGas: h(129)}, big.NewInt(29)},
		{"fee cap below base fee", TransactionArgs{MaxPriorityFeePerGas: h(30), MaxFeePerGas: h(90)}, big.NewInt(0)},
		{"fee cap only caps suggested tip", TransactionArgs{MaxFeePerGas: h(130)}, big.NewInt(30)},
		{"fee cap only above suggested tip", TransactionArgs{MaxFeePerGas: h(500)}, big.NewInt(50)},
		{"fee cap only below base fee", TransactionArgs{MaxFeePerGas: h(90)}, big.NewInt(0)},
		{"legacy gas price", TransactionArgs{GasPrice: h(125)}, big.NewInt(25)},
		{"legacy gas price at base fee", TransactionArgs{GasPrice: h(100)}, big.NewInt(0)},
		{"legacy gas price below base fee", TransactionArgs{GasPrice: h(99)}, big.NewInt(0)},
		{"suggested", TransactionArgs{}, suggested},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tip, err := api.quoteTip(context.Background(), tt.args, base)
			require.NoError(t, err)
			require.Equal(t, tt.want.String(), tip.String())
		})
	}
}

func TestNextBlockLaneWithoutBor(t *testing.T) {
	t.Parallel()

	lane, scale := nextBlockLane(&params.ChainConfig{ChainID: big.NewInt(1)}, &types.Header{Number: big.NewInt(1)})
	require.Nil(t, lane)
	require.Zero(t, scale)
}

type forkHeaderBackend struct {
	ChainContextBackend
	canonical *types.Header
	byHash    map[common.Hash]*types.Header
}

func (b forkHeaderBackend) HeaderByNumber(_ context.Context, n rpc.BlockNumber) (*types.Header, error) {
	if b.canonical.Number.Int64() != int64(n) {
		return nil, errors.New("not found")
	}

	return b.canonical, nil
}

func (b forkHeaderBackend) HeaderByHash(_ context.Context, h common.Hash) (*types.Header, error) {
	if hdr, ok := b.byHash[h]; ok {
		return hdr, nil
	}

	return nil, errors.New("not found")
}

func TestChainContextGetHeader(t *testing.T) {
	t.Parallel()

	canonical := &types.Header{Number: big.NewInt(5), Extra: []byte("canonical")}
	sibling := &types.Header{Number: big.NewInt(5), Extra: []byte("reorged-out")}
	backend := forkHeaderBackend{
		canonical: canonical,
		byHash:    map[common.Hash]*types.Header{canonical.Hash(): canonical, sibling.Hash(): sibling},
	}
	cc := NewChainContext(context.Background(), backend)

	tests := []struct {
		name   string
		hash   common.Hash
		number uint64
		want   *types.Header
	}{
		{"canonical", canonical.Hash(), 5, canonical},
		{"non-canonical sibling", sibling.Hash(), 5, sibling},
		{"number mismatch", sibling.Hash(), 6, nil},
		{"unknown hash", common.HexToHash("0x01"), 5, nil},
		{"unknown number and hash", common.HexToHash("0x01"), 9, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, cc.GetHeader(tt.hash, tt.number))
		})
	}
}
