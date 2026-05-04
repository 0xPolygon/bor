package server

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConvertReceiptToProtoReceipt_Int64Range(t *testing.T) {
	t.Parallel()

	// Largest value that fits in int64 — must round-trip cleanly.
	t.Run("max int64 round-trips", func(t *testing.T) {
		t.Parallel()
		max := new(big.Int).SetInt64(1<<63 - 1)
		r := &types.Receipt{EffectiveGasPrice: max, BlockNumber: max}
		out, err := ConvertReceiptToProtoReceipt(r)
		require.NoError(t, err)
		require.Equal(t, int64(1<<63-1), out.EffectiveGasPrice)
		require.Equal(t, int64(1<<63-1), out.BlockNumber)
	})

	// One past max int64 — must error rather than silently truncate to a
	// negative value.
	t.Run("EffectiveGasPrice over int64 errors", func(t *testing.T) {
		t.Parallel()
		over := new(big.Int).Add(new(big.Int).SetInt64(1<<63-1), big.NewInt(1))
		r := &types.Receipt{EffectiveGasPrice: over, BlockNumber: big.NewInt(1)}
		_, err := ConvertReceiptToProtoReceipt(r)
		require.Error(t, err)
		s, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.OutOfRange, s.Code())
		require.Contains(t, s.Message(), "effective gas price")
	})

	t.Run("BlockNumber over int64 errors", func(t *testing.T) {
		t.Parallel()
		over := new(big.Int).Add(new(big.Int).SetInt64(1<<63-1), big.NewInt(1))
		r := &types.Receipt{EffectiveGasPrice: big.NewInt(1), BlockNumber: over}
		_, err := ConvertReceiptToProtoReceipt(r)
		require.Error(t, err)
		s, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.OutOfRange, s.Code())
		require.Contains(t, s.Message(), "block number")
	})

	// Nil big.Int fields default to 0 — preserve that behavior.
	t.Run("nil big.Int fields default to 0", func(t *testing.T) {
		t.Parallel()
		r := &types.Receipt{}
		out, err := ConvertReceiptToProtoReceipt(r)
		require.NoError(t, err)
		require.Equal(t, int64(0), out.EffectiveGasPrice)
		require.Equal(t, int64(0), out.BlockNumber)
	})
}
