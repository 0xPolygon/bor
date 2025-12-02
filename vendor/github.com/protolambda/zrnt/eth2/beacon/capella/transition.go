package capella

import (
	"bytes"
	"context"
	"fmt"

	"github.com/protolambda/ztyp/tree"

	"github.com/protolambda/zrnt/eth2/beacon/altair"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
)

func (state *BeaconStateView) ProcessEpoch(ctx context.Context, spec *common.Spec, epc *common.EpochsContext) error {
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	flats, err := common.FlattenValidators(vals)
	if err != nil {
		return err
	}
	attesterData, err := altair.ComputeEpochAttesterData(ctx, spec, epc, flats, state)
	if err != nil {
		return err
	}
	just := phase0.JustificationStakeData{
		CurrentEpoch:                  epc.CurrentEpoch.Epoch,
		TotalActiveStake:              epc.TotalActiveStake,
		PrevEpochUnslashedTargetStake: attesterData.PrevEpochUnslashedStake.TargetStake,
		CurrEpochUnslashedTargetStake: attesterData.CurrEpochUnslashedTargetStake,
	}
	if err := phase0.ProcessEpochJustification(ctx, spec, &just, state); err != nil {
		return err
	}
	if err := altair.ProcessInactivityUpdates(ctx, spec, attesterData, state); err != nil {
		return err
	}
	if err := altair.ProcessEpochRewardsAndPenalties(ctx, spec, epc, attesterData, state); err != nil {
		return err
	}
	if err := phase0.ProcessEpochRegistryUpdates(ctx, spec, epc, flats, state); err != nil {
		return err
	}
	// phase0 implementation, but with fork-logic, will account for changed slashing multiplier
	if err := phase0.ProcessEpochSlashings(ctx, spec, epc, flats, state); err != nil {
		return err
	}
	if err := phase0.ProcessEth1DataReset(ctx, spec, epc, state); err != nil {
		return err
	}
	if err := phase0.ProcessEffectiveBalanceUpdates(ctx, spec, epc, flats, state); err != nil {
		return err
	}
	if err := phase0.ProcessSlashingsReset(ctx, spec, epc, state); err != nil {
		return err
	}
	if err := phase0.ProcessRandaoMixesReset(ctx, spec, epc, state); err != nil {
		return err
	}
	if err := ProcessHistoricalSummariesUpdate(ctx, spec, epc, state); err != nil {
		return err
	}
	if err := altair.ProcessParticipationFlagUpdates(ctx, spec, state); err != nil {
		return err
	}
	if err := altair.ProcessSyncCommitteeUpdates(ctx, spec, epc, state); err != nil {
		return err
	}
	return nil
}

func (state *BeaconStateView) ProcessBlock(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, benv *common.BeaconBlockEnvelope) error {
	body, ok := benv.Body.(*BeaconBlockBody)
	if !ok {
		return fmt.Errorf("unexpected block type %T in Bellatrix ProcessBlock", benv.Body)
	}
	expectedProposer, err := epc.GetBeaconProposer(benv.Slot)
	if err != nil {
		return err
	}
	if err := common.ProcessHeader(ctx, spec, state, &benv.BeaconBlockHeader, expectedProposer); err != nil {
		return err
	}
	// [Modified in Capella] Removed `is_execution_enabled` check in Capella
	if err := ProcessWithdrawals(ctx, spec, state, &body.ExecutionPayload); err != nil {
		return err
	}
	// Modified in Capella
	eng, ok := spec.ExecutionEngine.(ExecutionEngine)
	if !ok {
		return fmt.Errorf("provided execution-engine interface does not support Capella: %T", spec.ExecutionEngine)
	}
	if err := ProcessExecutionPayload(ctx, spec, state, &body.ExecutionPayload, eng); err != nil {
		return err
	}
	if err := phase0.ProcessRandaoReveal(ctx, spec, epc, state, body.RandaoReveal); err != nil {
		return err
	}
	if err := phase0.ProcessEth1Vote(ctx, spec, epc, state, body.Eth1Data); err != nil {
		return err
	}
	// Safety checks, in case the user of the function provided too many operations
	if err := body.CheckLimits(spec); err != nil {
		return err
	}

	if err := phase0.ProcessProposerSlashings(ctx, spec, epc, state, body.ProposerSlashings); err != nil {
		return err
	}
	if err := phase0.ProcessAttesterSlashings(ctx, spec, epc, state, body.AttesterSlashings); err != nil {
		return err
	}
	if err := altair.ProcessAttestations(ctx, spec, epc, state, body.Attestations); err != nil {
		return err
	}
	// Note: state.AddValidator changed in Altair, but the deposit processing itself stayed the same.
	if err := phase0.ProcessDeposits(ctx, spec, epc, state, body.Deposits); err != nil {
		return err
	}
	if err := phase0.ProcessVoluntaryExits(ctx, spec, epc, state, body.VoluntaryExits); err != nil {
		return err
	}
	if err := ProcessBLSToExecutionChanges(ctx, spec, epc, state, body.BLSToExecutionChanges); err != nil {
		return err
	}
	if err := altair.ProcessSyncAggregate(ctx, spec, epc, state, &body.SyncAggregate); err != nil {
		return err
	}
	return nil
}

func HasEth1WithdrawalCredential(validator common.Validator) bool {
	withdrawalCredentials, err := validator.WithdrawalCredentials()
	if err != nil {
		panic(err)
	}
	return bytes.Equal(withdrawalCredentials[:1], []byte{common.ETH1_ADDRESS_WITHDRAWAL_PREFIX})
}

func Eth1WithdrawalCredential(validator common.Validator) common.Eth1Address {
	withdrawalCredentials, err := validator.WithdrawalCredentials()
	if err != nil {
		panic(err)
	}
	var address common.Eth1Address
	copy(address[:], withdrawalCredentials[12:])
	return address
}

func IsFullyWithdrawableValidator(validator common.Validator, balance common.Gwei, epoch common.Epoch) bool {
	withdrawableEpoch, err := validator.WithdrawableEpoch()
	if err != nil {
		panic(err)
	}
	return HasEth1WithdrawalCredential(validator) && withdrawableEpoch <= epoch && balance > 0
}

func IsPartiallyWithdrawableValidator(spec *common.Spec, validator common.Validator, balance common.Gwei, epoch common.Epoch) bool {
	effectiveBalance, err := validator.EffectiveBalance()
	if err != nil {
		panic(err)
	}
	hasMaxEffectiveBalance := effectiveBalance == spec.MAX_EFFECTIVE_BALANCE
	hasExcessBalance := balance > spec.MAX_EFFECTIVE_BALANCE
	return HasEth1WithdrawalCredential(validator) && hasMaxEffectiveBalance && hasExcessBalance
}

type BeaconStateWithWithdrawals interface {
	common.BeaconState
	NextWithdrawalValidatorIndex() (common.ValidatorIndex, error)
	SetNextWithdrawalIndex(nextIndex common.WithdrawalIndex) error
	SetNextWithdrawalValidatorIndex(nextValidator common.ValidatorIndex) error
	NextWithdrawalIndex() (common.WithdrawalIndex, error)
}

func GetExpectedWithdrawals(state BeaconStateWithWithdrawals, spec *common.Spec) ([]common.Withdrawal, error) {
	slot, err := state.Slot()
	if err != nil {
		return nil, err
	}
	epoch := spec.SlotToEpoch(slot)
	withdrawalIndex, err := state.NextWithdrawalIndex()
	if err != nil {
		return nil, err
	}
	validatorIndex, err := state.NextWithdrawalValidatorIndex()
	if err != nil {
		return nil, err
	}
	validators, err := state.Validators()
	if err != nil {
		return nil, err
	}
	validatorCount, err := validators.ValidatorCount()
	if err != nil {
		return nil, err
	}
	balances, err := state.Balances()
	if err != nil {
		return nil, err
	}
	withdrawals := make(common.Withdrawals, 0)
	var i uint64 = 0
	for {
		validator, err := validators.Validator(validatorIndex)
		if err != nil {
			return nil, err
		}
		balance, err := balances.GetBalance(validatorIndex)
		if err != nil {
			return nil, err
		}
		if i >= validatorCount || i >= uint64(spec.MAX_VALIDATORS_PER_WITHDRAWALS_SWEEP) {
			break
		}
		if IsFullyWithdrawableValidator(validator, balance, epoch) {
			withdrawals = append(withdrawals, common.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        Eth1WithdrawalCredential(validator),
				Amount:         balance,
			})
			withdrawalIndex += 1
		} else if IsPartiallyWithdrawableValidator(spec, validator, balance, epoch) {
			withdrawals = append(withdrawals, common.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        Eth1WithdrawalCredential(validator),
				Amount:         balance - spec.MAX_EFFECTIVE_BALANCE,
			})
			withdrawalIndex += 1
		}
		if len(withdrawals) == int(spec.MAX_WITHDRAWALS_PER_PAYLOAD) {
			break
		}
		validatorIndex = common.ValidatorIndex(uint64(validatorIndex+1) % validatorCount)
		i += 1
	}
	return withdrawals, nil
}

type ExecutionPayloadWithWithdrawals interface {
	GetWitdrawals() []common.Withdrawal
}

func ProcessWithdrawals(ctx context.Context, spec *common.Spec, state BeaconStateWithWithdrawals, executionPayload ExecutionPayloadWithWithdrawals) error {
	expectedWithdrawals, err := GetExpectedWithdrawals(state, spec)
	if err != nil {
		return err
	}
	withdrawals := executionPayload.GetWitdrawals()
	if len(expectedWithdrawals) != len(withdrawals) {
		return fmt.Errorf("unexpected number of withdrawals in Capella ProcessWithdrawals: want=%d, got=%d", len(expectedWithdrawals), len(withdrawals))
	}
	bals, err := state.Balances()
	if err != nil {
		return err
	}
	for w := 0; w < len(expectedWithdrawals); w++ {
		withdrawal := withdrawals[w]
		expectedWithdrawal := expectedWithdrawals[w]
		if withdrawal.Index != expectedWithdrawal.Index ||
			withdrawal.ValidatorIndex != expectedWithdrawal.ValidatorIndex ||
			!bytes.Equal(withdrawal.Address[:], expectedWithdrawal.Address[:]) ||
			withdrawal.Amount != expectedWithdrawal.Amount {
			return fmt.Errorf("unexpected withdrawal in Capella ProcessWithdrawals: want=%s, got=%s", expectedWithdrawal, withdrawal)
		}
		if err := common.DecreaseBalance(bals, expectedWithdrawal.ValidatorIndex, expectedWithdrawal.Amount); err != nil {
			return fmt.Errorf("failed to decrease balance: %w", err)
		}
	}
	if len(expectedWithdrawals) > 0 {
		latestWithdrawal := expectedWithdrawals[len(expectedWithdrawals)-1]
		if err := state.SetNextWithdrawalIndex(latestWithdrawal.Index + 1); err != nil {
			return fmt.Errorf("failed to set withdrawal index: %w", err)
		}
	}
	validators, err := state.Validators()
	if err != nil {
		return err
	}
	validatorCount, err := validators.ValidatorCount()
	if err != nil {
		return err
	}
	if len(expectedWithdrawals) == int(spec.MAX_WITHDRAWALS_PER_PAYLOAD) {
		latestWithdrawal := expectedWithdrawals[len(expectedWithdrawals)-1]
		nextValidatorIndex := common.ValidatorIndex(uint64(latestWithdrawal.ValidatorIndex+1) % validatorCount)
		if err = state.SetNextWithdrawalValidatorIndex(nextValidatorIndex); err != nil {
			return err
		}
	} else {
		nextValidatorIndex, err := state.NextWithdrawalValidatorIndex()
		if err != nil {
			return err
		}
		nextValidatorIndex = common.ValidatorIndex((uint64(nextValidatorIndex) + uint64(spec.MAX_VALIDATORS_PER_WITHDRAWALS_SWEEP)) % validatorCount)
		if err = state.SetNextWithdrawalValidatorIndex(nextValidatorIndex); err != nil {
			return err
		}
	}
	return nil
}

type HistoricalSummariesBeaconState interface {
	common.BeaconState
	HistoricalSummaries() (HistoricalSummariesList, error)
}

func ProcessHistoricalSummariesUpdate(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state HistoricalSummariesBeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Set historical summaries accumulator
	if epc.NextEpoch.Epoch%spec.SlotToEpoch(spec.SLOTS_PER_HISTORICAL_ROOT) == 0 {
		if err := UpdateHistoricalSummaries(state); err != nil {
			return err
		}
	}
	return nil
}

func UpdateHistoricalSummaries(state HistoricalSummariesBeaconState) error {
	histSummaries, err := state.HistoricalSummaries()
	if err != nil {
		return err
	}
	blockRoots, err := state.BlockRoots()
	if err != nil {
		return err
	}
	stateRoots, err := state.StateRoots()
	if err != nil {
		return err
	}
	hFn := tree.GetHashFn()
	return histSummaries.Append(HistoricalSummary{
		BlockSummaryRoot: blockRoots.HashTreeRoot(hFn),
		StateSummaryRoot: stateRoots.HashTreeRoot(hFn),
	})
}
