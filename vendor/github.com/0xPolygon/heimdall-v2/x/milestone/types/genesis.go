package types

// NewGenesisState creates a new genesis state.
func NewGenesisState(params Params) GenesisState {
	return GenesisState{Params: params}
}

// DefaultGenesisState gets the raw genesis message for testing
func DefaultGenesisState() *GenesisState {
	params := DefaultParams()
	return &GenesisState{Params: params}
}

// Validate validates the provided checkpoint data
func (gs GenesisState) Validate() error {
	if err := gs.Params.ValidateBasic(); err != nil {
		return err
	}
	return nil
}

// DefaultParams returns a default set of parameters.
func DefaultParams() Params {
	return Params{
		MaxMilestonePropositionLength: 10,
		FfMilestoneThreshold:          1000,
		FfMilestoneBlockInterval:      100,
	}
}
