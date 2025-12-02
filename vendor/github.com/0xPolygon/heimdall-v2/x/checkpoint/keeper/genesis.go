package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
)

// InitGenesis sets initial state for checkpoint module
func (k Keeper) InitGenesis(ctx context.Context, data *types.GenesisState) {
	err := k.SetParams(ctx, data.Params)
	if err != nil {
		k.Logger(ctx).Error("error in setting checkpoint params during init genesis", "error", err)
		panic(err)
	}

	// Set last no-ack
	if data.LastNoAck > 0 {
		err = k.SetLastNoAck(ctx, data.LastNoAck)
		if err != nil {
			k.Logger(ctx).Error("error in setting last no ack during init genesis", "error", err)
			panic(err)
		}
	}

	// Add finalized checkpoints to the state
	if len(data.Checkpoints) != 0 {
		// check if we are provided all the checkpoints
		if int(data.AckCount) != len(data.Checkpoints) {
			k.Logger(ctx).Error("incorrect state in state-dump", "ack count", data.AckCount, "checkpoints length", data.Checkpoints)
			panic(err)
		}
		// sort checkpoints before loading to state
		data.Checkpoints = types.SortCheckpoints(data.Checkpoints)
		// load checkpoints to state
		for i, checkpoint := range data.Checkpoints {
			// create the checkpoint message for validation
			msg := types.NewMsgCheckpointBlock(checkpoint.Proposer,
				checkpoint.StartBlock,
				checkpoint.EndBlock,
				checkpoint.RootHash,
				nil, // account root hash is not used to validate checkpoint
				checkpoint.BorChainId)

			if err := msg.ValidateBasic(); err != nil {
				k.Logger(ctx).Error("error in validating checkpoint message while InitGenesis", "error", err)
				panic(err)
			}
			checkpointIndex := uint64(i) + 1
			checkpoint.Id = checkpointIndex
			if err = k.AddCheckpoint(ctx, checkpoint); err != nil {
				k.Logger(ctx).Error("error while adding the checkpoint to store",
					"checkpointIndex", checkpointIndex,
					"checkpoint", checkpoint.String(),
					"error", err)
			}
		}
	}

	// add checkpoint in the buffer
	if data.BufferedCheckpoint != nil {
		if err := k.SetCheckpointBuffer(ctx, *data.BufferedCheckpoint); err != nil {
			k.Logger(ctx).Error("error while setting the checkpoint in buffer", "error", err)
		}
	}

	// set initial ack count
	err = k.UpdateAckCountWithValue(ctx, data.AckCount)
	if err != nil {
		k.Logger(ctx).Error("error in updating the ack count value in store", "error", err)
		panic(err)
	}

	// set checkpoint signatures
	if len(data.CheckpointSignatures.Signatures) > 0 {
		for _, s := range data.CheckpointSignatures.Signatures {
			if err = address.VerifyAddressFormat(s.ValidatorAddress); err != nil {
				k.Logger(ctx).Error("error in validating checkpoint signature address", "error", err)
				panic(err)
			}

			if len(s.Signature) == 0 {
				panic("checkpoint signature cannot be empty")
			}
		}
		if err = k.SetCheckpointSignatures(ctx, data.CheckpointSignatures); err != nil {
			k.Logger(ctx).Error("error in setting checkpoint signatures", "error", err)
			panic(err)
		}
	} else {
		// if checkpoint signatures are empty, set it to nil
		if err = k.SetCheckpointSignatures(ctx, types.CheckpointSignatures{Signatures: make([]types.CheckpointSignature, 0)}); err != nil {
			k.Logger(ctx).Error("error in setting checkpoint signatures", "error", err)
			panic(err)
		}
	}

	// set checkpoint signatures txhash
	if data.CheckpointSignaturesTxhash != "" {
		if err = k.SetCheckpointSignaturesTxHash(ctx, data.CheckpointSignaturesTxhash); err != nil {
			k.Logger(ctx).Error("error in setting checkpoint signatures txhash", "error", err)
			panic(err)
		}
	} else {
		// if checkpoint signatures txhash are empty, set it to nil
		if err = k.SetCheckpointSignaturesTxHash(ctx, ""); err != nil {
			k.Logger(ctx).Error("error in setting checkpoint signatures txhash", "error", err)
			panic(err)
		}
	}
}

// ExportGenesis returns a GenesisState for a given context and keeper of
// checkpoint module
func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in getting checkpoint params in export genesis call", "error", err)
		return nil
	}

	checkpoints, err := k.GetCheckpoints(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in getting checkpoints in export genesis call", "error", err)
		return nil
	}

	bufferedCheckpoint, _ := k.GetCheckpointFromBuffer(ctx)
	lastNoAck, err := k.GetLastNoAck(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in getting last no ack in export genesis call", "error", err)
		return nil
	}

	ackCount, err := k.GetAckCount(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in getting ack count in export genesis call", "error", err)
		return nil
	}

	checkpointSignatures, err := k.GetCheckpointSignatures(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in getting checkpoint signatures in export genesis call", "error", err)
		return nil
	}

	checkpointSignaturesTxhash, err := k.GetCheckpointSignaturesTxHash(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in getting checkpoint signatures txhash in export genesis call", "error", err)
		return nil
	}

	return &types.GenesisState{
		Params:                     params,
		BufferedCheckpoint:         &bufferedCheckpoint,
		LastNoAck:                  lastNoAck,
		AckCount:                   ackCount,
		Checkpoints:                types.SortCheckpoints(checkpoints),
		CheckpointSignatures:       checkpointSignatures,
		CheckpointSignaturesTxhash: checkpointSignaturesTxhash,
	}
}
