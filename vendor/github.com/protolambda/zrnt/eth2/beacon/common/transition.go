package common

import (
	"context"
	"errors"
	"fmt"

	"github.com/protolambda/ztyp/tree"
)

func ProcessSlot(ctx context.Context, _ *Spec, state BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The state root could take long, but absolute worst case is around a 1.5 seconds.
	// With any caching, this is more like < 50 ms. So no context use.
	// Cache state root
	previousStateRoot := state.HashTreeRoot(tree.GetHashFn())

	stateRootsBatch, err := state.StateRoots()
	if err != nil {
		return err
	}
	slot, err := state.Slot()
	if err != nil {
		return err
	}
	if err := stateRootsBatch.SetRoot(slot, previousStateRoot); err != nil {
		return err
	}

	latestHeader, err := state.LatestBlockHeader()
	if err != nil {
		return err
	}
	if latestHeader.StateRoot == (Root{}) {
		latestHeader.StateRoot = previousStateRoot
		if err := state.SetLatestBlockHeader(latestHeader); err != nil {
			return err
		}
	}
	previousBlockRoot := latestHeader.HashTreeRoot(tree.GetHashFn())

	// Cache latest known block and state root
	blockRootsBatch, err := state.BlockRoots()
	if err != nil {
		return err
	}
	if err := blockRootsBatch.SetRoot(slot, previousBlockRoot); err != nil {
		return err
	}

	return nil
}

// Process the state to the given slot.
// Returns an error if the slot is older than the state is already at.
// Mutates the state, does not copy.
func ProcessSlots(ctx context.Context, spec *Spec, epc *EpochsContext, state UpgradeableBeaconState, slot Slot) error {
	// happens at the start of every CurrentSlot
	currentSlot, err := state.Slot()
	if err != nil {
		return err
	}
	if currentSlot >= slot {
		return errors.New("cannot transition from pre-state with higher or equal slot than transition target")
	}
	for currentSlot < slot {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ProcessSlot(ctx, spec, state); err != nil {
			return err
		}
		// Per-epoch transition happens at the start of the first slot of every epoch.
		// (with the slot still at the end of the last epoch)
		isEpochEnd := spec.SlotToEpoch(currentSlot+1) != spec.SlotToEpoch(currentSlot)
		if isEpochEnd {
			if err := state.ProcessEpoch(ctx, spec, epc); err != nil {
				return err
			}
		}
		currentSlot += 1
		if err := state.SetSlot(currentSlot); err != nil {
			return err
		}

		if isEpochEnd {
			if err := epc.RotateEpochs(state); err != nil {
				return err
			}
		}

		if err := state.UpgradeMaybe(ctx, spec, epc); err != nil {
			return err
		}
	}
	return nil
}

// StateTransition to the slot of the given block, then process the block.
// Returns an error if the slot is older or equal to what the state is already at.
// Mutates the state, does not copy.
func StateTransition(ctx context.Context, spec *Spec, epc *EpochsContext, state UpgradeableBeaconState, benv *BeaconBlockEnvelope, validateResult bool) error {
	if err := ProcessSlots(ctx, spec, epc, state, benv.Slot); err != nil {
		return err
	}
	return PostSlotTransition(ctx, spec, epc, state, benv, validateResult)
}

// PostSlotTransition finishes a state transition after applying ProcessSlots(..., block.Slot).
func PostSlotTransition(ctx context.Context, spec *Spec, epc *EpochsContext, state BeaconState, benv *BeaconBlockEnvelope, validateResult bool) error {
	slot, err := state.Slot()
	if err != nil {
		return err
	}
	if slot != benv.Slot {
		return fmt.Errorf("transition of block, post-slot-processing, must run on state with same slot")
	}
	if validateResult {
		// TODO: tests have invalid fork version in state
		fork, err := state.Fork()
		if err != nil {
			return err
		}
		//version := spec.ForkVersion(benv.Slot)
		//if fork.CurrentVersion != version {
		//	return fmt.Errorf("state does not have expected fork version of block slot: %s <> %s (slot %d)",
		//		fork.CurrentVersion, version, benv.Slot)
		//}
		proposer, err := epc.GetBeaconProposer(benv.Slot)
		if err != nil {
			return err
		}
		genValRoot, err := state.GenesisValidatorsRoot()
		if err != nil {
			return err
		}
		pub, ok := epc.ValidatorPubkeyCache.Pubkey(proposer)
		if !ok {
			return fmt.Errorf("unknown pubkey for proposer %d", proposer)
		}
		if !benv.VerifySignatureVersioned(spec, fork.CurrentVersion, genValRoot, proposer, pub) {
			return errors.New("block has invalid signature")
		}
	}
	if err := state.ProcessBlock(ctx, spec, epc, benv); err != nil {
		return err
	}

	// State root verification
	if validateResult && benv.StateRoot != state.HashTreeRoot(tree.GetHashFn()) {
		return errors.New("block has invalid state root")
	}
	return nil
}
