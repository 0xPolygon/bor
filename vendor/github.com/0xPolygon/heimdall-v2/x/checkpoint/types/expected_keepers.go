package types

import (
	"context"

	hmTypes "github.com/0xPolygon/heimdall-v2/types"
	cmTypes "github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

type TopupKeeper interface {
	GetAllDividendAccounts(ctx context.Context) ([]hmTypes.DividendAccount, error)
}

type StakeKeeper interface {
	GetValidatorSet(ctx context.Context) (validatorSet stakeTypes.ValidatorSet, err error)
	GetCurrentProposer(ctx context.Context) *stakeTypes.Validator
	IncrementAccum(ctx context.Context, times int) error
}

type ChainManagerKeeper interface {
	GetParams(ctx context.Context) (cmTypes.Params, error)
}
