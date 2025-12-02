package types

import (
	"bytes"

	sdkmath "cosmossdk.io/math"
	hexCodec "github.com/cosmos/cosmos-sdk/codec/address"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"

	util "github.com/0xPolygon/heimdall-v2/common/hex"
)

var (
	_ sdk.Msg = &MsgValidatorJoin{}
	_ sdk.Msg = &MsgStakeUpdate{}
	_ sdk.Msg = &MsgSignerUpdate{}
	_ sdk.Msg = &MsgValidatorExit{}
)

// NewMsgValidatorJoin creates a new MsgCreateValidator instance.
func NewMsgValidatorJoin(
	from string, id uint64, activationEpoch uint64,
	amount sdkmath.Int, pubKey cryptotypes.PubKey, txHash []byte, logIndex uint64,
	blockNumber uint64, nonce uint64,
) (*MsgValidatorJoin, error) {
	return &MsgValidatorJoin{
		From:            util.FormatAddress(from),
		ValId:           id,
		ActivationEpoch: activationEpoch,
		Amount:          amount,
		SignerPubKey:    pubKey.Bytes(),
		TxHash:          txHash,
		LogIndex:        logIndex,
		BlockNumber:     blockNumber,
		Nonce:           nonce,
	}, nil
}

// ValidateBasic validates the validator join msg before it is executed
func (msg MsgValidatorJoin) ValidateBasic() error {
	if msg.ValId == uint64(0) {
		return ErrInvalidMsg.Wrapf("invalid validator id %v", msg.ValId)
	}

	ac := hexCodec.NewHexCodec()
	addrBytes, err := ac.StringToBytes(msg.From)
	if err != nil {
		return ErrInvalidMsg.Wrapf("invalid proposer %v", msg.From)
	}

	accAddr := sdk.AccAddress(addrBytes)

	if accAddr.Empty() {
		return ErrInvalidMsg.Wrapf("invalid proposer %v", msg.From)
	}

	if msg.SignerPubKey == nil {
		return ErrInvalidMsg.Wrap("signer public key can't be nil")
	}

	if bytes.Equal(msg.SignerPubKey, EmptyPubKey[:]) {
		return ErrInvalidMsg.Wrap("signer public key can't be of zero bytes")
	}

	if msg.Amount.IsZero() || msg.Amount.IsNegative() {
		return ErrInvalidMsg.Wrapf("invalid amount %v", msg.Amount)
	}

	if len(msg.TxHash) != common.HashLength {
		return ErrInvalidMsg.Wrapf("invalid tx hash %v", msg.TxHash)
	}

	return nil
}

// NewMsgStakeUpdate creates a new MsgStakeUpdate instance
func NewMsgStakeUpdate(from string, id uint64,
	newAmount sdkmath.Int, txHash []byte, logIndex uint64,
	blockNumber uint64, nonce uint64,
) (*MsgStakeUpdate, error) {
	return &MsgStakeUpdate{
		From:        util.FormatAddress(from),
		ValId:       id,
		NewAmount:   newAmount,
		TxHash:      txHash,
		LogIndex:    logIndex,
		BlockNumber: blockNumber,
		Nonce:       nonce,
	}, nil
}

// ValidateBasic validates the stake update msg before it is executed
func (msg MsgStakeUpdate) ValidateBasic() error {
	if msg.ValId == uint64(0) {
		return ErrInvalidMsg.Wrapf("invalid validator id %v", msg.ValId)
	}

	ac := hexCodec.NewHexCodec()
	addrBytes, err := ac.StringToBytes(msg.From)
	if err != nil {
		return ErrInvalidMsg.Wrapf("invalid proposer %v", msg.From)
	}

	accAddr := sdk.AccAddress(addrBytes)

	if accAddr.Empty() {
		return ErrInvalidMsg.Wrapf("invalid proposer %v", msg.From)
	}

	if msg.NewAmount.IsZero() || msg.NewAmount.IsNegative() {
		return ErrInvalidMsg.Wrapf("invalid amount %v", msg.NewAmount)
	}

	if len(msg.TxHash) != common.HashLength {
		return ErrInvalidMsg.Wrapf("invalid tx hash %v", msg.TxHash)
	}

	return nil
}

// NewMsgSignerUpdate creates a new MsgSignerUpdate instance.
func NewMsgSignerUpdate(from string, id uint64,
	pubKey []byte, txHash []byte, logIndex uint64,
	blockNumber uint64, nonce uint64,
) (*MsgSignerUpdate, error) {
	return &MsgSignerUpdate{
		From:            util.FormatAddress(from),
		ValId:           id,
		NewSignerPubKey: pubKey,
		TxHash:          txHash,
		LogIndex:        logIndex,
		BlockNumber:     blockNumber,
		Nonce:           nonce,
	}, nil
}

// ValidateBasic validates the signer update msg before it is executed
func (msg MsgSignerUpdate) ValidateBasic() error {
	if msg.ValId == uint64(0) {
		return ErrInvalidMsg.Wrapf("invalid validator id %v", msg.ValId)
	}

	ac := hexCodec.NewHexCodec()
	addrBytes, err := ac.StringToBytes(msg.From)
	if err != nil {
		return ErrInvalidMsg.Wrapf("invalid proposer %v", msg.From)
	}

	accAddr := sdk.AccAddress(addrBytes)

	if accAddr.Empty() {
		return ErrInvalidMsg.Wrapf("invalid proposer %v", msg.From)
	}

	if msg.NewSignerPubKey == nil {
		return ErrInvalidMsg.Wrap("signer public key can't be nil")
	}

	if bytes.Equal(msg.NewSignerPubKey, EmptyPubKey[:]) {
		return ErrInvalidMsg.Wrap("new signer public key can't be of zero bytes")
	}

	if len(msg.TxHash) != common.HashLength {
		return ErrInvalidMsg.Wrapf("invalid tx hash %v", msg.TxHash)
	}

	return nil
}

// NewMsgValidatorExit creates a new MsgValidatorExit instance.
func NewMsgValidatorExit(
	from string, id uint64, deactivationEpoch uint64,
	txHash []byte, logIndex uint64,
	blockNumber uint64, nonce uint64,
) (*MsgValidatorExit, error) {
	return &MsgValidatorExit{
		From:              util.FormatAddress(from),
		ValId:             id,
		DeactivationEpoch: deactivationEpoch,
		TxHash:            txHash,
		LogIndex:          logIndex,
		BlockNumber:       blockNumber,
		Nonce:             nonce,
	}, nil
}

// ValidateBasic validates the validator exit msg before it is executed
func (msg MsgValidatorExit) ValidateBasic() error {
	if msg.ValId == uint64(0) {
		return ErrInvalidMsg.Wrapf("invalid validator id %v", msg.ValId)
	}

	ac := hexCodec.NewHexCodec()
	addrBytes, err := ac.StringToBytes(msg.From)
	if err != nil {
		return ErrInvalidMsg.Wrapf("invalid proposer %v", msg.From)
	}

	accAddr := sdk.AccAddress(addrBytes)

	if accAddr.Empty() {
		return ErrInvalidMsg.Wrapf("invalid proposer %v", msg.From)
	}

	if len(msg.TxHash) != common.HashLength {
		return ErrInvalidMsg.Wrapf("invalid tx hash %v", msg.TxHash)
	}

	return nil
}
