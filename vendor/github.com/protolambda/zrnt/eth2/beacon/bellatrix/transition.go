package bellatrix

import (
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
	if err := phase0.ProcessHistoricalRootsUpdate(ctx, spec, epc, state); err != nil {
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
	block := &BeaconBlock{
		Slot:          benv.Slot,
		ProposerIndex: benv.ProposerIndex,
		ParentRoot:    benv.ParentRoot,
		StateRoot:     benv.StateRoot,
		Body:          *body,
	}
	if enabled, err := state.IsExecutionEnabled(spec, block); err != nil {
		return err
	} else if enabled {
		// New in Bellatrix
		eng, ok := spec.ExecutionEngine.(ExecutionEngine)
		if !ok {
			return fmt.Errorf("provided execution-engine interface does not support Bellatrix: %T", spec.ExecutionEngine)
		}
		if err := ProcessExecutionPayload(ctx, spec, state, &body.ExecutionPayload, eng); err != nil {
			return err
		}
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
	if err := altair.ProcessSyncAggregate(ctx, spec, epc, state, &body.SyncAggregate); err != nil {
		return err
	}
	return nil
}

type ExecutionUpgradeBeaconState interface {
	IsExecutionEnabled(spec *common.Spec, block *BeaconBlock) (bool, error)
	IsTransitionCompleted() (bool, error)
	IsTransitionBlock(spec *common.Spec, block *BeaconBlock) (bool, error)
}

type ExecutionTrackingBeaconState interface {
	common.BeaconState

	LatestExecutionPayloadHeader() (*ExecutionPayloadHeaderView, error)
	SetLatestExecutionPayloadHeader(h *ExecutionPayloadHeader) error
}

func (state *BeaconStateView) IsExecutionEnabled(spec *common.Spec, block *BeaconBlock) (bool, error) {
	isTransitionCompleted, err := state.IsTransitionCompleted()
	if err != nil {
		return false, err
	}
	if isTransitionCompleted {
		return true, nil
	}
	return state.IsTransitionBlock(spec, block)
}

func (state *BeaconStateView) IsTransitionCompleted() (bool, error) {
	execHeader, err := state.LatestExecutionPayloadHeader()
	if err != nil {
		return false, err
	}
	empty := ExecutionPayloadHeaderType.DefaultNode().MerkleRoot(tree.GetHashFn())
	return execHeader.HashTreeRoot(tree.GetHashFn()) != empty, nil
}

func (state *BeaconStateView) IsTransitionBlock(spec *common.Spec, block *BeaconBlock) (bool, error) {
	isTransitionCompleted, err := state.IsTransitionCompleted()
	if err != nil {
		return false, err
	}
	if isTransitionCompleted {
		return false, nil
	}
	empty := ExecutionPayloadType(spec).DefaultNode().MerkleRoot(tree.GetHashFn())
	return block.Body.ExecutionPayload.HashTreeRoot(spec, tree.GetHashFn()) != empty, nil
}
