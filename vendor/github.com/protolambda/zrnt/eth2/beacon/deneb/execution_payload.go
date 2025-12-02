package deneb

import (
	"context"
	"errors"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

func ProcessExecutionPayload(ctx context.Context, spec *common.Spec, state ExecutionTrackingBeaconState, body *BeaconBlockBody, engine ExecutionEngine) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if engine == nil {
		return errors.New("nil execution engine")
	}
	payload := &body.ExecutionPayload

	slot, err := state.Slot()
	if err != nil {
		return err
	}

	latestExecHeader, err := state.LatestExecutionPayloadHeader()
	if err != nil {
		return err
	}
	// Verify consistency of the parent hash with respect to the previous execution payload header
	parent, err := latestExecHeader.Raw()
	if err != nil {
		return fmt.Errorf("failed to read previous header: %v", err)
	}
	if payload.ParentHash != parent.BlockHash {
		return fmt.Errorf("expected parent hash %s in execution payload, but got %s",
			parent.BlockHash, payload.ParentHash)
	}

	// Verify prev_randao
	mixes, err := state.RandaoMixes()
	if err != nil {
		return err
	}
	expectedMix, err := mixes.GetRandomMix(spec.SlotToEpoch(slot))
	if err != nil {
		return err
	}
	if payload.PrevRandao != expectedMix {
		return fmt.Errorf("invalid random data %s, expected %s", payload.PrevRandao, expectedMix)
	}

	// Verify timestamp
	genesisTime, err := state.GenesisTime()
	if err != nil {
		return err
	}
	if expectedTime, err := spec.TimeAtSlot(slot, genesisTime); err != nil {
		return fmt.Errorf("slot or genesis time in state is corrupt, cannot compute time: %v", err)
	} else if payload.Timestamp != expectedTime {
		return fmt.Errorf("state at slot %d, genesis time %d, expected execution payload time %d, but got %d",
			slot, genesisTime, expectedTime, payload.Timestamp)
	}

	// [New in Deneb:EIP4844] Verify commitments are under limit
	if uint64(len(body.BlobKZGCommitments)) > uint64(spec.MAX_BLOBS_PER_BLOCK) {
		return fmt.Errorf("too many blob KZG commitments: %d", len(body.BLSToExecutionChanges))
	}

	// Verify the execution payload is valid
	// [Modified in Deneb:EIP4844] Pass `versioned_hashes` to Execution Engine
	// [Modified in Deneb:EIP4788] Pass `parent_beacon_block_root` to Execution Engine
	versionedHashes := make([]common.Hash32, 0, len(body.BlobKZGCommitments))
	for _, commit := range body.BlobKZGCommitments {
		versionedHashes = append(versionedHashes, commit.ToVersionedHash())
	}
	latestHeader, err := state.LatestBlockHeader()
	if err != nil {
		return fmt.Errorf("failed to get current in-progresss latest beacon-block-header from beacon state: %w", err)
	}
	if valid, err := VerifyAndNotifyNewPayload(ctx, engine, &NewPayloadRequest{
		ExecutionPayload:      payload,
		VersionedHashes:       versionedHashes,
		ParentBeaconBlockRoot: latestHeader.ParentRoot,
	}); err != nil {
		return fmt.Errorf("unexpected problem in execution engine when inserting block %s (height %d), err: %v",
			payload.BlockHash, payload.BlockNumber, err)
	} else if !valid {
		return fmt.Errorf("execution engine says payload is invalid: %s (height %d)",
			payload.BlockHash, payload.BlockNumber)
	}

	return state.SetLatestExecutionPayloadHeader(payload.Header(spec))
}
