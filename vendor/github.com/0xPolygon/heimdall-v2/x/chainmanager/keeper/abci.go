package keeper

import (
	"context"
	"fmt"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// EndBlocker called at the end of every block
func (k *Keeper) EndBlocker(ctx context.Context) ([]abci.ValidatorUpdate, error) {
	defer telemetry.ModuleMeasureSince(types.ModuleName, time.Now(), telemetry.MetricKeyEndBlocker)

	// Change root chain contract addresses if required
	if chainManagerAddressMigration, found := helper.GetChainManagerAddressMigration(sdk.UnwrapSDKContext(ctx).BlockHeight()); found {
		params, err := k.GetParams(ctx)
		if err != nil {
			_ = fmt.Errorf("error fetching chain manager params. Error: %w", err)
			return nil, err
		}

		params.ChainParams.PolTokenAddress = chainManagerAddressMigration.PolTokenAddress
		params.ChainParams.StakingManagerAddress = chainManagerAddressMigration.StakingManagerAddress
		params.ChainParams.RootChainAddress = chainManagerAddressMigration.RootChainAddress
		params.ChainParams.SlashManagerAddress = chainManagerAddressMigration.SlashManagerAddress
		params.ChainParams.StakingInfoAddress = chainManagerAddressMigration.StakingInfoAddress
		params.ChainParams.StateSenderAddress = chainManagerAddressMigration.StateSenderAddress

		// update chain manager state
		err = k.SetParams(ctx, params)
		if err != nil {
			_ = fmt.Errorf("error updating chain manager params. Error: %w", err)
			return nil, err
		}
	}
	return nil, nil
}
