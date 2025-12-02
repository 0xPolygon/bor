package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	addresscodec "cosmossdk.io/core/address"
	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	util "github.com/0xPolygon/heimdall-v2/common/hex"
	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// AddValidator adds validator indexed with address
func (k *Keeper) AddValidator(ctx context.Context, validator types.Validator) error {
	k.PanicIfSetupIsIncomplete()
	// store validator with address prefixed with the validator key as index
	err := k.validators.Set(ctx, util.FormatAddress(validator.Signer), validator)
	if err != nil {
		k.Logger(ctx).Error("error while setting the validator in store", "err", err)
		return err
	}

	k.Logger(ctx).Debug("Validator stored", "key", validator.Signer, "validator", validator.String())

	// add validator to validator ID => SignerAddress map
	k.SetValidatorIDToSignerAddr(ctx, validator.ValId, validator.Signer)

	return nil
}

// IsCurrentValidatorByAddress check if the validator is in the current validators' set by signer address
func (k *Keeper) IsCurrentValidatorByAddress(ctx context.Context, address string) (bool, error) {
	k.PanicIfSetupIsIncomplete()
	// get ack count
	ackCount, err := k.checkpointKeeper.GetAckCount(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in getting ack count", "error", err)
		return false, err
	}

	// get validator info
	validator, err := k.GetValidatorInfo(ctx, util.FormatAddress(address))
	if err != nil {
		k.Logger(ctx).Error("error in getting validator info", "error", err)
		return false, err
	}

	// check if the validator is the current validator
	return validator.IsCurrentValidator(ackCount), nil
}

// GetValidatorInfo returns the validator info given its address
func (k *Keeper) GetValidatorInfo(ctx context.Context, address string) (validator types.Validator, err error) {
	k.PanicIfSetupIsIncomplete()
	validator, err = k.validators.Get(ctx, util.FormatAddress(address))
	if err != nil {
		return validator, fmt.Errorf("error while fetching the validator from the store %w", err)
	}

	return validator, nil
}

// GetActiveValidatorInfo returns active validator
func (k *Keeper) GetActiveValidatorInfo(ctx context.Context, address string) (validator types.Validator, err error) {
	k.PanicIfSetupIsIncomplete()
	validator, err = k.GetValidatorInfo(ctx, util.FormatAddress(address))
	if err != nil {
		return validator, err
	}

	// get ack count
	ackCount, err := k.checkpointKeeper.GetAckCount(ctx)
	if err != nil {
		return validator, err
	}

	if !validator.IsCurrentValidator(ackCount) {
		return validator, errors.New("validator is not active")
	}

	return validator, nil
}

// GetCurrentValidators returns all validators who are in the validators' set
func (k *Keeper) GetCurrentValidators(ctx context.Context) (validators []types.Validator) {
	k.PanicIfSetupIsIncomplete()
	// get ack count
	ackCount, err := k.checkpointKeeper.GetAckCount(ctx)
	if err != nil {
		return nil
	}

	// Get validators and iterate through the validators' list
	k.IterateValidatorsAndApplyFn(ctx, func(validator types.Validator) error {
		// check if the validator is valid for the current epoch
		if validator.IsCurrentValidator(ackCount) {
			// append if validator is current validator
			validators = append(validators, validator)
		}
		return nil
	})

	return
}

func (k *Keeper) GetTotalPower(ctx context.Context) (totalPower int64, err error) {
	k.PanicIfSetupIsIncomplete()
	err = k.IterateCurrentValidatorsAndApplyFn(ctx, func(validator types.Validator) bool {
		totalPower += validator.GetBondedTokens().Int64()
		return false
	})
	if err != nil {
		return 0, err
	}

	return
}

// GetSpanEligibleValidators returns current validators who are not getting deactivated between next span
func (k *Keeper) GetSpanEligibleValidators(ctx context.Context) (validators []types.Validator) {
	k.PanicIfSetupIsIncomplete()
	// get ack count
	ackCount, err := k.checkpointKeeper.GetAckCount(ctx)
	if err != nil {
		return nil
	}

	// Get validators and iterate through the validators' list
	k.IterateValidatorsAndApplyFn(ctx, func(validator types.Validator) error {
		// check if the validator is valid for the current epoch, and endEpoch is not set.
		if validator.EndEpoch == 0 && validator.IsCurrentValidator(ackCount) {
			// append if validator is current validator
			validators = append(validators, validator)
		}
		return nil
	})

	return
}

// GetAllValidators returns all validators
func (k *Keeper) GetAllValidators(ctx context.Context) (validators []*types.Validator) {
	k.PanicIfSetupIsIncomplete()
	// iterate through validators and create the validator update array
	k.IterateValidatorsAndApplyFn(ctx, func(validator types.Validator) error {
		// append to the list of validatorUpdates
		validators = append(validators, &validator)
		return nil
	})

	return
}

// IterateValidatorsAndApplyFn iterate validators and apply the given function.
func (k *Keeper) IterateValidatorsAndApplyFn(ctx context.Context, f func(validator types.Validator) error) {
	k.PanicIfSetupIsIncomplete()
	// get validator iterator
	iterator, err := k.validators.Iterate(ctx, nil)

	// get validator iterator
	defer func() {
		err := iterator.Close()
		if err != nil {
			k.Logger(ctx).Error("error in closing the iterator", "error", err)
		}
	}()

	if err != nil {
		k.Logger(ctx).Error("error in getting iterator for validators")
		return
	}

	// loop through all the validators
	for ; iterator.Valid(); iterator.Next() {
		// unmarshall validator
		validator, err := iterator.Value()
		if err != nil {
			k.Logger(ctx).Error("error in getting validator from iterator", "err", err)
			return
		}

		// call function and return if required
		if err = f(validator); err != nil {
			return
		}
	}
}

// UpdateSigner updates validator fields in store
func (k *Keeper) UpdateSigner(ctx context.Context, newSigner string, newPubKey []byte, prevSigner string) error {
	k.PanicIfSetupIsIncomplete()
	// get old validator from state and make power 0
	validator, err := k.GetValidatorInfo(ctx, util.FormatAddress(prevSigner))
	if err != nil {
		k.Logger(ctx).Error("unable to fetch validator from store")
		return err
	}

	// copy power to reassign below
	validatorPower := validator.VotingPower
	validator.VotingPower = 0

	// update validator
	if err := k.AddValidator(ctx, validator); err != nil {
		k.Logger(ctx).Error("error in adding validator", "error", err)
		return err
	}

	// update signer in prev validator
	validator.Signer = newSigner
	validator.PubKey = newPubKey
	validator.VotingPower = validatorPower

	// add updated validator to store with the new key
	if err = k.AddValidator(ctx, validator); err != nil {
		k.Logger(ctx).Error("error in adding validator", "error", err)
		return err
	}

	return nil
}

// UpdateValidatorSetInStore adds validator set to store
func (k *Keeper) UpdateValidatorSetInStore(ctx context.Context, newValidatorSet types.ValidatorSet) error {
	k.PanicIfSetupIsIncomplete()
	// set validator set with CurrentValidatorSetKey as the key in store
	err := k.validatorSet.Set(ctx, types.CurrentValidatorSetKey, newValidatorSet)
	if err != nil {
		k.Logger(ctx).Error("error in setting the current validator set in store", "err", err)
		return err
	}

	return nil
}

// GetValidatorSet returns current validator set from store
func (k *Keeper) GetValidatorSet(ctx context.Context) (validatorSet types.ValidatorSet, err error) {
	k.PanicIfSetupIsIncomplete()
	// get the current validator set from store
	validatorSet, err = k.validatorSet.Get(ctx, types.CurrentValidatorSetKey)
	if err != nil {
		k.Logger(ctx).Error("error in fetching current validator set from store", "error", err)
		return validatorSet, err
	}

	// return validator set
	return validatorSet, nil
}

// UpdatePreviousBlockValidatorSetInStore adds the previous block's validator set to store
func (k *Keeper) UpdatePreviousBlockValidatorSetInStore(ctx context.Context, newValidatorSet types.ValidatorSet) error {
	k.PanicIfSetupIsIncomplete()
	// set validator set with CurrentValidatorSetKey as the key in store
	err := k.validatorSet.Set(ctx, types.PreviousBlockValidatorSetKey, newValidatorSet)
	if err != nil {
		k.Logger(ctx).Error("error in setting the previous block's validator set in store", "err", err)
		return err
	}

	return nil
}

// GetPreviousBlockValidatorSet returns the previous block's validator set from store
func (k *Keeper) GetPreviousBlockValidatorSet(ctx context.Context) (validatorSet types.ValidatorSet, err error) {
	k.PanicIfSetupIsIncomplete()
	// get the current validator set from store
	validatorSet, err = k.validatorSet.Get(ctx, types.PreviousBlockValidatorSetKey)
	if err != nil {
		k.Logger(ctx).Error("error in fetching the previous block's validator set from store", "error", err)
		return validatorSet, err
	}

	// return validator set
	return validatorSet, nil
}

// UpdatePenultimateBlockValidatorSetInStore adds the validator set from 2 blocks ago to store
func (k *Keeper) UpdatePenultimateBlockValidatorSetInStore(ctx context.Context, newValidatorSet types.ValidatorSet) error {
	k.PanicIfSetupIsIncomplete()
	// set validator set with PenultimateBlockValidatorSetKey as the key in store
	err := k.validatorSet.Set(ctx, types.PenultimateBlockValidatorSetKey, newValidatorSet)
	if err != nil {
		k.Logger(ctx).Error("error in setting the validator set from 2 blocks ago in store", "err", err)
		return err
	}

	return nil
}

// GetPenultimateBlockValidatorSet returns the validator set from 2 blocks ago from store
func (k *Keeper) GetPenultimateBlockValidatorSet(ctx context.Context) (validatorSet types.ValidatorSet, err error) {
	k.PanicIfSetupIsIncomplete()
	// get the validator set from 2 blocks ago from store
	validatorSet, err = k.validatorSet.Get(ctx, types.PenultimateBlockValidatorSetKey)
	if err != nil {
		k.Logger(ctx).Error("error in fetching the validator set from 2 blocks ago from store", "error", err)
		return validatorSet, err
	}

	// return validator set
	return validatorSet, nil
}

// IncrementAccum increments accum for validator set by n times and replace the validator set in store
func (k *Keeper) IncrementAccum(ctx context.Context, times int) error {
	k.PanicIfSetupIsIncomplete()
	// get the validator set
	validatorSet, err := k.GetValidatorSet(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in fetching validator set from store", "error", err)
		return err

	}
	// increment accum
	validatorSet.IncrementProposerPriority(times)

	if err = k.UpdateValidatorSetInStore(ctx, validatorSet); err != nil {
		k.Logger(ctx).Error("error in updating validator set in store", "error", err)
		return err
	}

	return nil
}

// GetNextProposer returns next proposer
func (k *Keeper) GetNextProposer(ctx context.Context) *types.Validator {
	k.PanicIfSetupIsIncomplete()
	// get the validator set
	validatorSet, err := k.GetValidatorSet(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in fetching the validator set from database", "error", err)
		return nil
	}

	// Increment accum in copy
	copiedValidatorSet := validatorSet.CopyIncrementProposerPriority(1)

	// get signer address for the next signer
	return copiedValidatorSet.GetProposer()
}

// GetCurrentProposer returns the current proposer from the validator set
func (k *Keeper) GetCurrentProposer(ctx context.Context) *types.Validator {
	k.PanicIfSetupIsIncomplete()
	// get the validator set
	validatorSet, err := k.GetValidatorSet(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in fetching the validator set from database", "error", err)
		return nil
	}

	return validatorSet.GetProposer()
}

// SetValidatorIDToSignerAddr sets mapping for validator ID to signer address
func (k *Keeper) SetValidatorIDToSignerAddr(ctx context.Context, valID uint64, signerAddr string) {
	err := k.signer.Set(ctx, valID, util.FormatAddress(signerAddr))
	if err != nil {
		k.Logger(ctx).Error("key or value is nil", "error", err)
	}
}

// GetSignerFromValidatorID gets the signer address from the validator id
func (k *Keeper) GetSignerFromValidatorID(ctx context.Context, valID uint64) (string, error) {
	k.PanicIfSetupIsIncomplete()
	signer, err := k.signer.Get(ctx, valID)
	if err != nil {
		k.Logger(ctx).Error("error while getting fetching signer address", "error", err)
		return "", err
	}

	// return address from bytes
	return signer, nil
}

// DoesValIdExist checks if validator ID exists in store
func (k *Keeper) DoesValIdExist(ctx context.Context, valID uint64) (bool, error) {
	k.PanicIfSetupIsIncomplete()
	return k.signer.Has(ctx, valID)
}

// GetValidatorFromValID returns signer from validator ID
func (k *Keeper) GetValidatorFromValID(ctx context.Context, valID uint64) (validator types.Validator, err error) {
	k.PanicIfSetupIsIncomplete()
	signerAddr, err := k.GetSignerFromValidatorID(ctx, valID)
	if err != nil {
		return validator, err
	}

	// query for validator signer address
	validator, err = k.GetValidatorInfo(ctx, signerAddr)
	if err != nil {
		return validator, err
	}

	return validator, nil
}

// GetLastUpdated get last updated at for validator
func (k *Keeper) GetLastUpdated(ctx context.Context, valID uint64) (updatedAt string, err error) {
	k.PanicIfSetupIsIncomplete()
	// get validator
	validator, err := k.GetValidatorFromValID(ctx, valID)
	if err != nil {
		return "", err
	}

	return validator.LastUpdated, nil
}

// SetStakingSequence sets staking sequence
func (k *Keeper) SetStakingSequence(ctx context.Context, sequence string) error {
	k.PanicIfSetupIsIncomplete()
	return k.sequences.Set(ctx, sequence, true)
}

// HasStakingSequence checks if the staking sequence already exists
func (k *Keeper) HasStakingSequence(ctx context.Context, sequence string) bool {
	k.PanicIfSetupIsIncomplete()
	res, err := k.sequences.Has(ctx, sequence)
	if err != nil {
		k.Logger(ctx).Error("error while checking for the existence of staking key in store", "error", err)
		return false
	}

	return res
}

// GetStakingSequences returns all the sequences appended together
func (k *Keeper) GetStakingSequences(ctx context.Context) (sequences []string, err error) {
	k.PanicIfSetupIsIncomplete()
	err = k.IterateStakingSequencesAndApplyFn(ctx, func(sequence string) error {
		sequences = append(sequences, sequence)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return
}

// IterateStakingSequencesAndApplyFn iterates staking sequences and applies the given function.
func (k *Keeper) IterateStakingSequencesAndApplyFn(ctx context.Context, f func(sequence string) error) (e error) {
	k.PanicIfSetupIsIncomplete()
	// get staking sequence iterator
	iterator, err := k.sequences.Iterate(ctx, nil)
	defer func(iterator collections.Iterator[string, bool]) {
		err := iterator.Close()
		if err != nil {
			k.Logger(ctx).Error("error in closing the iterator", "error", err)
			e = err
		}
	}(iterator)

	if err != nil {
		k.Logger(ctx).Error("error in getting iterator for validators")
		return
	}

	// loop through validators to get valid value
	for ; iterator.Valid(); iterator.Next() {
		sequence, err := iterator.Key()
		if err != nil {
			k.Logger(ctx).Error("error in getting key value", "err", err)
		}

		// call function and return if required
		if err := f(sequence); err != nil {
			return
		}
	}

	return
}

// GetValIdFromAddress returns a validator's id given its address string
func (k *Keeper) GetValIdFromAddress(ctx context.Context, address string) (uint64, error) {
	k.PanicIfSetupIsIncomplete()
	// get ack count
	ackCount, err := k.checkpointKeeper.GetAckCount(ctx)
	if err != nil {
		return 0, err
	}

	validator, err := k.GetValidatorInfo(ctx, util.FormatAddress(address))
	if err != nil {
		return 0, err
	}

	// check if the validator is the current validator
	if !validator.IsCurrentValidator(ackCount) {
		return 0, errors.New("address not found in current validator set")
	}

	return validator.ValId, nil
}

// IterateCurrentValidatorsAndApplyFn iterate through current validators
func (k Keeper) IterateCurrentValidatorsAndApplyFn(ctx context.Context, f func(validator types.Validator) bool) error {
	k.PanicIfSetupIsIncomplete()
	currentValidatorSet, err := k.GetValidatorSet(ctx)
	if err != nil {
		k.Logger(ctx).Error("error in fetching the validator set from database", "error", err)
		return err
	}

	for _, v := range currentValidatorSet.Validators {
		if stop := f(*v); stop {
			return nil
		}
	}
	return nil
}

// ValidatorAddressCodec return the validator address codec
func (k *Keeper) ValidatorAddressCodec() addresscodec.Codec {
	return k.validatorAddressCodec
}

func (k Keeper) ApplyAndReturnValidatorSetUpdates(ctx context.Context) (updates []abci.ValidatorUpdate, err error) {
	var cmtValUpdates []abci.ValidatorUpdate
	currentValidatorSet, err := k.GetValidatorSet(ctx)
	if err != nil {
		k.Logger(ctx).Error("error while calling the GetValidatorSet fn", "err", err)
		return nil, err
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if sdkCtx.BlockHeight() >= helper.GetTallyFixHeight()-1 {
		// save the current previous block's validator set as the 2-blocks-ago validator set
		previousValidatorSet, err := k.GetPreviousBlockValidatorSet(ctx)
		if err != nil {
			return nil, err
		}

		// update the penultimate validator set in store
		err = k.UpdatePenultimateBlockValidatorSetInStore(ctx, previousValidatorSet)
		if err != nil {
			k.Logger(ctx).Error("unable to set validator set from 2 blocks ago in state", "error", err)
			return nil, err
		}

	}

	// save the current validator set as the previous block's validator set
	err = k.UpdatePreviousBlockValidatorSetInStore(ctx, currentValidatorSet)
	if err != nil {
		k.Logger(ctx).Error("unable to set previous block's validator set in state", "error", err)
		return nil, err
	}

	allValidators := k.GetAllValidators(ctx)
	ackCount, err := k.checkpointKeeper.GetAckCount(ctx)
	if err != nil {
		return cmtValUpdates, err
	}
	// get validator updates
	setUpdates := types.GetUpdatedValidators(
		&currentValidatorSet, // pointer to the current validator set -- UpdateValidators will modify it
		allValidators,        // All validators
		ackCount,             // ack count
	)

	if len(setUpdates) > 0 {
		// create the new validator set
		if err = currentValidatorSet.UpdateWithChangeSet(setUpdates); err != nil {
			// return error
			k.Logger(ctx).Error("unable to update current validator set", "error", err)
			return nil, err
		}

		// save set in store
		if err = k.UpdateValidatorSetInStore(ctx, currentValidatorSet); err != nil {
			// return with nothing
			k.Logger(ctx).Error("unable to update current validator set in state", "error", err)
			return nil, err
		}

		currentValidatorSet.IncrementProposerPriority(1)

		// convert updates from map to array
		for _, v := range setUpdates {
			cmtProtoPk, err := v.CmtConsPublicKey()
			if err != nil {
				k.Logger(ctx).Error("error while getting the public key for validator, skipping it", "error", err, "validatorId", v.ValId)
				continue
			}

			cmtValUpdates = append(cmtValUpdates, abci.ValidatorUpdate{
				Power:  v.VotingPower,
				PubKey: cmtProtoPk,
			})
		}
	}

	return cmtValUpdates, nil
}
