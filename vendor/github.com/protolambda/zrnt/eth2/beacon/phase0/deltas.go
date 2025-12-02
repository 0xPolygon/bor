package phase0

import (
	"context"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/util/math"
)

type RewardsAndPenalties struct {
	Source         *common.Deltas
	Target         *common.Deltas
	Head           *common.Deltas
	InclusionDelay *common.Deltas
	Inactivity     *common.Deltas
}

func NewRewardsAndPenalties(validatorCount uint64) *RewardsAndPenalties {
	return &RewardsAndPenalties{
		Source:         common.NewDeltas(validatorCount),
		Target:         common.NewDeltas(validatorCount),
		Head:           common.NewDeltas(validatorCount),
		InclusionDelay: common.NewDeltas(validatorCount),
		Inactivity:     common.NewDeltas(validatorCount),
	}
}

func AttestationRewardsAndPenalties(ctx context.Context, spec *common.Spec,
	epc *common.EpochsContext, attesterData *EpochAttesterData, state common.BeaconState) (*RewardsAndPenalties, error) {

	validatorCount := common.ValidatorIndex(uint64(len(attesterData.Statuses)))
	res := NewRewardsAndPenalties(uint64(validatorCount))

	previousEpoch := epc.PreviousEpoch.Epoch

	attesterStatuses := attesterData.Statuses

	totalBalance := epc.TotalActiveStake

	prevEpochStake := &attesterData.PrevEpochUnslashedStake
	prevEpochSourceStake := prevEpochStake.SourceStake
	prevEpochTargetStake := prevEpochStake.TargetStake
	prevEpochHeadStake := prevEpochStake.HeadStake

	balanceSqRoot := common.Gwei(math.IntegerSquareroot(uint64(totalBalance)))
	finalized, err := state.FinalizedCheckpoint()
	if err != nil {
		return nil, err
	}
	finalityDelay := previousEpoch - finalized.Epoch

	settings := state.ForkSettings(spec)

	// All summed effective balances are normalized to effective-balance increments, to avoid overflows.
	totalBalance /= spec.EFFECTIVE_BALANCE_INCREMENT
	prevEpochSourceStake /= spec.EFFECTIVE_BALANCE_INCREMENT
	prevEpochTargetStake /= spec.EFFECTIVE_BALANCE_INCREMENT
	prevEpochHeadStake /= spec.EFFECTIVE_BALANCE_INCREMENT

	isInactivityLeak := finalityDelay > spec.MIN_EPOCHS_TO_INACTIVITY_PENALTY

	for i := common.ValidatorIndex(0); i < validatorCount; i++ {
		// every 1024 validators, check if the context is done.
		if i&((1<<10)-1) == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		status := &attesterStatuses[i]
		effBalance := attesterData.Flats[i].EffectiveBalance
		baseReward := effBalance * common.Gwei(spec.BASE_REWARD_FACTOR) /
			balanceSqRoot / common.BASE_REWARDS_PER_EPOCH

		// Inclusion delay
		if status.Flags.HasMarkers(PrevSourceAttester | UnslashedAttester) {
			// Inclusion speed bonus
			proposerReward := baseReward / common.Gwei(spec.PROPOSER_REWARD_QUOTIENT)
			res.InclusionDelay.Rewards[status.AttestedProposer] += proposerReward
			maxAttesterReward := baseReward - proposerReward
			res.InclusionDelay.Rewards[i] += maxAttesterReward / common.Gwei(status.InclusionDelay)
		}

		if status.Flags&EligibleAttester != 0 {
			// Since full base reward will be canceled out by inactivity penalty deltas,
			// optimal participation receives full base reward compensation here.

			// Expected FFG source
			if status.Flags.HasMarkers(PrevSourceAttester | UnslashedAttester) {
				if isInactivityLeak {
					res.Source.Rewards[i] += baseReward
				} else {
					// Justification-participation reward
					res.Source.Rewards[i] += baseReward * prevEpochSourceStake / totalBalance
				}
			} else {
				//Justification-non-participation R-penalty
				res.Source.Penalties[i] += baseReward
			}

			// Expected FFG target
			if status.Flags.HasMarkers(PrevTargetAttester | UnslashedAttester) {
				if isInactivityLeak {
					res.Target.Rewards[i] += baseReward
				} else {
					// Boundary-attestation reward
					res.Target.Rewards[i] += baseReward * prevEpochTargetStake / totalBalance
				}
			} else {
				//Boundary-attestation-non-participation R-penalty
				res.Target.Penalties[i] += baseReward
			}

			// Expected head
			if status.Flags.HasMarkers(PrevHeadAttester | UnslashedAttester) {
				if isInactivityLeak {
					res.Head.Rewards[i] += baseReward
				} else {
					// Canonical-participation reward
					res.Head.Rewards[i] += baseReward * prevEpochHeadStake / totalBalance
				}
			} else {
				// Non-canonical-participation R-penalty
				res.Head.Penalties[i] += baseReward
			}

			// Take away max rewards if we're not finalizing
			if isInactivityLeak {
				// If validator is performing optimally this cancels all rewards for a neutral balance
				proposerReward := baseReward / common.Gwei(spec.PROPOSER_REWARD_QUOTIENT)
				res.Inactivity.Penalties[i] += common.BASE_REWARDS_PER_EPOCH*baseReward - proposerReward
				if !status.Flags.HasMarkers(PrevTargetAttester | UnslashedAttester) {
					res.Inactivity.Penalties[i] += effBalance * common.Gwei(finalityDelay) / common.Gwei(settings.InactivityPenaltyQuotient)
				}
			}
		}
	}

	return res, nil
}

func ProcessEpochRewardsAndPenalties(ctx context.Context, spec *common.Spec, epc *common.EpochsContext,
	attesterData *EpochAttesterData, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	currentEpoch := epc.CurrentEpoch.Epoch
	if currentEpoch == common.GENESIS_EPOCH {
		return nil
	}
	valCount := uint64(len(attesterData.Statuses))
	sum := common.NewDeltas(valCount)
	rewAndPenalties, err := AttestationRewardsAndPenalties(ctx, spec, epc, attesterData, state)
	if err != nil {
		return err
	}
	sum.Add(rewAndPenalties.Source)
	sum.Add(rewAndPenalties.Target)
	sum.Add(rewAndPenalties.Head)
	sum.Add(rewAndPenalties.InclusionDelay)
	sum.Add(rewAndPenalties.Inactivity)
	balances, err := common.ApplyDeltas(state, sum)
	if err != nil {
		return err
	}
	return state.SetBalances(balances)
}
