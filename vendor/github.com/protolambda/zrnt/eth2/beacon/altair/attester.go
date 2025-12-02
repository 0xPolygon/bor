package altair

import (
	"context"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

type EpochStakeSummary struct {
	SourceStake common.Gwei
	TargetStake common.Gwei
	HeadStake   common.Gwei
}

type EpochAttesterData struct {
	PrevEpoch common.Epoch
	CurrEpoch common.Epoch

	Flats           []common.FlatValidator
	EligibleIndices []common.ValidatorIndex

	PrevParticipation ParticipationRegistry
	CurrParticipation ParticipationRegistry

	PrevEpochUnslashedStake       EpochStakeSummary
	CurrEpochUnslashedTargetStake common.Gwei
}

func ComputeEpochAttesterData(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, flats []common.FlatValidator, state AltairLikeBeaconState) (out *EpochAttesterData, err error) {
	prevEpoch := epc.PreviousEpoch.Epoch
	currentEpoch := epc.CurrentEpoch.Epoch
	out = &EpochAttesterData{
		PrevEpoch:       prevEpoch,
		CurrEpoch:       currentEpoch,
		Flats:           flats,
		EligibleIndices: make([]common.ValidatorIndex, 0, len(flats)),
		PrevEpochUnslashedStake: EpochStakeSummary{
			SourceStake: 0,
			TargetStake: 0,
			HeadStake:   0,
		},
		CurrEpochUnslashedTargetStake: 0,
	}
	for i := common.ValidatorIndex(0); i < common.ValidatorIndex(len(flats)); i++ {
		flat := &flats[i]
		// eligibility check
		if flat.IsActive(prevEpoch) || (flat.Slashed && prevEpoch+1 < flat.WithdrawableEpoch) {
			out.EligibleIndices = append(out.EligibleIndices, i)
		}
	}
	prevEpochParticipationView, err := state.PreviousEpochParticipation()
	if err != nil {
		return nil, err
	}
	prevEpochParticipation, err := prevEpochParticipationView.Raw()
	if err != nil {
		return nil, err
	}
	out.PrevParticipation = prevEpochParticipation
	currEpochParticipationView, err := state.CurrentEpochParticipation()
	if err != nil {
		return nil, err
	}
	currEpochParticipation, err := currEpochParticipationView.Raw()
	if err != nil {
		return nil, err
	}
	out.CurrParticipation = currEpochParticipation
	for _, vi := range epc.PreviousEpoch.ActiveIndices {
		if flats[vi].Slashed {
			continue
		}
		effBal := flats[vi].EffectiveBalance
		prevFlag := prevEpochParticipation[vi]
		if prevFlag&TIMELY_SOURCE_FLAG != 0 {
			out.PrevEpochUnslashedStake.SourceStake += effBal
		}
		if prevFlag&TIMELY_TARGET_FLAG != 0 {
			out.PrevEpochUnslashedStake.TargetStake += effBal
		}
		if prevFlag&TIMELY_HEAD_FLAG != 0 {
			out.PrevEpochUnslashedStake.HeadStake += effBal
		}
		if currEpochParticipation[vi]&TIMELY_TARGET_FLAG != 0 {
			out.CurrEpochUnslashedTargetStake += effBal
		}
	}
	if out.PrevEpochUnslashedStake.SourceStake < spec.EFFECTIVE_BALANCE_INCREMENT {
		out.PrevEpochUnslashedStake.SourceStake = spec.EFFECTIVE_BALANCE_INCREMENT
	}
	if out.PrevEpochUnslashedStake.TargetStake < spec.EFFECTIVE_BALANCE_INCREMENT {
		out.PrevEpochUnslashedStake.TargetStake = spec.EFFECTIVE_BALANCE_INCREMENT
	}
	if out.PrevEpochUnslashedStake.HeadStake < spec.EFFECTIVE_BALANCE_INCREMENT {
		out.PrevEpochUnslashedStake.HeadStake = spec.EFFECTIVE_BALANCE_INCREMENT
	}
	if out.CurrEpochUnslashedTargetStake < spec.EFFECTIVE_BALANCE_INCREMENT {
		out.CurrEpochUnslashedTargetStake = spec.EFFECTIVE_BALANCE_INCREMENT
	}

	return out, nil
}
