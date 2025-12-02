package types

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0xPolygon/heimdall-v2/helper"
)

// Default parameter values
const (
	DefaultMainChainTxConfirmations uint64 = 6
	DefaultBorChainTxConfirmations  uint64 = 10

	DefaultStateReceiverAddress = "0x0000000000000000000000000000000000001001"
	DefaultValidatorSetAddress  = "0x0000000000000000000000000000000000001000"
)

// DefaultParams returns a default set of parameters.
func DefaultParams() Params {
	return Params{
		MainChainTxConfirmations: DefaultMainChainTxConfirmations,
		BorChainTxConfirmations:  DefaultBorChainTxConfirmations,
		ChainParams: ChainParams{
			BorChainId:           helper.DefaultBorChainID,
			HeimdallChainId:      helper.DefaultHeimdallChainID,
			StateReceiverAddress: DefaultStateReceiverAddress,
			ValidatorSetAddress:  DefaultValidatorSetAddress,
		},
	}
}

// NewParams creates a new Params object
func NewParams(mainChainTxConfirmations uint64, borChainTxConfirmations uint64, chainParams ChainParams) Params {
	return Params{
		MainChainTxConfirmations: mainChainTxConfirmations,
		BorChainTxConfirmations:  borChainTxConfirmations,
		ChainParams:              chainParams,
	}
}

// ValidateBasic checks that the parameters have valid values.
func (p Params) ValidateBasic() error {
	if err := validateHeimdallAddress("pol_token_address", p.ChainParams.PolTokenAddress); err != nil {
		return err
	}

	if err := validateHeimdallAddress("staking_manager_address", p.ChainParams.StakingManagerAddress); err != nil {
		return err
	}

	if err := validateHeimdallAddress("slash_manager_address", p.ChainParams.SlashManagerAddress); err != nil {
		return err
	}

	if err := validateHeimdallAddress("root_chain_address", p.ChainParams.RootChainAddress); err != nil {
		return err
	}

	if err := validateHeimdallAddress("staking_info_address", p.ChainParams.StakingInfoAddress); err != nil {
		return err
	}

	if err := validateHeimdallAddress("state_sender_address", p.ChainParams.StateSenderAddress); err != nil {
		return err
	}

	if err := validateHeimdallAddress("state_receiver_address", p.ChainParams.StateReceiverAddress); err != nil {
		return err
	}

	if err := validateHeimdallAddress("validator_set_address", p.ChainParams.ValidatorSetAddress); err != nil {
		return err
	}

	return nil
}

func validateHeimdallAddress(key string, value string) error {
	if !common.IsHexAddress(value) {
		return fmt.Errorf("invalid address for value %s for %s in chain_params", value, key)
	}

	return nil
}
