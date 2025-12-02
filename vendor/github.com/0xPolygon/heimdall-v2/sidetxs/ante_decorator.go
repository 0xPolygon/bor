package sidetxs

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
)

type SideTxDecorator struct {
	sideTxCfg SideTxConfigurator
}

func NewSideTxDecorator(sideTxCfg SideTxConfigurator) SideTxDecorator {
	return SideTxDecorator{
		sideTxCfg: sideTxCfg,
	}
}

func (std SideTxDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	_, ok := tx.(authsigning.Tx)
	if !ok {
		return ctx, errorsmod.Wrap(sdkerrors.ErrTxDecode, "invalid transaction type")
	}

	if CountSideHandlers(std.sideTxCfg, tx) > 1 {
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "multiple messages in a single side transaction")
	}

	return next(ctx, tx, simulate)
}

// CountSideHandlers returns the number of side handlers for the transaction to make sure we only propose and process one side msg per tx.
// This enforces only one message per sideTx. Otherwise, a single comet tx would contain more than one side msg, allowing for more than one vote for the same tx hash.
func CountSideHandlers(sideTxCfg SideTxConfigurator, tx sdk.Tx) int {
	sideHandlerCount := 0
	for _, msg := range tx.GetMsgs() {
		if sideHandler := sideTxCfg.GetSideHandler(msg); sideHandler != nil {
			sideHandlerCount++
		}
	}
	return sideHandlerCount
}
