package keeper

import (
	"context"
	"fmt"

	"github.com/0xPolygon/heimdall-v2/x/milestone/types"
)

// InitGenesis sets the milestone module's state from a given genesis state.
func (k Keeper) InitGenesis(ctx context.Context, data *types.GenesisState) {
	if data == nil {
		panic("genesis state cannot be nil")
	}

	err := data.Params.ValidateBasic()
	if err != nil {
		panic(fmt.Sprintf("error in validating milestone params: err = %v", err))
	}

	err = k.params.Set(ctx, data.Params)
	if err != nil {
		panic(fmt.Sprintf("error in setting the milestone params: err = %v", err))
	}

	if len(data.Milestones) > 0 {
		sortedMilestones := types.SortMilestones(data.Milestones)
		for _, milestone := range sortedMilestones {
			if err := k.AddMilestone(ctx, milestone); err != nil {
				k.Logger(ctx).Error("error while adding the milestone to store",
					"milestone", milestone.String(),
					"error", err)
			}
		}
	}
}

// ExportGenesis returns milestone module's genesis state
func (k Keeper) ExportGenesis(ctx context.Context) *types.GenesisState {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.Logger(ctx).Error("error while getting milestone params")
	}

	milestones, err := k.GetMilestones(ctx)
	if err != nil {
		k.Logger(ctx).Error("error while getting milestones")
	}

	return &types.GenesisState{
		Params:     params,
		Milestones: milestones,
	}
}
