package keeper

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	errorsmod "cosmossdk.io/errors"
	addrCodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"

	util "github.com/0xPolygon/heimdall-v2/common/hex"
	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/metrics/api"
	"github.com/0xPolygon/heimdall-v2/x/stake/types"
)

type msgServer struct {
	k *Keeper
}

// NewMsgServerImpl returns an implementation of the staking MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper *Keeper) types.MsgServer {
	return &msgServer{k: keeper}
}

// ValidatorJoin defines a method for new validator's joining
func (m msgServer) ValidatorJoin(ctx context.Context, msg *types.MsgValidatorJoin) (*types.MsgValidatorJoinResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeTransactionMetric(api.ValidatorJoinMethod, startTime, &err)

	m.k.Logger(ctx).Debug("✅ Validating validator join msg",
		"validatorId", msg.ValId,
		"activationEpoch", msg.ActivationEpoch,
		"amount", msg.Amount,
		"SignerPubKey", common.Bytes2Hex(msg.SignerPubKey),
		"txHash", msg.TxHash,
		"logIndex", msg.LogIndex,
		"blockNumber", msg.BlockNumber,
	)

	err = msg.ValidateBasic()
	if err != nil {
		m.k.Logger(ctx).Error("failed to validate msg", "error", err)
		return nil, errorsmod.Wrap(types.ErrInvalidMsg, "failed to validate msg")
	}

	// Generate PubKey from PubKey in message and signer
	pubKey := msg.SignerPubKey
	pk := secp256k1.PubKey{Key: pubKey}

	if pk.Type() != types.Secp256k1Type {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "pub key is invalid")
	}

	signer, err := addrCodec.NewHexCodec().BytesToString(pk.Address())
	if err != nil {
		m.k.Logger(ctx).Error("signer is invalid", "error", err)
		return nil, errorsmod.Wrap(types.ErrInvalidMsg, "signer is invalid")
	}

	// check if the validator has been validator before
	if ok, err := m.k.DoesValIdExist(ctx, msg.ValId); ok {
		m.k.Logger(ctx).Error("validator has been a validator before, hence cannot join with same id", "validatorId", msg.ValId, "err", err)
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "validator corresponding to the val id already exists in store")
	}

	signer = util.FormatAddress(signer)
	// get validator by signer
	checkVal, err := m.k.GetValidatorInfo(ctx, signer)
	// not returning error if validator not found because it is a new validator
	if err == nil && strings.Compare(util.FormatAddress(checkVal.Signer), signer) == 0 {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, fmt.Sprintf("validator %s already exists", signer))
	}

	// validate voting power
	_, err = helper.GetPowerFromAmount(msg.Amount.BigInt())
	if err != nil {
		return nil, errorsmod.Wrap(types.ErrInvalidMsg, fmt.Sprintf("Invalid amount %s for validator %d", msg.Amount, msg.ValId))
	}

	// add the sequence
	blockNumber := new(big.Int).SetUint64(msg.BlockNumber)
	sequence := new(big.Int).Mul(blockNumber, big.NewInt(types.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(msg.LogIndex))

	// check if the event has already been processed
	if m.k.HasStakingSequence(ctx, sequence.String()) {
		m.k.Logger(ctx).Error("Event already processed", "sequence", sequence.String())
		return nil, errors.Wrapf(sdkerrors.ErrConflict, "old events are not allowed")
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sdkCtx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeValidatorJoin,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.AttributeValueCategory),
			sdk.NewAttribute(types.AttributeKeyValidatorID, strconv.FormatUint(msg.ValId, 10)),
			sdk.NewAttribute(types.AttributeKeyValidatorNonce, strconv.FormatUint(msg.Nonce, 10)),
		),
	})

	return &types.MsgValidatorJoinResponse{}, nil
}

// StakeUpdate defines a method for updating the stake of a validator
func (m msgServer) StakeUpdate(ctx context.Context, msg *types.MsgStakeUpdate) (*types.MsgStakeUpdateResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeTransactionMetric(api.StakeUpdateMethod, startTime, &err)

	m.k.Logger(ctx).Debug("✅ Validating stake update msg",
		"validatorID", msg.ValId,
		"newAmount", msg.NewAmount,
		"txHash", msg.TxHash,
		"logIndex", msg.LogIndex,
		"blockNumber", msg.BlockNumber,
	)

	err = msg.ValidateBasic()
	if err != nil {
		m.k.Logger(ctx).Error("failed to validate msg", "error", err)
		return nil, errorsmod.Wrap(types.ErrInvalidMsg, "failed to validate msg")
	}

	// pull validator from store
	_, err = m.k.GetValidatorFromValID(ctx, msg.ValId)
	if err != nil {
		m.k.Logger(ctx).Error("failed to fetch validator from store", "validatorId", msg.ValId, "error", err)
		return nil, errorsmod.Wrap(types.ErrNoValidator, "failed to fetch validator from store")
	}

	// add the sequence
	blockNumber := new(big.Int).SetUint64(msg.BlockNumber)
	sequence := new(big.Int).Mul(blockNumber, big.NewInt(types.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(msg.LogIndex))

	// check if the event has already been processed
	if m.k.HasStakingSequence(ctx, sequence.String()) {
		m.k.Logger(ctx).Error("Event already processed", "sequence", sequence.String())
		return nil, errors.Wrapf(sdkerrors.ErrConflict, "old events are not allowed")
	}

	// set validator amount
	_, err = helper.GetPowerFromAmount(msg.NewAmount.BigInt())
	if err != nil {
		return nil, errorsmod.Wrap(types.ErrInvalidMsg, fmt.Sprintf("invalid amount %s for validator %d", msg.NewAmount, msg.ValId))
	}

	return &types.MsgStakeUpdateResponse{}, nil
}

// SignerUpdate defines a method for updating the validator's signer
func (m msgServer) SignerUpdate(ctx context.Context, msg *types.MsgSignerUpdate) (*types.MsgSignerUpdateResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeTransactionMetric(api.SignerUpdateMethod, startTime, &err)

	m.k.Logger(ctx).Debug("✅ Validating signer update msg",
		"validatorID", msg.ValId,
		"NewSignerPubKey", common.Bytes2Hex(msg.NewSignerPubKey),
		"txHash", msg.TxHash,
		"logIndex", msg.LogIndex,
		"blockNumber", msg.BlockNumber,
	)

	err = msg.ValidateBasic()
	if err != nil {
		m.k.Logger(ctx).Error("failed to validate msg", "error", err)
		return nil, errorsmod.Wrap(types.ErrInvalidMsg, "failed to validate msg")
	}

	// Generate PubKey from PubKey in message and signer
	pubKey := msg.NewSignerPubKey
	pk := &secp256k1.PubKey{Key: pubKey}

	if pk.Type() != types.Secp256k1Type {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "pub key is invalid")
	}

	newSigner, err := addrCodec.NewHexCodec().BytesToString(pk.Address())
	if err != nil {
		m.k.Logger(ctx).Error("new signer is invalid", "error", err, "newSigner", newSigner)
		return nil, errorsmod.Wrap(types.ErrInvalidMsg, "new signer is invalid")
	}

	// pull validator from store
	validator, err := m.k.GetValidatorFromValID(ctx, msg.ValId)
	if err != nil {
		m.k.Logger(ctx).Error("Fetching of validator from store failed", "validatorId", msg.ValId, "error", err)
		return nil, errorsmod.Wrap(types.ErrNoValidator, "Fetching of validator from store failed")
	}

	// make oldSigner address compatible with newSigner address
	oldSigner := util.FormatAddress(validator.Signer)

	// add the sequence
	blockNumber := new(big.Int).SetUint64(msg.BlockNumber)
	sequence := new(big.Int).Mul(blockNumber, big.NewInt(types.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(msg.LogIndex))

	// check if the event has already been processed
	if m.k.HasStakingSequence(ctx, sequence.String()) {
		m.k.Logger(ctx).Error("Event already processed", "sequence", sequence.String())
		return nil, errors.Wrapf(sdkerrors.ErrConflict, "old events are not allowed")
	}

	// check if the new signer address is the same as the existing signer
	if newSigner == oldSigner {
		// No signer change
		m.k.Logger(ctx).Error("new signer is the same as old signer")
		return nil, errorsmod.Wrap(types.ErrNoSignerChange, "newSigner same as oldSigner")

	}

	return &types.MsgSignerUpdateResponse{}, nil
}

// ValidatorExit defines a method for exiting the validator from the validator set
func (m msgServer) ValidatorExit(ctx context.Context, msg *types.MsgValidatorExit) (*types.MsgValidatorExitResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeTransactionMetric(api.ValidatorExitMethod, startTime, &err)

	m.k.Logger(ctx).Debug("✅ Validating validator exit msg",
		"validatorID", msg.ValId,
		"deactivationEpoch", msg.DeactivationEpoch,
		"txHash", msg.TxHash,
		"logIndex", msg.LogIndex,
		"blockNumber", msg.BlockNumber,
	)

	err = msg.ValidateBasic()
	if err != nil {
		m.k.Logger(ctx).Error("failed to validate msg", "error", err)
		return nil, errorsmod.Wrap(types.ErrInvalidMsg, "failed to validate msg")
	}

	validator, err := m.k.GetValidatorFromValID(ctx, msg.ValId)
	if err != nil {
		m.k.Logger(ctx).Error("failed to fetch validator from store", "validatorID", msg.ValId, "error", err)
		return nil, errorsmod.Wrap(types.ErrNoValidator, "failed to fetch validator from store")
	}

	m.k.Logger(ctx).Debug("validator in store", "validator", validator)
	// check if the validator deactivation period is set
	if validator.EndEpoch != 0 {
		m.k.Logger(ctx).Error("validator already unBonded")
		return nil, errorsmod.Wrap(types.ErrValUnBonded, "validator already unBonded")
	}

	// add the sequence
	blockNumber := new(big.Int).SetUint64(msg.BlockNumber)
	sequence := new(big.Int).Mul(blockNumber, big.NewInt(types.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(msg.LogIndex))

	// check if the event has already been processed
	if m.k.HasStakingSequence(ctx, sequence.String()) {
		m.k.Logger(ctx).Error("Event already processed", "sequence", sequence.String())
		return nil, errors.Wrapf(sdkerrors.ErrConflict, "old events are not allowed")
	}

	return &types.MsgValidatorExitResponse{}, nil
}

func recordStakeTransactionMetric(method string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.StakeSubsystem, method, api.TxType, success, start)
}
