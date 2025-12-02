package altair

import (
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
	"github.com/protolambda/ztyp/view"
)

func TranslateParticipation(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState,
	pendingAtts *phase0.PendingAttestationsView, participationRegistry ParticipationRegistry) error {
	attIter := pendingAtts.ReadonlyIter()
	for {
		el, ok, err := attIter.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		attView, err := phase0.AsPendingAttestation(el, nil)
		if err != nil {
			return err
		}
		att, err := attView.Raw()
		if err != nil {
			return err
		}
		data := &att.Data
		inclusionDelay := att.InclusionDelay
		applicableFlags, err := GetApplicableAttestationParticipationFlags(spec, state, data, inclusionDelay)
		if err != nil {
			return err
		}
		committee, err := epc.GetBeaconCommittee(att.Data.Slot, att.Data.Index)
		if err != nil {
			return err
		}
		for i, vi := range committee {
			if att.AggregationBits.GetBit(uint64(i)) {
				participationRegistry[vi] |= applicableFlags
			}
		}
	}
	return nil
}

func UpgradeToAltair(spec *common.Spec, epc *common.EpochsContext, pre *phase0.BeaconStateView) (*BeaconStateView, error) {
	// yes, super ugly code, but it does transfer compatible subtrees without duplicating data or breaking caches
	slot, err := pre.Slot()
	if err != nil {
		return nil, err
	}
	epoch := spec.SlotToEpoch(slot)
	genesisTime, err := pre.GenesisTime()
	if err != nil {
		return nil, err
	}
	genesisValidatorsRoot, err := pre.GenesisValidatorsRoot()
	if err != nil {
		return nil, err
	}
	preFork, err := pre.Fork()
	if err != nil {
		return nil, err
	}
	fork := common.Fork{
		PreviousVersion: preFork.CurrentVersion,
		CurrentVersion:  spec.ALTAIR_FORK_VERSION,
		Epoch:           epoch,
	}
	latestBlockHeader, err := pre.LatestBlockHeader()
	if err != nil {
		return nil, err
	}
	blockRoots, err := pre.BlockRoots()
	if err != nil {
		return nil, err
	}
	stateRoots, err := pre.StateRoots()
	if err != nil {
		return nil, err
	}
	historicalRoots, err := pre.HistoricalRoots()
	if err != nil {
		return nil, err
	}
	eth1Data, err := pre.Eth1Data()
	if err != nil {
		return nil, err
	}
	eth1DataVotes, err := pre.Eth1DataVotes()
	if err != nil {
		return nil, err
	}
	eth1DepositIndex, err := pre.Eth1DepositIndex()
	if err != nil {
		return nil, err
	}
	validators, err := pre.Validators()
	if err != nil {
		return nil, err
	}
	balances, err := pre.Balances()
	if err != nil {
		return nil, err
	}
	randaoMixes, err := pre.RandaoMixes()
	if err != nil {
		return nil, err
	}
	slashings, err := pre.Slashings()
	if err != nil {
		return nil, err
	}
	valCount, err := validators.ValidatorCount()
	if err != nil {
		return nil, err
	}

	// Fill in previous epoch participation from the pre state's pending attestations
	prevPendingAtts, err := pre.PreviousEpochAttestations()
	if err != nil {
		return nil, err
	}
	prevRegistry := make(ParticipationRegistry, valCount, valCount)
	if err := TranslateParticipation(spec, epc, pre, prevPendingAtts, prevRegistry); err != nil {
		return nil, err
	}
	previousEpochParticipation, err := prevRegistry.View(spec)
	if err != nil {
		return nil, err
	}

	// current registry is empty
	currRegistry := make(ParticipationRegistry, valCount, valCount)
	currentEpochParticipation, err := currRegistry.View(spec)
	if err != nil {
		return nil, err
	}

	justBits, err := pre.JustificationBits()
	if err != nil {
		return nil, err
	}
	prevJustCh, err := pre.PreviousJustifiedCheckpoint()
	if err != nil {
		return nil, err
	}
	currJustCh, err := pre.CurrentJustifiedCheckpoint()
	if err != nil {
		return nil, err
	}
	finCh, err := pre.FinalizedCheckpoint()
	if err != nil {
		return nil, err
	}
	emptyScores := make(InactivityScores, valCount, valCount)
	inactivityScores, err := emptyScores.View(spec)
	if err != nil {
		return nil, err
	}

	// Fill in sync committees
	// Note: A duplicate committee is assigned for the current and next committee at the fork boundary
	nextSyncCommittee, err := common.ComputeNextSyncCommittee(spec, epc, pre)
	if err != nil {
		return nil, err
	}
	currentSyncCommitteeView, err := nextSyncCommittee.View(spec)
	if err != nil {
		return nil, err
	}
	nextSyncCommitteeView, err := currentSyncCommitteeView.Copy()
	if err != nil {
		return nil, err
	}

	return AsBeaconStateView(BeaconStateType(spec).FromFields(
		(*view.Uint64View)(&genesisTime),
		(*view.RootView)(&genesisValidatorsRoot),
		(*view.Uint64View)(&slot),
		fork.View(),
		latestBlockHeader.View(),
		blockRoots.(view.View),
		stateRoots.(view.View),
		historicalRoots.(view.View),
		eth1Data.View(),
		eth1DataVotes.(view.View),
		(*view.Uint64View)(&eth1DepositIndex),
		validators.(view.View),
		balances.(view.View),
		randaoMixes.(view.View),
		slashings.(view.View),
		previousEpochParticipation,
		currentEpochParticipation,
		justBits.View(),
		prevJustCh.View(),
		currJustCh.View(),
		finCh.View(),
		inactivityScores,
		currentSyncCommitteeView,
		nextSyncCommitteeView,
	))
}
