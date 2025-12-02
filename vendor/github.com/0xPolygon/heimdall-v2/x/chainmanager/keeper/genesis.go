package keeper

import (
	"context"
	"fmt"

	"github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
)

// InitGenesis sets chainmanager information for genesis.
func (k Keeper) InitGenesis(ctx context.Context, data *types.GenesisState) {
	if err := k.SetParams(ctx, data.Params); err != nil {
		panic(fmt.Errorf("failed to set chainmanager params: %w", err))
	}
}

// ExportGenesis returns a GenesisState for chainmanager.
func (k Keeper) ExportGenesis(ctx context.Context) *types.GenesisState {
	params, err := k.GetParams(ctx)
	if err != nil {
		panic(fmt.Errorf("failed to get chainmanager params: %w", err))
	}

	return types.NewGenesisState(
		params,
	)
}
