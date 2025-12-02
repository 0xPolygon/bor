package types

import (
	"errors"

	sdkmath "cosmossdk.io/math"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"

	util "github.com/0xPolygon/heimdall-v2/common/hex"
)

var (
	_ sdk.Msg = &MsgTopupTx{}
	_ sdk.Msg = &MsgWithdrawFeeTx{}
)

// NewMsgTopupTx creates and returns a new MsgTopupTx.
func NewMsgTopupTx(proposer, user string, fee sdkmath.Int, txHash []byte, index, blockNumber uint64) *MsgTopupTx {
	return &MsgTopupTx{
		Proposer:    util.FormatAddress(proposer),
		User:        user,
		Fee:         fee,
		TxHash:      txHash,
		LogIndex:    index,
		BlockNumber: blockNumber,
	}
}

// Type returns the type of the x/topup MsgTopupTx.
func (msg MsgTopupTx) Type() string {
	return EventTypeTopup
}

// NewMsgWithdrawFeeTx creates and returns a new MsgWithdrawFeeTx.
func NewMsgWithdrawFeeTx(proposer string, amount sdkmath.Int) *MsgWithdrawFeeTx {
	return &MsgWithdrawFeeTx{
		Proposer: util.FormatAddress(proposer),
		Amount:   amount,
	}
}

// Type returns the type of the x/topup MsgWithdrawFeeTx.
func (msg MsgWithdrawFeeTx) Type() string {
	return EventTypeWithdraw
}

func (msg MsgTopupTx) ValidateBasic() error {
	if msg.Fee.IsNegative() {
		return errors.New("fee cannot be negative")
	}
	ac := addresscodec.NewHexCodec()
	_, err := ac.StringToBytes(msg.Proposer)
	if err != nil {
		return errors.New("invalid proposer")
	}
	_, err = ac.StringToBytes(msg.User)
	if err != nil {
		return errors.New("invalid user")
	}
	if len(msg.TxHash) != common.HashLength {
		return errors.New("invalid tx hash")
	}

	return nil
}

func (msg MsgWithdrawFeeTx) ValidateBasic() error {
	if msg.Amount.IsNegative() {
		return errors.New("amount cannot be negative")
	}
	ac := addresscodec.NewHexCodec()
	_, err := ac.StringToBytes(msg.Proposer)
	if err != nil {
		return errors.New("invalid proposer")
	}
	return nil
}
