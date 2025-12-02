package common

import "github.com/protolambda/ztyp/tree"

// Return the block root at a recent slot. Only valid to SLOTS_PER_HISTORICAL_ROOT slots ago.
func GetBlockRootAtSlot(spec *Spec, state BeaconState, slot Slot) (Root, error) {
	blockRoots, err := state.BlockRoots()
	if err != nil {
		return Root{}, err
	}
	return blockRoots.GetRoot(slot)
}

// Return the block root at a recent epoch. Only valid to SLOTS_PER_HISTORICAL_ROOT slots ago.
func GetBlockRoot(spec *Spec, state BeaconState, epoch Epoch) (Root, error) {
	blockRoots, err := state.BlockRoots()
	if err != nil {
		return Root{}, err
	}
	startSlot, err := spec.EpochStartSlot(epoch)
	if err != nil {
		return Root{}, err
	}
	return blockRoots.GetRoot(startSlot)
}

func SetRecentRoots(spec *Spec, state BeaconState, slot Slot, blockRoot Root, stateRoot Root) error {
	blockRootsBatch, err := state.BlockRoots()
	if err != nil {
		return err
	}
	stateRootsBatch, err := state.StateRoots()
	if err != nil {
		return err
	}
	if err := blockRootsBatch.SetRoot(slot%spec.SLOTS_PER_HISTORICAL_ROOT, blockRoot); err != nil {
		return err
	}
	if err := stateRootsBatch.SetRoot(slot%spec.SLOTS_PER_HISTORICAL_ROOT, stateRoot); err != nil {
		return err
	}
	return nil
}

func UpdateHistoricalRoots(state BeaconState) error {
	histRoots, err := state.HistoricalRoots()
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
	// emulating HistoricalBatch here
	hFn := tree.GetHashFn()
	return histRoots.Append(tree.Hash(blockRoots.HashTreeRoot(hFn), stateRoots.HashTreeRoot(hFn)))
}
