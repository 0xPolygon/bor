package phase0

import (
	"context"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

type EpochStakeSummary struct {
	SourceStake common.Gwei
	TargetStake common.Gwei
	HeadStake   common.Gwei
}

type AttesterFlag uint8

func (flags AttesterFlag) HasMarkers(markers AttesterFlag) bool {
	return flags&markers == markers
}

const (
	PrevSourceAttester AttesterFlag = 1 << iota
	PrevTargetAttester
	PrevHeadAttester

	CurrSourceAttester
	CurrTargetAttester
	CurrHeadAttester

	UnslashedAttester
	EligibleAttester
)

type AttesterStatus struct {
	// The delay of inclusion of the latest attestation by the attester.
	// No delay (i.e. 0) by default
	InclusionDelay common.Slot
	// The validator index of the proposer of the attested beacon block.
	// Only valid if the validator has an attesting flag set.
	AttestedProposer common.ValidatorIndex
	// A bitfield of markers describing the recent actions of the validator
	Flags AttesterFlag
}

type EpochAttesterData struct {
	PrevEpoch common.Epoch
	CurrEpoch common.Epoch

	Statuses []AttesterStatus
	Flats    []common.FlatValidator

	PrevEpochUnslashedStake       EpochStakeSummary
	CurrEpochUnslashedTargetStake common.Gwei
}

type Phase0PendingAttestationsBeaconState interface {
	common.BeaconState
	PreviousEpochAttestations() (*PendingAttestationsView, error)
	CurrentEpochAttestations() (*PendingAttestationsView, error)
}

func ComputeEpochAttesterData(ctx context.Context, spec *common.Spec, epc *common.EpochsContext,
	flats []common.FlatValidator, state Phase0PendingAttestationsBeaconState) (out *EpochAttesterData, err error) {

	count := common.ValidatorIndex(len(flats))
	prevEpoch := epc.PreviousEpoch.Epoch
	currentEpoch := epc.CurrentEpoch.Epoch

	out = &EpochAttesterData{
		PrevEpoch: prevEpoch,
		CurrEpoch: currentEpoch,
		Statuses:  make([]AttesterStatus, count, count),
		Flats:     flats,
	}

	for i := common.ValidatorIndex(0); i < count; i++ {
		flat := &flats[i]

		status := &out.Statuses[i]
		status.AttestedProposer = common.ValidatorIndexMarker

		if !flat.Slashed {
			status.Flags |= UnslashedAttester
		}

		if flat.IsActive(prevEpoch) || (flat.Slashed && (prevEpoch+1 < flat.WithdrawableEpoch)) {
			status.Flags |= EligibleAttester
		}
	}

	processEpoch := func(
		attestations *PendingAttestationsView,
		epoch common.Epoch,
		sourceFlag, targetFlag, headFlag AttesterFlag) error {

		startSlot, err := spec.EpochStartSlot(epoch)
		if err != nil {
			return err
		}
		actualTargetBlockRoot, err := common.GetBlockRootAtSlot(spec, state, startSlot)
		if err != nil {
			return err
		}
		participants := make([]common.ValidatorIndex, 0, spec.MAX_VALIDATORS_PER_COMMITTEE)
		attIter := attestations.ReadonlyIter()
		i := 0
		for {
			// every 32 attestations, check if the context is done.
			if i&((1<<5)-1) == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			el, ok, err := attIter.Next()
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			attView, err := AsPendingAttestation(el, nil)
			if err != nil {
				return err
			}
			att, err := attView.Raw()
			if err != nil {
				return err
			}

			attBlockRoot, err := common.GetBlockRootAtSlot(spec, state, att.Data.Slot)
			if err != nil {
				return err
			}

			// attestation-target is already known to be this epoch, get it from the pre-computed shuffling directly.
			committee, err := epc.GetBeaconCommittee(att.Data.Slot, att.Data.Index)
			if err != nil {
				return err
			}

			participants = participants[:0]                                     // reset old slice (re-used in for loop)
			participants = append(participants, committee...)                   // add committee indices
			participants = att.AggregationBits.FilterParticipants(participants) // only keep the participants

			if epoch == prevEpoch {
				for _, p := range participants {
					status := &out.Statuses[p]

					// If the attestation is the earliest, i.e. has the smallest delay
					if status.AttestedProposer == common.ValidatorIndexMarker || status.InclusionDelay > att.InclusionDelay {
						status.InclusionDelay = att.InclusionDelay
						status.AttestedProposer = att.ProposerIndex
					}
				}
			}

			for _, p := range participants {
				status := &out.Statuses[p]

				// remember the participant as one of the good validators
				status.Flags |= sourceFlag

				// If the attestation is for the boundary:
				if att.Data.Target.Root == actualTargetBlockRoot {
					status.Flags |= targetFlag

					// If the attestation is for the head (att the time of attestation):
					if att.Data.BeaconBlockRoot == attBlockRoot {
						status.Flags |= headFlag
					}
				}
			}
			i += 1
		}
		return nil
	}
	prevAtts, err := state.PreviousEpochAttestations()
	if err != nil {
		return nil, err
	}
	if err := processEpoch(prevAtts, prevEpoch,
		PrevSourceAttester, PrevTargetAttester, PrevHeadAttester); err != nil {
		return nil, err
	}
	currAtts, err := state.CurrentEpochAttestations()
	if err != nil {
		return nil, err
	}
	if err := processEpoch(currAtts, currentEpoch,
		CurrSourceAttester, CurrTargetAttester, CurrHeadAttester); err != nil {
		return nil, err
	}

	for i := 0; i < len(out.Statuses); i++ {
		status := &out.Statuses[i]
		flat := &flats[i]
		// nested, since they are subsets anyway
		if status.Flags.HasMarkers(PrevSourceAttester | UnslashedAttester) {
			out.PrevEpochUnslashedStake.SourceStake += flat.EffectiveBalance
			// already know it's unslashed, just look if attesting target, then head
			if status.Flags.HasMarkers(PrevTargetAttester) {
				out.PrevEpochUnslashedStake.TargetStake += flat.EffectiveBalance
				if status.Flags.HasMarkers(PrevHeadAttester) {
					out.PrevEpochUnslashedStake.HeadStake += flat.EffectiveBalance
				}
			}
		}
		if status.Flags.HasMarkers(CurrTargetAttester | UnslashedAttester) {
			out.CurrEpochUnslashedTargetStake += flat.EffectiveBalance
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

	return
}
