package eth

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestServiceGetBlockBodiesQueryEmptyBodyEncoding(t *testing.T) {
	for _, withdrawals := range []bool{false, true} {
		for _, stored := range []bool{false, true} {
			name := map[bool]string{false: "pre-withdrawals", true: "withdrawals"}[withdrawals] + "/" + map[bool]string{false: "fallback", true: "stored"}[stored]
			t.Run(name, func(t *testing.T) {
				backend := newTestBackendWithGenerator(0, withdrawals, false, nil)
				defer backend.close()
				block := backend.chain.Genesis()
				require.Equal(t, withdrawals, block.Header().WithdrawalsHash != nil)
				canonical := []byte{0xc2, 0xc0, 0xc0}
				if withdrawals {
					canonical = []byte{0xc3, 0xc0, 0xc0, 0xc0}
				}
				if stored {
					rawdb.WriteBodyRLP(backend.db, block.Hash(), 0, canonical)
					require.NotEmpty(t, backend.chain.GetBodyRLP(block.Hash()))
				} else {
					rawdb.DeleteBody(backend.db, block.Hash(), 0)
					require.Empty(t, backend.chain.GetBodyRLP(block.Hash()))
				}
				bodies := ServiceGetBlockBodiesQuery(backend.chain, GetBlockBodiesRequest{block.Hash()})
				require.Len(t, bodies, 1)
				require.Equal(t, canonical, []byte(bodies[0]))
				var body BlockBody
				require.NoError(t, rlp.DecodeBytes(bodies[0], &body))
				require.Equal(t, withdrawals, body.Withdrawals != nil)
				var fields []rlp.RawValue
				require.NoError(t, rlp.DecodeBytes(bodies[0], &fields))
				if withdrawals {
					require.Len(t, fields, 3)
				} else {
					require.Len(t, fields, 2)
				}
			})
		}
	}
}

func TestServiceGetBlockBodiesQueryMissingNonEmptyBody(t *testing.T) {
	backend := newTestBackend(0)
	defer backend.close()
	for _, field := range []string{"transactions", "uncles", "withdrawals"} {
		t.Run(field, func(t *testing.T) {
			header := &types.Header{Number: big.NewInt(1), TxHash: types.EmptyTxsHash, UncleHash: types.EmptyUncleHash}
			switch field {
			case "transactions":
				header.TxHash = common.Hash{1}
			case "uncles":
				header.UncleHash = common.Hash{1}
			case "withdrawals":
				hash := common.Hash{1}
				header.WithdrawalsHash = &hash
			}
			rawdb.WriteHeader(backend.db, header)
			require.Empty(t, ServiceGetBlockBodiesQuery(backend.chain, GetBlockBodiesRequest{header.Hash(), common.Hash{99}}))
		})
	}
}
