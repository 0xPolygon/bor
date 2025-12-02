package app

import (
	"encoding/json"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"

	"github.com/0xPolygon/heimdall-v2/x/stake"
)

// ExportAppStateAndValidators exports the state of the application for a genesis
// file.
func (app *HeimdallApp) ExportAppStateAndValidators(
	_ bool,
	_ []string,
	modulesToExport []string,
) (servertypes.ExportedApp, error) {
	// as if they could withdraw from the start of the next block
	ctx := app.NewContextLegacy(true, cmtproto.Header{Height: app.LastBlockHeight()})

	// We export at last height + 1, because that's the height at which
	// Tendermint will start InitChain.
	height := app.LastBlockHeight() + 1
	genState, err := app.ModuleManager.ExportGenesisForModules(ctx, app.appCodec, modulesToExport)
	if err != nil {
		return servertypes.ExportedApp{}, err
	}
	appState, err := json.MarshalIndent(genState, "", "  ")
	if err != nil {
		return servertypes.ExportedApp{}, err
	}

	validators, err := stake.WriteValidators(ctx, &app.StakeKeeper)
	return servertypes.ExportedApp{
		AppState:        appState,
		Height:          height,
		Validators:      validators,
		ConsensusParams: app.BaseApp.GetConsensusParams(ctx),
	}, err
}
