package types

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	chainmanagertypes "github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
)

// BankKeeper defines the bank keeper contract used by x/topup module
type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	IsSendEnabledDenom(ctx context.Context, denom string) bool
	SpendableCoin(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins) error
	BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins) error
	SendCoins(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) error
	MintCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
}

// ChainKeeper defines the chain keeper contract used by x/topup module
type ChainKeeper interface {
	GetParams(ctx context.Context) (chainmanagertypes.Params, error)
}
