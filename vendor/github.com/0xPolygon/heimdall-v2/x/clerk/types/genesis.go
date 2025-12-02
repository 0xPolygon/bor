package types

import "errors"

// DefaultGenesisState returns a default genesis state
func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		EventRecords:    make([]EventRecord, 0),
		RecordSequences: nil,
	}
}

// Validate performs basic validation of the clerk genesis data, returning an
// error for any failed validation criteria.
func Validate(data GenesisState) error {
	for _, sq := range data.RecordSequences {
		if sq == "" {
			return errors.New("invalid sequence")
		}
	}

	return nil
}
