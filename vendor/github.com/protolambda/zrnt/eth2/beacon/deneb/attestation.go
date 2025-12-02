package deneb

import (
	"context"
	"errors"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/altair"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
	"github.com/protolambda/zrnt/eth2/util/math"
)

func ProcessAttestations(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state altair.AltairLikeBeaconState, ops []phase0.Attestation) error {
	for i := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ProcessAttestation(spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

func ProcessAttestation(spec *common.Spec, epc *common.EpochsContext, state altair.AltairLikeBeaconState, attestation *phase0.Attestation) error {
	data := &attestation.Data

	currentSlot, err := state.Slot()
	if err != nil {
		return err
	}

	currentEpoch := spec.SlotToEpoch(currentSlot)
	previousEpoch := currentEpoch.Previous()

	// Check target
	if data.Target.Epoch < previousEpoch {
		return errors.New("attestation data is invalid, target is too far in past")
	} else if data.Target.Epoch > currentEpoch {
		return errors.New("attestation data is invalid, target is in future")
	}
	// And if it matches the slot
	if data.Target.Epoch != spec.SlotToEpoch(data.Slot) {
		return errors.New("attestation data is invalid, slot epoch does not match target epoch")
	}

	// Modified in Deneb: removal of "too old" check (already got epoch range),
	// allow inclusion in target.epoch and target.epoch+1.
	if !(data.Slot+spec.MIN_ATTESTATION_INCLUSION_DELAY <= currentSlot) {
		return errors.New("attestation is too new")
	}

	// Check committee index
	if commCount, err := epc.GetCommitteeCountPerSlot(data.Target.Epoch); err != nil {
		return err
	} else if uint64(data.Index) >= commCount {
		return errors.New("attestation data is invalid, committee index out of range")
	}

	// Note: this checks the source checkpoint.
	applyFlags, err := GetApplicableAttestationParticipationFlags(spec, state, data, currentSlot-data.Slot)
	if err != nil {
		return err
	}

	// Check signature and bitfields
	committee, err := epc.GetBeaconCommittee(data.Slot, data.Index)
	if err != nil {
		return err
	}
	indexedAtt, err := attestation.ConvertToIndexed(spec, committee)
	if err != nil {
		return fmt.Errorf("attestation could not be converted to an indexed attestation: %v", err)
	} else if err := phase0.ValidateIndexedAttestation(spec, epc, state, indexedAtt); err != nil {
		return fmt.Errorf("attestation could not be verified in its indexed form: %v", err)
	}

	var epochParticipation *altair.ParticipationRegistryView
	// Check source
	if data.Target.Epoch == currentEpoch {
		epochParticipation, err = state.CurrentEpochParticipation()
		if err != nil {
			return err
		}
	} else {
		epochParticipation, err = state.PreviousEpochParticipation()
		if err != nil {
			return err
		}
	}

	// TODO: probably better to batch flag changes, needs optimization, tree structure not good for this.
	proposerRewardNumerator := common.Gwei(0)
	baseRewardPerIncrement := spec.EFFECTIVE_BALANCE_INCREMENT * common.Gwei(spec.BASE_REWARD_FACTOR) / epc.TotalActiveStakeSqRoot
	for _, vi := range indexedAtt.AttestingIndices {
		if applyFlags == 0 { // no work to do, just skip ahead
			continue
		}
		increments := epc.EffectiveBalances[vi] / spec.EFFECTIVE_BALANCE_INCREMENT
		baseReward := increments * baseRewardPerIncrement
		existingFlags, err := epochParticipation.GetFlags(vi)
		if err != nil {
			return err
		}
		if (applyFlags&altair.TIMELY_SOURCE_FLAG != 0) && (existingFlags&altair.TIMELY_SOURCE_FLAG == 0) {
			proposerRewardNumerator += baseReward * altair.TIMELY_SOURCE_WEIGHT
		}
		if (applyFlags&altair.TIMELY_TARGET_FLAG != 0) && (existingFlags&altair.TIMELY_TARGET_FLAG == 0) {
			proposerRewardNumerator += baseReward * altair.TIMELY_TARGET_WEIGHT
		}
		if (applyFlags&altair.TIMELY_HEAD_FLAG != 0) && (existingFlags&altair.TIMELY_HEAD_FLAG == 0) {
			proposerRewardNumerator += baseReward * altair.TIMELY_HEAD_WEIGHT
		}
		if err := epochParticipation.SetFlags(vi, existingFlags|applyFlags); err != nil {
			return err
		}
	}
	proposerRewardDenominator := ((altair.WEIGHT_DENOMINATOR - altair.PROPOSER_WEIGHT) * altair.WEIGHT_DENOMINATOR) / altair.PROPOSER_WEIGHT
	proposerReward := proposerRewardNumerator / proposerRewardDenominator
	proposerIndex, err := epc.GetBeaconProposer(currentSlot)
	if err != nil {
		return err
	}
	bals, err := state.Balances()
	if err != nil {
		return err
	}
	if err := common.IncreaseBalance(bals, proposerIndex, proposerReward); err != nil {
		return err
	}
	return nil
}

func GetApplicableAttestationParticipationFlags(
	spec *common.Spec, state common.BeaconState,
	data *phase0.AttestationData, inclusionDelay common.Slot) (out altair.ParticipationFlags, err error) {

	currentSlot, err := state.Slot()
	if err != nil {
		return 0, err
	}

	currentEpoch := spec.SlotToEpoch(currentSlot)

	var justifiedCheckpoint common.Checkpoint
	if data.Target.Epoch == currentEpoch {
		justifiedCheckpoint, err = state.CurrentJustifiedCheckpoint()
		if err != nil {
			return 0, err
		}
	} else {
		justifiedCheckpoint, err = state.PreviousJustifiedCheckpoint()
		if err != nil {
			return 0, err
		}
	}
	expectedHead, err := common.GetBlockRootAtSlot(spec, state, data.Slot)
	if err != nil {
		return 0, err
	}
	expectedTarget, err := common.GetBlockRoot(spec, state, data.Target.Epoch)
	if err != nil {
		return 0, err
	}

	isMatchingSource := data.Source == justifiedCheckpoint
	isMatchingTarget := isMatchingSource && expectedTarget == data.Target.Root
	isMatchingHead := isMatchingTarget && expectedHead == data.BeaconBlockRoot

	if !isMatchingSource {
		return 0, fmt.Errorf("source %s must match justified %s", data.Source, justifiedCheckpoint)
	}

	if isMatchingSource && inclusionDelay <= common.Slot(math.IntegerSquareroot(uint64(spec.SLOTS_PER_EPOCH))) {
		out |= altair.TIMELY_SOURCE_FLAG
	}
	if isMatchingTarget { // Modified in Deneb
		out |= altair.TIMELY_TARGET_FLAG
	}
	if isMatchingHead && inclusionDelay == spec.MIN_ATTESTATION_INCLUSION_DELAY {
		out |= altair.TIMELY_HEAD_FLAG
	}
	return out, nil
}
