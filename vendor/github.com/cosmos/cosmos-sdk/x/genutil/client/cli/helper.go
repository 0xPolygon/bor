package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	errorsmod "cosmossdk.io/errors"
	milestonestypes "github.com/0xPolygon/heimdall-v2/x/milestone/types"
	staketypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
)

func SetGenesisValidator(valPubKey cryptotypes.PubKey) (json.RawMessage, error) {
	if valPubKey == nil {
		return nil, fmt.Errorf("invalid public key: nil")
	}

	genesisState := staketypes.GenesisState{
		CurrentValidatorSet: staketypes.ValidatorSet{
			Validators: []*staketypes.Validator{
				{
					EndEpoch:         0,
					ValId:            1,
					StartEpoch:       0,
					Nonce:            0,
					VotingPower:      1000,
					PubKey:           valPubKey.Bytes(),
					Signer:           strings.ToUpper(valPubKey.Address().String()),
					LastUpdated:      "",
					Jailed:           false,
					ProposerPriority: 0,
				},
			},
			Proposer: &staketypes.Validator{
				ValId:            1,
				StartEpoch:       0,
				EndEpoch:         0,
				Nonce:            0,
				VotingPower:      1000,
				PubKey:           valPubKey.Bytes(),
				Signer:           strings.ToUpper(valPubKey.Address().String()),
				LastUpdated:      "",
				Jailed:           false,
				ProposerPriority: 0,
			},
		},
		Validators: []*staketypes.Validator{
			{
				ValId:            1,
				StartEpoch:       0,
				EndEpoch:         1000000,
				Nonce:            0,
				VotingPower:      1000,
				PubKey:           valPubKey.Bytes(),
				Signer:           valPubKey.Address().String(),
				LastUpdated:      "",
				Jailed:           false,
				ProposerPriority: 0,
			},
		},
	}

	data, err := json.Marshal(genesisState)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// BuildEmptyMilestoneGenesis constructs a milestone genesis state with empty milestones
// and default params fetched from the milestone module.
func BuildEmptyMilestoneGenesis() (json.RawMessage, error) {
	// get genesis state's default params as JSON
	milestoneGenesisRaw, err := GetMilestonesDefaultParams()
	if err != nil {
		return nil, fmt.Errorf("failed to get milestone default params: %w", err)
	}

	// unmarshal to map
	var milestoneState map[string]interface{}
	if err := json.Unmarshal(milestoneGenesisRaw, &milestoneState); err != nil {
		return nil, fmt.Errorf("failed to unmarshal milestone genesis state: %w", err)
	}

	// override milestones with an empty list
	milestoneState["milestones"] = []interface{}{}

	// marshal back to RawMessage
	marshalledMilestonesState, err := json.Marshal(milestoneState)
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to marshal milestone genesis state")
	}

	return marshalledMilestonesState, nil
}

func GetMilestonesDefaultParams() (json.RawMessage, error) {
	genesisState := milestonestypes.GenesisState{
		Params:     milestonestypes.DefaultParams(),
		Milestones: []milestonestypes.Milestone{},
	}
	data, err := json.Marshal(genesisState)
	if err != nil {
		return nil, err
	}

	return data, nil
}
