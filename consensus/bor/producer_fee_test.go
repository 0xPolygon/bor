package bor

import (
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
)

func tippedTx(tipCap, feeCap int64) *types.Transaction {
	return types.NewTx(&types.DynamicFeeTx{GasTipCap: big.NewInt(tipCap), GasFeeCap: big.NewInt(feeCap), Gas: 100_000})
}

func gasReceipt(gasUsed uint64) *types.Receipt {
	return &types.Receipt{Status: types.ReceiptStatusSuccessful, GasUsed: gasUsed}
}

func TestBlockPriorityFees(t *testing.T) {
	t.Parallel()

	baseFee := big.NewInt(100)

	tests := []struct {
		name     string
		txs      types.Transactions
		receipts []*types.Receipt
		want     uint64
	}{
		{name: "no transactions"},
		{
			name:     "tip below the fee cap headroom",
			txs:      types.Transactions{tippedTx(30, 1_000)},
			receipts: []*types.Receipt{gasReceipt(21_000)},
			want:     30 * 21_000,
		},
		{
			name:     "tip capped by the fee cap",
			txs:      types.Transactions{tippedTx(30, 110)},
			receipts: []*types.Receipt{gasReceipt(21_000)},
			want:     10 * 21_000,
		},
		{
			name:     "several transactions",
			txs:      types.Transactions{tippedTx(30, 1_000), tippedTx(0, 1_000), tippedTx(7, 1_000)},
			receipts: []*types.Receipt{gasReceipt(21_000), gasReceipt(50_000), gasReceipt(3)},
			want:     30*21_000 + 7*3,
		},
		{
			name:     "legacy transaction pays its price over the base fee",
			txs:      types.Transactions{types.NewTx(&types.LegacyTx{GasPrice: big.NewInt(150), Gas: 21_000})},
			receipts: []*types.Receipt{gasReceipt(21_000)},
			want:     50 * 21_000,
		},
		{
			name: "system transactions after the user ones are not counted",
			txs: types.Transactions{
				tippedTx(30, 1_000),
				types.NewTx(&types.StateSyncTx{}),
				types.NewTx(&types.StakingTx{BlockNumber: 1}),
			},
			receipts: []*types.Receipt{gasReceipt(21_000)},
			want:     30 * 21_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := blockPriorityFees(baseFee, tt.txs, tt.receipts)
			require.NoError(t, err)
			require.Equal(t, uint256.NewInt(tt.want), got)
		})
	}
}

func TestProducerFeeShare_RoundsDown(t *testing.T) {
	t.Parallel()

	tests := []struct{ fees, want uint64 }{
		{0, 0},
		{1, 0},
		{3, 0},
		{4, 1},
		{99, 25},
		{100, 26},
		{630_000, 163_800},
	}

	for _, tt := range tests {
		require.Equal(t, uint256.NewInt(tt.want), producerFeeShare(uint256.NewInt(tt.fees)), "fees %d", tt.fees)
	}

	max := new(uint256.Int).SetAllOne()
	want := new(big.Int).Div(new(big.Int).Mul(max.ToBig(), big.NewInt(26)), big.NewInt(100))
	require.Equal(t, want, producerFeeShare(max).ToBig(), "no overflow at the top of the range")
}

func TestPayProducerFeeShare(t *testing.T) {
	t.Parallel()

	const fork = 512

	var (
		producer = common.HexToAddress("0xbeef")
		baseFee  = big.NewInt(100)
		txs      = types.Transactions{tippedTx(30, 1_000)}
		receipts = []*types.Receipt{gasReceipt(21_000)}
		fees     = uint64(30 * 21_000)
		share    = uint64(163_800) // 26% of fees
	)

	resolve := func() (common.Address, error) { return producer, nil }

	tests := []struct {
		name         string
		number       uint64
		credited     uint64 // what the coinbase holds when the block's transactions are done
		resolve      func() (common.Address, error)
		wantProducer uint64
		wantErr      bool
	}{
		{name: "before the fork", number: fork - 1, credited: fees, resolve: resolve},
		{name: "at the fork", number: fork, credited: fees, resolve: resolve, wantProducer: share},
		{name: "after the fork", number: fork + 1, credited: fees, resolve: resolve, wantProducer: share},
		{name: "coinbase holds less than the share", number: fork, credited: 1_000, resolve: resolve, wantProducer: 1_000},
		{
			name: "producer cannot be resolved", number: fork, credited: fees, wantErr: true,
			resolve: func() (common.Address, error) { return common.Address{}, errors.New("no seal") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, b, genesis, statedb := newStakingChain(t, 16, fork, true, &stakingHeimdall{})

			header := uburuHeader(genesis, b, tt.number)
			header.BaseFee = baseFee

			coinbase := common.HexToAddress(b.config.CalculateCoinbase(tt.number))
			before := statedb.GetBalance(coinbase).Uint64()
			statedb.AddBalance(coinbase, uint256.NewInt(tt.credited), tracing.BalanceIncreaseRewardTransactionFee)

			err := b.payProducerFeeShare(statedb, header, &types.Body{Transactions: txs}, receipts, tt.resolve)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantProducer, statedb.GetBalance(producer).Uint64())
			require.Equal(t, before+tt.credited-tt.wantProducer, statedb.GetBalance(coinbase).Uint64())

			if tt.number >= fork {
				require.Equal(t, distributorContractAddr, coinbase)
			}
		})
	}
}

// With no priority fees there is nothing to pay, so the producer is never
// resolved. Import relies on this for headers that carry no seal in tests.
func TestPayProducerFeeShare_NoFeesDoesNotResolveProducer(t *testing.T) {
	t.Parallel()

	_, b, genesis, statedb := newStakingChain(t, 16, 0, true, &stakingHeimdall{})
	header := uburuHeader(genesis, b, 5)

	err := b.payProducerFeeShare(statedb, header, &types.Body{}, nil, func() (common.Address, error) {
		t.Fatal("producer resolved with no fees to share")
		return common.Address{}, nil
	})
	require.NoError(t, err)
}

func TestBlockPriorityFees_Edges(t *testing.T) {
	t.Parallel()

	baseFee := big.NewInt(100)
	huge := new(big.Int).Lsh(big.NewInt(1), 255)
	hugeTip := types.NewTx(&types.DynamicFeeTx{GasTipCap: huge, GasFeeCap: new(big.Int).Add(huge, baseFee), Gas: 100_000})

	t.Run("more receipts than transactions", func(t *testing.T) {
		t.Parallel()

		got, err := blockPriorityFees(baseFee, types.Transactions{tippedTx(30, 1_000)}, []*types.Receipt{gasReceipt(10), gasReceipt(10)})
		require.NoError(t, err)
		require.Equal(t, uint256.NewInt(300), got)
	})

	t.Run("fee cap below the base fee", func(t *testing.T) {
		t.Parallel()

		got, err := blockPriorityFees(baseFee, types.Transactions{tippedTx(30, 1_000), tippedTx(30, 50)}, []*types.Receipt{gasReceipt(10), gasReceipt(10)})
		require.ErrorIs(t, err, types.ErrGasFeeCapTooLow)
		require.ErrorContains(t, err, "transaction 1")
		require.Nil(t, got)
	})

	t.Run("one fee overflows", func(t *testing.T) {
		t.Parallel()

		got, err := blockPriorityFees(baseFee, types.Transactions{hugeTip}, []*types.Receipt{gasReceipt(4)})
		require.ErrorIs(t, err, types.ErrUint256Overflow)
		require.ErrorContains(t, err, "transaction 0")
		require.Nil(t, got)
	})

	t.Run("the sum overflows", func(t *testing.T) {
		t.Parallel()

		got, err := blockPriorityFees(baseFee, types.Transactions{hugeTip, hugeTip}, []*types.Receipt{gasReceipt(1), gasReceipt(1)})
		require.ErrorIs(t, err, types.ErrUint256Overflow)
		require.Nil(t, got)
	})

	t.Run("largest fee that fits", func(t *testing.T) {
		t.Parallel()

		got, err := blockPriorityFees(baseFee, types.Transactions{hugeTip}, []*types.Receipt{gasReceipt(1)})
		require.NoError(t, err)
		require.Equal(t, huge, got.ToBig())
	})
}

func TestPayProducerFeeShare_FeeErrorFailsBlock(t *testing.T) {
	t.Parallel()

	_, b, genesis, statedb := newStakingChain(t, 16, 0, true, &stakingHeimdall{})

	header := uburuHeader(genesis, b, 5)
	header.BaseFee = big.NewInt(100)

	body := &types.Body{Transactions: types.Transactions{tippedTx(30, 50)}}
	err := b.payProducerFeeShare(statedb, header, body, []*types.Receipt{gasReceipt(10)}, func() (common.Address, error) {
		return common.HexToAddress("0xbeef"), nil
	})
	require.ErrorIs(t, err, types.ErrGasFeeCapTooLow)
	require.ErrorContains(t, err, "summing priority fees")
}

// The producer being the coinbase is harmless: the share leaves and returns.
func TestPayProducerFeeShare_ProducerIsCoinbase(t *testing.T) {
	t.Parallel()

	_, b, genesis, statedb := newStakingChain(t, 16, 0, true, &stakingHeimdall{})

	header := uburuHeader(genesis, b, 5)
	header.BaseFee = big.NewInt(100)

	statedb.AddBalance(distributorContractAddr, uint256.NewInt(630_000), tracing.BalanceIncreaseRewardTransactionFee)

	body := &types.Body{Transactions: types.Transactions{tippedTx(30, 1_000)}}
	err := b.payProducerFeeShare(statedb, header, body, []*types.Receipt{gasReceipt(21_000)}, func() (common.Address, error) {
		return distributorContractAddr, nil
	})
	require.NoError(t, err)
	require.Equal(t, uint64(630_000), statedb.GetBalance(distributorContractAddr).Uint64())
}

func TestLocalProducer(t *testing.T) {
	t.Parallel()

	_, b, _, _ := newStakingChain(t, 16, 0, true, &stakingHeimdall{})

	signerAddr := common.HexToAddress("0xfeed")
	b.Authorize(signerAddr, nil)

	got, err := b.localProducer()
	require.NoError(t, err)
	require.Equal(t, signerAddr, got)
}
