package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/0xPolygon/heimdall-v2/x/topup/types"
)

// InitGenesis sets x/topup module information for genesis
func (k Keeper) InitGenesis(ctx sdk.Context, state *types.GenesisState) {
	// add sequences to the genesis state
	for _, sequence := range state.TopupSequences {
		if err := k.SetTopupSequence(ctx, sequence); err != nil {
			panic(fmt.Errorf("failed to set topup sequence during initGenesis: %w", err))
		}
	}
	// add dividend accounts to genesis state
	for _, dividendAccount := range state.DividendAccounts {
		if err := k.SetDividendAccount(ctx, dividendAccount); err != nil {
			panic(fmt.Errorf("failed to set topup dividendAccounts during initGenesis: %w", err))
		}
	}
}

// ExportGenesis returns a GenesisState for the given topup context and keeper.
func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	sequences, err := k.GetAllTopupSequences(ctx)
	if err != nil {
		panic(fmt.Errorf("failed to get topup sequences during ExportGenesis: %w", err))
	}
	dividendAccounts, err := k.GetAllDividendAccounts(ctx)
	if err != nil {
		panic(fmt.Errorf("failed to get dividend accounts during ExportGenesis: %w", err))
	}
	return &types.GenesisState{
		TopupSequences:   sequences,
		DividendAccounts: dividendAccounts,
	}
}
