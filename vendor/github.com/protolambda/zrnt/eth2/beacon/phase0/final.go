package phase0

import (
	"context"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

func ProcessEffectiveBalanceUpdates(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, flats []common.FlatValidator, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	HYSTERESIS_INCREMENT := spec.EFFECTIVE_BALANCE_INCREMENT / common.Gwei(spec.HYSTERESIS_QUOTIENT)
	DOWNWARD_THRESHOLD := HYSTERESIS_INCREMENT * common.Gwei(spec.HYSTERESIS_DOWNWARD_MULTIPLIER)
	UPWARD_THRESHOLD := HYSTERESIS_INCREMENT * common.Gwei(spec.HYSTERESIS_UPWARD_MULTIPLIER)

	vals, err := state.Validators()
	if err != nil {
		return err
	}
	bals, err := state.Balances()
	if err != nil {
		return err
	}
	balIterNext := bals.Iter()
	for i := common.ValidatorIndex(0); true; i++ {
		balance, ok, err := balIterNext()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		effBalance := flats[i].EffectiveBalance
		if balance+DOWNWARD_THRESHOLD < effBalance || effBalance+UPWARD_THRESHOLD < balance {
			effBalance = balance - (balance % spec.EFFECTIVE_BALANCE_INCREMENT)
			if spec.MAX_EFFECTIVE_BALANCE < effBalance {
				effBalance = spec.MAX_EFFECTIVE_BALANCE
			}
			val, err := vals.Validator(i)
			if err != nil {
				return err
			}
			if err := val.SetEffectiveBalance(effBalance); err != nil {
				return err
			}
		}
	}
	return nil
}

func ProcessEth1DataReset(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reset eth1 data votes if it is the end of the voting period.
	if epc.NextEpoch.Epoch%spec.EPOCHS_PER_ETH1_VOTING_PERIOD == 0 {
		votes, err := state.Eth1DataVotes()
		if err != nil {
			return err
		}
		if err := votes.Reset(); err != nil {
			return err
		}
	}
	return nil
}

func ProcessSlashingsReset(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	slashings, err := state.Slashings()
	if err != nil {
		return err
	}
	return slashings.ResetSlashings(epc.NextEpoch.Epoch)
}

func ProcessRandaoMixesReset(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mixes, err := state.RandaoMixes()
	if err != nil {
		return err
	}
	return common.PrepareRandao(mixes, epc.NextEpoch.Epoch)
}

func ProcessHistoricalRootsUpdate(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Set historical root accumulator
	if epc.NextEpoch.Epoch%spec.SlotToEpoch(spec.SLOTS_PER_HISTORICAL_ROOT) == 0 {
		if err := common.UpdateHistoricalRoots(state); err != nil {
			return err
		}
	}
	return nil
}

func ProcessParticipationRecordUpdates(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state Phase0PendingAttestationsBeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Rotate current/previous epoch attestations
	prevAtts, err := state.PreviousEpochAttestations()
	if err != nil {
		return err
	}
	currAtts, err := state.CurrentEpochAttestations()
	if err != nil {
		return err
	}
	if err := prevAtts.SetBacking(currAtts.Backing()); err != nil {
		return err
	}
	if err := currAtts.SetBacking(PendingAttestationsType(spec).DefaultNode()); err != nil {
		return err
	}

	return nil
}
