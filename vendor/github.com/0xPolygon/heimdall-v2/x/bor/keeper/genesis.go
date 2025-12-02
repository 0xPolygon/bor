package keeper

import (
	"context"
	"fmt"

	"github.com/0xPolygon/heimdall-v2/x/bor/types"
)

// InitGenesis sets bor information for genesis.
func (k Keeper) InitGenesis(ctx context.Context, data *types.GenesisState) {
	if err := k.SetParams(ctx, data.Params); err != nil {
		panic(fmt.Sprintf("error while setting bor params during InitGenesis: %v", err))
	}

	// sort data spans before inserting to ensure the last span id fetched is correct
	types.SortSpansById(data.Spans)

	// add new span
	for _, span := range data.Spans {
		if err := k.AddNewRawSpan(ctx, &span); err != nil {
			panic(fmt.Sprintf("error while adding span during InitGenesis: %v", err))
		}
	}

	if len(data.Spans) > 0 {
		// update last span
		if err := k.UpdateLastSpan(ctx, data.Spans[len(data.Spans)-1].Id); err != nil {
			panic(fmt.Sprintf("error while updating last span during InitGenesis: %v", err))
		}
	}
}

// ExportGenesis returns a GenesisState for bor.
func (k Keeper) ExportGenesis(ctx context.Context) *types.GenesisState {
	params, err := k.FetchParams(ctx)
	if err != nil {
		panic(err)
	}

	allSpans, err := k.GetAllSpans(ctx)
	if err != nil {
		panic(err)
	}

	spans := make([]types.Span, len(allSpans))
	for i, spanPtr := range allSpans {
		spans[i] = *spanPtr
	}

	types.SortSpansById(spans)

	return types.NewGenesisState(
		params,
		spans,
	)
}
