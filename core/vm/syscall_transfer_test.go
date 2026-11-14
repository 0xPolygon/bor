package vm

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

func TestSystemCallTransferAcrossAmsterdam(t *testing.T) {
	const amsterdamBlock = 100

	cfg := *params.MergedTestChainConfig
	cfg.Bor = nil
	cfg.AmsterdamBlock = big.NewInt(amsterdamBlock)

	target := common.BytesToAddress([]byte("target"))

	tests := []struct {
		name          string
		block         int64
		value         uint64
		wantErr       error
		wantTransfers int
	}{
		{"zero value before Amsterdam", amsterdamBlock - 1, 0, nil, 1},
		{"zero value at Amsterdam", amsterdamBlock, 0, nil, 0},
		{"zero value after Amsterdam", amsterdamBlock + 1, 0, nil, 0},
		{"unfunded value before Amsterdam", amsterdamBlock - 1, 1, ErrInsufficientBalance, 0},
		{"unfunded value at Amsterdam", amsterdamBlock, 1, nil, 0},
		{"unfunded value after Amsterdam", amsterdamBlock + 1, 1, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
			statedb.CreateAccount(target)
			statedb.SetCode(target, []byte{byte(STOP)}, tracing.CodeChangeUnspecified)
			statedb.Finalise(params.Rules{IsEIP158: true})

			transfers := 0
			ctx := BlockContext{
				CanTransfer: func(db StateDB, addr common.Address, amount *uint256.Int) bool {
					return db.GetBalance(addr).Cmp(amount) >= 0
				},
				Transfer: func(db StateDB, sender, recipient common.Address, amount *uint256.Int, _ *params.Rules) {
					transfers++
					db.SubBalance(sender, amount, tracing.BalanceChangeTransfer)
					db.AddBalance(recipient, amount, tracing.BalanceChangeTransfer)
				},
				BlockNumber: big.NewInt(tt.block),
				Random:      &common.Hash{},
			}
			evm := NewEVM(ctx, statedb, &cfg, Config{})
			require.Equal(t, tt.block >= amsterdamBlock, evm.chainRules.IsAmsterdam)

			_, _, err := evm.Call(params.SystemAddress, target, nil, NewGasBudget(100_000, 0), uint256.NewInt(tt.value))
			require.ErrorIs(t, err, tt.wantErr)
			require.Equal(t, tt.wantTransfers, transfers)
			require.True(t, statedb.GetBalance(params.SystemAddress).IsZero())
			require.True(t, statedb.GetBalance(target).IsZero())
		})
	}
}
