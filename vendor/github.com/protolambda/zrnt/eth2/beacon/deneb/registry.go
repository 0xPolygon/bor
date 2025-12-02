package deneb

import (
	"context"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
)

func ProcessEpochRegistryUpdates(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, flats []common.FlatValidator, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	vals, err := state.Validators()
	if err != nil {
		return err
	}

	registerData, err := phase0.ComputeRegistryProcessData(spec, flats, epc.CurrentEpoch.Epoch)
	if err != nil {
		return fmt.Errorf("invalid ProcessEpochRegistryUpdates: %v", err)
	}

	// process ejections
	{
		exitEnd := registerData.ExitQueueEnd
		endChurn := registerData.ExitQueueEndChurn
		for _, index := range registerData.IndicesToEject {
			val, err := vals.Validator(index)
			if err != nil {
				return err
			}
			if err := val.SetExitEpoch(exitEnd); err != nil {
				return err
			}
			withdrawEpoch := exitEnd + spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY
			if withdrawEpoch < exitEnd { // practically impossible, but here for spec test introduced in consensus-specs#2887
				return fmt.Errorf("exit epoch overflow: %d + %d = %d", exitEnd, spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY, withdrawEpoch)
			}
			if err := val.SetWithdrawableEpoch(withdrawEpoch); err != nil {
				return err
			}
			endChurn += 1
			if endChurn >= registerData.ChurnLimit {
				endChurn = 0
				exitEnd += 1
			}
		}
	}

	// Process activation eligibility
	{
		eligibilityEpoch := epc.CurrentEpoch.Epoch + 1
		for _, index := range registerData.IndicesToSetActivationEligibility {
			val, err := vals.Validator(index)
			if err != nil {
				return err
			}
			if err := val.SetActivationEligibilityEpoch(eligibilityEpoch); err != nil {
				return err
			}
		}
	}

	// Process activations
	{
		finality, err := state.FinalizedCheckpoint()
		if err != nil {
			return err
		}
		dequeued := registerData.IndicesToMaybeActivate
		// Modified in Deneb: use "get_validator_activation_churn_limit"
		// instead of "get_validator_churn_limit"
		churnLimit := getValidatorActivationChurnLimit(spec, registerData.ChurnLimit)
		if uint64(len(dequeued)) > churnLimit {
			dequeued = dequeued[:churnLimit]
		}
		activationEpoch := spec.ComputeActivationExitEpoch(epc.CurrentEpoch.Epoch)
		for _, index := range dequeued {
			if flats[index].ActivationEligibilityEpoch > finality.Epoch {
				// remaining validators all have an activation_eligibility_epoch that is higher anyway, break early
				// The tie-breaks were already sorted correctly in the IndicesToMaybeActivate queue.
				break
			}
			val, err := vals.Validator(index)
			if err != nil {
				return err
			}
			if err := val.SetActivationEpoch(activationEpoch); err != nil {
				return err
			}
		}
	}
	return nil
}

func getValidatorActivationChurnLimit(spec *common.Spec, phase0ChurnLimit uint64) uint64 {
	return min(uint64(spec.MAX_PER_EPOCH_ACTIVATION_CHURN_LIMIT), phase0ChurnLimit)
}
