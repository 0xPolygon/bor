package altair

import (
	"context"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

func ComputeFlagDeltas(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, attesterData *EpochAttesterData,
	flag ParticipationFlags, weight common.Gwei, isInactivityLeak bool) (*common.Deltas, error) {

	valCount := uint64(len(attesterData.Flats))
	out := common.NewDeltas(valCount)

	unslashedParticipatingTotalBalance := common.Gwei(0)
	for _, vi := range epc.PreviousEpoch.ActiveIndices {
		if !attesterData.Flats[vi].Slashed && (attesterData.PrevParticipation[vi]&flag != 0) {
			unslashedParticipatingTotalBalance += attesterData.Flats[vi].EffectiveBalance
		}
	}
	// get_total_balance makes it 1 increment minimum
	if unslashedParticipatingTotalBalance < spec.EFFECTIVE_BALANCE_INCREMENT {
		unslashedParticipatingTotalBalance = spec.EFFECTIVE_BALANCE_INCREMENT
	}
	unslashedParticipatingIncrements := unslashedParticipatingTotalBalance / spec.EFFECTIVE_BALANCE_INCREMENT

	activeIncrements := epc.TotalActiveStake / spec.EFFECTIVE_BALANCE_INCREMENT

	baseRewardPerIncrement := (spec.EFFECTIVE_BALANCE_INCREMENT * common.Gwei(spec.BASE_REWARD_FACTOR)) / epc.TotalActiveStakeSqRoot
	for _, vi := range attesterData.EligibleIndices {
		effBal := attesterData.Flats[vi].EffectiveBalance
		increments := effBal / spec.EFFECTIVE_BALANCE_INCREMENT
		baseReward := increments * baseRewardPerIncrement
		prevEpochParticipation := attesterData.PrevParticipation[vi]
		flagParticipation := prevEpochParticipation&flag != 0

		slashed := attesterData.Flats[vi].Slashed
		if !slashed && flagParticipation {
			if !isInactivityLeak {
				rewardNumerator := (baseReward * weight) * unslashedParticipatingIncrements
				rewardDenominator := activeIncrements * WEIGHT_DENOMINATOR
				out.Rewards[vi] += rewardNumerator / rewardDenominator
			}
		} else if flag != TIMELY_HEAD_FLAG {
			out.Penalties[vi] += (baseReward * weight) / WEIGHT_DENOMINATOR
		}
	}
	return out, nil
}

func ComputeInactivityPenaltyDeltas(ctx context.Context, spec *common.Spec, epc *common.EpochsContext,
	attesterData *EpochAttesterData, inactivityScores *InactivityScoresView, inactivityPenaltyQuotient uint64) (*common.Deltas, error) {
	out := common.NewDeltas(uint64(len(attesterData.Flats)))
	penaltyDenominator := common.Gwei(uint64(spec.INACTIVITY_SCORE_BIAS) * inactivityPenaltyQuotient)
	for _, vi := range attesterData.EligibleIndices {
		if !(!attesterData.Flats[vi].Slashed && (attesterData.PrevParticipation[vi]&TIMELY_TARGET_FLAG != 0)) {
			score, err := inactivityScores.GetScore(vi)
			if err != nil {
				return nil, err
			}
			effBal := attesterData.Flats[vi].EffectiveBalance
			penaltyNumerator := effBal * common.Gwei(score)
			out.Penalties[vi] += penaltyNumerator / penaltyDenominator
		}
	}
	return out, nil
}

type RewardsAndPenalties struct {
	Source     *common.Deltas
	Target     *common.Deltas
	Head       *common.Deltas
	Inactivity *common.Deltas
}

func NewRewardsAndPenalties(validatorCount uint64) *RewardsAndPenalties {
	return &RewardsAndPenalties{
		Source:     common.NewDeltas(validatorCount),
		Target:     common.NewDeltas(validatorCount),
		Head:       common.NewDeltas(validatorCount),
		Inactivity: common.NewDeltas(validatorCount),
	}
}

func AttestationRewardsAndPenalties(ctx context.Context, spec *common.Spec, epc *common.EpochsContext,
	attesterData *EpochAttesterData, state AltairLikeBeaconState) (*RewardsAndPenalties, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	finalized, err := state.FinalizedCheckpoint()
	if err != nil {
		return nil, err
	}
	finalityDelay := attesterData.PrevEpoch - finalized.Epoch
	isInactivityLeak := finalityDelay > spec.MIN_EPOCHS_TO_INACTIVITY_PENALTY

	sourceDeltas, err := ComputeFlagDeltas(ctx, spec, epc, attesterData,
		TIMELY_SOURCE_FLAG, TIMELY_SOURCE_WEIGHT, isInactivityLeak)
	if err != nil {
		return nil, err
	}
	targetDeltas, err := ComputeFlagDeltas(ctx, spec, epc, attesterData,
		TIMELY_TARGET_FLAG, TIMELY_TARGET_WEIGHT, isInactivityLeak)
	if err != nil {
		return nil, err
	}
	headDeltas, err := ComputeFlagDeltas(ctx, spec, epc, attesterData,
		TIMELY_HEAD_FLAG, TIMELY_HEAD_WEIGHT, isInactivityLeak)
	if err != nil {
		return nil, err
	}
	inactivityScores, err := state.InactivityScores()
	if err != nil {
		return nil, err
	}
	settings := state.ForkSettings(spec)
	inactivityPenalties, err := ComputeInactivityPenaltyDeltas(
		ctx, spec, epc, attesterData, inactivityScores, settings.InactivityPenaltyQuotient)
	if err != nil {
		return nil, err
	}
	return &RewardsAndPenalties{
		Source:     sourceDeltas,
		Target:     targetDeltas,
		Head:       headDeltas,
		Inactivity: inactivityPenalties,
	}, nil
}

func ProcessEpochRewardsAndPenalties(ctx context.Context, spec *common.Spec, epc *common.EpochsContext,
	attesterData *EpochAttesterData, state AltairLikeBeaconState) error {
	currentEpoch := epc.CurrentEpoch.Epoch
	if currentEpoch == common.GENESIS_EPOCH {
		return nil
	}

	rewAndPenalties, err := AttestationRewardsAndPenalties(ctx, spec, epc, attesterData, state)
	if err != nil {
		return err
	}

	valCount := uint64(len(attesterData.Flats))
	sum := common.NewDeltas(valCount)
	sum.Add(rewAndPenalties.Source)
	sum.Add(rewAndPenalties.Target)
	sum.Add(rewAndPenalties.Head)
	sum.Add(rewAndPenalties.Inactivity)
	balances, err := common.ApplyDeltas(state, sum)
	if err != nil {
		return err
	}
	return state.SetBalances(balances)
}
