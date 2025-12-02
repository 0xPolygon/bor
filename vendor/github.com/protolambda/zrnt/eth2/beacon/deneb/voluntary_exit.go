package deneb

import (
	"context"
	"errors"
	"fmt"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/ztyp/tree"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
)

func ValidateVoluntaryExit(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, signedExit *phase0.SignedVoluntaryExit) error {
	exit := &signedExit.Message
	currentEpoch := epc.CurrentEpoch.Epoch
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	if valid, err := vals.IsValidIndex(exit.ValidatorIndex); err != nil {
		return err
	} else if !valid {
		return errors.New("invalid exit validator index")
	}
	validator, err := vals.Validator(exit.ValidatorIndex)
	if err != nil {
		return err
	}
	// Verify that the validator is active
	if isActive, err := phase0.IsActive(validator, currentEpoch); err != nil {
		return err
	} else if !isActive {
		return errors.New("validator must be active to be able to voluntarily exit")
	}
	scheduledExitEpoch, err := validator.ExitEpoch()
	if err != nil {
		return err
	}
	// Verify exit has not been initiated
	if scheduledExitEpoch != common.FAR_FUTURE_EPOCH {
		return errors.New("validator already exited")
	}
	// Exits must specify an epoch when they become valid; they are not valid before then
	if currentEpoch < exit.Epoch {
		return errors.New("invalid exit epoch")
	}
	registeredActivationEpoch, err := validator.ActivationEpoch()
	if err != nil {
		return err
	}
	// Verify the validator has been active long enough
	if currentEpoch < registeredActivationEpoch+spec.SHARD_COMMITTEE_PERIOD {
		return errors.New("exit is too soon")
	}
	pubkey, ok := epc.ValidatorPubkeyCache.Pubkey(exit.ValidatorIndex)
	if !ok {
		return errors.New("could not find index of exiting validator")
	}
	// [Modified in Deneb:EIP7044]
	genesisValRoot, err := state.GenesisValidatorsRoot()
	if err != nil {
		return err
	}
	domain := common.ComputeDomain(common.DOMAIN_VOLUNTARY_EXIT, spec.CAPELLA_FORK_VERSION, genesisValRoot)
	sigRoot := common.ComputeSigningRoot(signedExit.Message.HashTreeRoot(tree.GetHashFn()), domain)
	blsPub, err := pubkey.Pubkey()
	if err != nil {
		return fmt.Errorf("failed to deserialize cached pubkey: %v", err)
	}
	sig, err := signedExit.Signature.Signature()
	if err != nil {
		return fmt.Errorf("failed to deserialize and sub-group check exit signature: %v", err)
	}
	// Verify signature
	if !blsu.Verify(blsPub, sigRoot[:], sig) {
		return errors.New("voluntary exit signature could not be verified")
	}
	return nil
}

func ProcessVoluntaryExit(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, signedExit *phase0.SignedVoluntaryExit) error {
	if err := ValidateVoluntaryExit(spec, epc, state, signedExit); err != nil {
		return err
	}
	return phase0.InitiateValidatorExit(spec, epc, state, signedExit.Message.ValidatorIndex)
}

func ProcessVoluntaryExits(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ops []phase0.SignedVoluntaryExit) error {
	for i := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ProcessVoluntaryExit(spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}
