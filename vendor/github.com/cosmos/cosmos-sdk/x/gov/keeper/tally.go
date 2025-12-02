package keeper

import (
	"context"
	"strings"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// TODO: Break into several smaller functions for clarity

// Tally iterates over the votes and updates the tally of a proposal based on the voting power of the
// voters
func (keeper Keeper) Tally(ctx context.Context, proposal v1.Proposal) (passes, burnDeposits bool, tallyResults v1.TallyResult, err error) {
	results := make(map[v1.VoteOption]math.LegacyDec)
	results[v1.OptionYes] = math.LegacyZeroDec()
	results[v1.OptionAbstain] = math.LegacyZeroDec()
	results[v1.OptionNo] = math.LegacyZeroDec()
	results[v1.OptionNoWithVeto] = math.LegacyZeroDec()

	totalBondedTokens := math.LegacyZeroDec()
	totalVotingPower := math.LegacyZeroDec()
	currValidators := make(map[string]v1.ValidatorGovInfo)

	err = keeper.sk.IterateCurrentValidatorsAndApplyFn(ctx, func(validator stakeTypes.Validator) bool {
		valBz, err := keeper.sk.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
		if err != nil {
			return false
		}
		currValidators[strings.TrimPrefix(strings.ToLower(validator.GetOperator()), "0x")] = v1.NewValidatorGovInfo(
			valBz,
			// HV2: using validator.GetBondedTokens() as custom staking module will return the validator's VotingPower for it
			validator.GetBondedTokens(),
			math.LegacyZeroDec(),
			math.LegacyZeroDec(),
			v1.WeightedVoteOptions{},
		)

		return false
	})
	if err != nil {
		return false, false, tallyResults, err
	}

	rng := collections.NewPrefixedPairRange[uint64, sdk.AccAddress](proposal.Id)
	err = keeper.Votes.Walk(ctx, rng, func(_ collections.Pair[uint64, sdk.AccAddress], vote v1.Vote) (bool, error) {
		// if validator, just record it in the map
		voter, err := keeper.authKeeper.AddressCodec().StringToBytes(vote.Voter)
		if err != nil {
			return false, err
		}

		valAddrStr, err := keeper.sk.ValidatorAddressCodec().BytesToString(voter)
		if err != nil {
			return false, err
		}
		valAddrStr = strings.TrimPrefix(strings.ToLower(valAddrStr), "0x")
		if val, ok := currValidators[valAddrStr]; ok {
			val.Vote = vote.Options
			currValidators[valAddrStr] = val
		}

		/* HV2: delegations not supported in heimdall
		// iterate over all delegations from voter, deduct from any delegated-to validators
		err = keeper.sk.IterateDelegations(ctx, voter, func(index int64, delegation stakingtypes.DelegationI) (stop bool) {
			valAddrStr := delegation.GetValidatorAddr()

			if val, ok := currValidators[valAddrStr]; ok {
				// There is no need to handle the special case that validator address equal to voter address.
				// Because voter's voting power will tally again even if there will be deduction of voter's voting power from validator.
				val.DelegatorDeductions = val.DelegatorDeductions.Add(delegation.GetShares())
				currValidators[valAddrStr] = val

				// delegation shares * bonded / total shares
				votingPower := delegation.GetShares().MulInt(val.BondedTokens).Quo(val.DelegatorShares)

				for _, option := range vote.Options {
					weight, _ := math.LegacyNewDecFromStr(option.Weight)
					subPower := votingPower.Mul(weight)
					results[option.Option] = results[option.Option].Add(subPower)
				}
				totalVotingPower = totalVotingPower.Add(votingPower)
			}

			return false
		})
		if err != nil {
			return false, err
		}
		*/
		return false, keeper.Votes.Remove(ctx, collections.Join(vote.ProposalId, sdk.AccAddress(voter)))
	})
	if err != nil {
		return false, false, tallyResults, err
	}

	// iterate over the validators again to tally their voting power
	for _, val := range currValidators {
		// HV2: using val.BondedTokens as this is heimdall's val.VotingPower
		votingPower := math.LegacyNewDec(val.BondedTokens.Int64())
		totalBondedTokens = totalBondedTokens.Add(votingPower)

		if len(val.Vote) == 0 {
			continue
		}

		/* HV2: removed the following lines as they are also deleted in heimdall
		sharesAfterDeductions := val.DelegatorShares.Sub(val.DelegatorDeductions)
		votingPower := sharesAfterDeductions.MulInt(val.BondedTokens).Quo(val.DelegatorShares)
		*/

		// HV2: check on the length of the options.
		// This should never fail as all votes must have only 1 option, hence we panic if something goes wrong
		err := keeper.assertVoteOptionsLength(val.Vote)
		if err != nil {
			panic(err)
		}

		for _, option := range val.Vote {
			// HV2: check on the weight of the option.
			// This should never fail as the only option should have weight=1, hence we panic if something goes wrong
			if !v1.ValidWeightedVoteOption(*option) {
				panic(err)
			}
			weight, _ := math.LegacyNewDecFromStr(option.Weight)
			subPower := votingPower.Mul(weight)
			results[option.Option] = results[option.Option].Add(subPower)
		}
		totalVotingPower = totalVotingPower.Add(votingPower)
	}

	params, err := keeper.Params.Get(ctx)
	if err != nil {
		return false, false, tallyResults, err
	}
	tallyResults = v1.NewTallyResultFromMap(results)

	/* 	HV2: this has been removed from heimdall's gov/tally.go and replaced with zero check on totalVotingPower
	// If there is no staked coins, the proposal fails
	totalBonded, err := keeper.sk.TotalBondedTokens(ctx)
	if err != nil {
		return false, false, tallyResults, err
	}
	*/

	// HV2: using zero check on totalVotingPower instead of totalBonded {
	if totalVotingPower.IsZero() {
		return false, false, tallyResults, nil
	}

	// If there is not enough quorum of votes, the proposal fails
	percentVoting := totalVotingPower.Quo(totalBondedTokens)
	quorum, _ := math.LegacyNewDecFromStr(params.Quorum)
	if percentVoting.LT(quorum) {
		return false, params.BurnVoteQuorum, tallyResults, nil
	}

	// If no one votes (everyone abstains), proposal fails
	if totalVotingPower.Sub(results[v1.OptionAbstain]).Equal(math.LegacyZeroDec()) {
		return false, false, tallyResults, nil
	}

	// If more than 1/3 of voters veto, proposal fails
	vetoThreshold, _ := math.LegacyNewDecFromStr(params.VetoThreshold)
	if results[v1.OptionNoWithVeto].Quo(totalVotingPower).GT(vetoThreshold) {
		return false, params.BurnVoteVeto, tallyResults, nil
	}

	// If more than 1/2 of non-abstaining voters vote Yes, proposal passes
	// For expedited 2/3
	var thresholdStr string
	if proposal.Expedited {
		thresholdStr = params.GetExpeditedThreshold()
	} else {
		thresholdStr = params.GetThreshold()
	}

	threshold, _ := math.LegacyNewDecFromStr(thresholdStr)

	if results[v1.OptionYes].Quo(totalVotingPower.Sub(results[v1.OptionAbstain])).GT(threshold) {
		return true, false, tallyResults, nil
	}

	// If more than 1/2 of non-abstaining voters vote No, proposal fails
	return false, false, tallyResults, nil
}
