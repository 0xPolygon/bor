package types

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// CheckpointKeeper defines the checkpoint keeper contract used by x/stake module
type CheckpointKeeper interface {
	GetAckCount(ctx context.Context) (uint64, error)
}

type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	SendCoins(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) error
}
