package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/0xPolygon/heimdall-v2/x/clerk/types"
)

// InitGenesis sets clerk information for genesis.
func (k *Keeper) InitGenesis(ctx sdk.Context, data *types.GenesisState) {
	if len(data.EventRecords) != 0 {
		for _, record := range data.EventRecords {
			if err := k.SetEventRecord(ctx, record); err != nil {
				k.Logger(ctx).Error("error in storing event record", "error", err)
			}
		}
	}

	for _, sequence := range data.RecordSequences {
		k.SetRecordSequence(ctx, sequence)
	}
}

// ExportGenesis returns a GenesisState for a given context and keeper.
func (k *Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	return &types.GenesisState{
		EventRecords:    k.GetAllEventRecords(ctx),
		RecordSequences: k.GetRecordSequences(ctx),
	}
}
