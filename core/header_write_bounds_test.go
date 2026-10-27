package core

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestInsertHeaderChainWithoutValidationBounds(t *testing.T) {
	t.Parallel()

	two64 := new(big.Int).Lsh(big.NewInt(1), 64)
	two256 := new(big.Int).Lsh(big.NewInt(1), 256)

	tests := []struct {
		name    string
		mutate  func(h *types.Header)
		wantErr bool
	}{
		{"in bounds", func(*types.Header) {}, false},
		{"number over 64 bits", func(h *types.Header) { h.Number = new(big.Int).Add(two64, h.Number) }, true},
		{"base fee over 256 bits", func(h *types.Header) { h.BaseFee = two256 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, bc, err := newCanonical(ethash.NewFaker(), 0, true, rawdb.HashScheme)
			require.NoError(t, err)
			defer bc.Stop()

			parent := bc.CurrentHeader()
			header := &types.Header{
				ParentHash: parent.Hash(),
				Number:     new(big.Int).Add(parent.Number, big.NewInt(1)),
				Difficulty: big.NewInt(1),
				GasLimit:   parent.GasLimit,
				BaseFee:    parent.BaseFee,
				Time:       parent.Time + 1,
			}
			tt.mutate(header)

			_, err = bc.InsertHeaderChainWithoutValidation([]*types.Header{header})
			_, stored := rawdb.ReadHeaderNumber(bc.db, header.Hash())
			if tt.wantErr {
				require.Error(t, err)
				require.False(t, stored)
			} else {
				require.NoError(t, err)
				require.True(t, stored)
			}
		})
	}
}
