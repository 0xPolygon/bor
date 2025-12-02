package types

import (
	"encoding/json"

	"github.com/cosmos/cosmos-sdk/codec"

	chainmanagertypes "github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
	staketypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// NewGenesisState creates a new genesis state for bor.
func NewGenesisState(params Params, spans []Span) *GenesisState {
	return &GenesisState{
		Params: params,
		Spans:  spans,
	}
}

// DefaultGenesisState returns a default genesis state for bor
func DefaultGenesisState() *GenesisState {
	return NewGenesisState(DefaultParams(), nil)
}

// Validate performs basic validation of bor genesis data, returning an
// error for any failed validation criteria.
func (gs GenesisState) Validate() error {
	if err := gs.Params.ValidateBasic(); err != nil {
		return err
	}

	return nil
}

// GetGenesisStateFromAppState returns x/bor GenesisState given raw application
// genesis state.
func GetGenesisStateFromAppState(cdc codec.JSONCodec, appState map[string]json.RawMessage) *GenesisState {
	var genesisState GenesisState

	if appState[ModuleName] != nil {
		cdc.MustUnmarshalJSON(appState[ModuleName], &genesisState)
		if err := genesisState.Validate(); err != nil {
			panic(err)
		}
	}

	return &genesisState
}

// SetGenesisStateToAppState sets x/bor GenesisState into the raw application genesis state.
func SetGenesisStateToAppState(cdc codec.JSONCodec, appState map[string]json.RawMessage, currentValSet staketypes.ValidatorSet) (map[string]json.RawMessage, error) {
	// set state to bor state
	borState := GetGenesisStateFromAppState(cdc, appState)
	chainState := chainmanagertypes.GetGenesisStateFromAppState(cdc, appState)
	borChainId := chainState.Params.ChainParams.BorChainId
	if borChainId == "" {
		panic("borChainId is empty, please check the order of genesis initialization in your app")
	}
	borState.Spans = genFirstSpan(currentValSet, borChainId)

	appState[ModuleName] = cdc.MustMarshalJSON(borState)

	return appState, nil
}

// genFirstSpan generates the default first span using the validators' producer set
func genFirstSpan(valSet staketypes.ValidatorSet, chainId string) []Span {
	var (
		firstSpan         []Span
		selectedProducers []staketypes.Validator
	)

	if len(valSet.Validators) > int(DefaultProducerCount) {
		// pop top validators and select
		for i := 0; uint64(i) < DefaultProducerCount; i++ {
			selectedProducers = append(selectedProducers, *valSet.Validators[i])
		}
	} else {
		for _, val := range valSet.Validators {
			selectedProducers = append(selectedProducers, *val)
		}
	}

	newSpan := Span{
		Id:                0,
		StartBlock:        0,
		EndBlock:          0 + DefaultFirstSpanDuration - 1,
		ValidatorSet:      valSet,
		SelectedProducers: selectedProducers,
		BorChainId:        chainId,
	}

	firstSpan = append(firstSpan, newSpan)

	return firstSpan
}
