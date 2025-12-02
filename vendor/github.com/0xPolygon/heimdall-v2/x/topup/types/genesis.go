package types

import (
	"encoding/json"
	"errors"

	"github.com/cosmos/cosmos-sdk/codec"

	"github.com/0xPolygon/heimdall-v2/types"
)

// NewGenesisState creates a new genesis state for x/topup module
func NewGenesisState(sequences []string, accounts []types.DividendAccount) *GenesisState {
	return &GenesisState{
		TopupSequences:   sequences,
		DividendAccounts: accounts,
	}
}

// DefaultGenesisState returns a default genesis state for x/topup module
func DefaultGenesisState() *GenesisState {
	return NewGenesisState(nil, nil)
}

// Validate performs basic validation of x/topup genesis data
func (gs GenesisState) Validate() error {
	for _, sequence := range gs.TopupSequences {
		if sequence == "" {
			return errors.New("invalid sequence detected while validating x/topup genesis state")
		}
	}
	for _, account := range gs.DividendAccounts {
		if account.User == "" {
			return errors.New("invalid dividend account detected while validating x/topup genesis state")
		}
	}

	return nil
}

// GetGenesisStateFromAppState returns the x/topup module GenesisState given a raw application genesis state
func GetGenesisStateFromAppState(cdc codec.JSONCodec, appState map[string]json.RawMessage) (*GenesisState, error) {
	var genesisState GenesisState
	if appState[ModuleName] != nil {
		cdc.MustUnmarshalJSON(appState[ModuleName], &genesisState)
	}
	err := genesisState.Validate()
	if err != nil {
		return nil, err
	}
	return &genesisState, nil
}

// SetGenesisStateToAppState sets x/topup module GenesisState into the raw application genesis state.
func SetGenesisStateToAppState(cdc codec.JSONCodec, appState map[string]json.RawMessage, dividendAccounts []types.DividendAccount) (map[string]json.RawMessage, error) {
	topupState, err := GetGenesisStateFromAppState(cdc, appState)
	if err != nil {
		return nil, err
	}
	topupState.DividendAccounts = dividendAccounts

	appState[ModuleName] = cdc.MustMarshalJSON(topupState)

	return appState, nil
}
