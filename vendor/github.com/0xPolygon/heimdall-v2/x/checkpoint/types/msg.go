package types

import (
	"bytes"
	"errors"
	"math/big"
	"strconv"

	addressCodec "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/ethereum/go-ethereum/common"

	util "github.com/0xPolygon/heimdall-v2/common/hex"
	"github.com/0xPolygon/heimdall-v2/types"
)

var (
	_ sdk.Msg = &MsgCheckpoint{}
	_ sdk.Msg = &MsgCpAck{}
	_ sdk.Msg = &MsgCpNoAck{}
)

// NewMsgCheckpointBlock creates the new checkpoint message using the mentioned arguments
func NewMsgCheckpointBlock(
	proposer string,
	startBlock uint64,
	endBlock uint64,
	rootHash []byte,
	accountRootHash []byte,
	borChainID string,
) *MsgCheckpoint {
	return &MsgCheckpoint{
		Proposer:        util.FormatAddress(proposer),
		StartBlock:      startBlock,
		EndBlock:        endBlock,
		RootHash:        rootHash,
		AccountRootHash: accountRootHash,
		BorChainId:      borChainID,
	}
}

func (msg MsgCheckpoint) ValidateBasic() error {
	if _, err := strconv.ParseUint(msg.BorChainId, 10, 64); err != nil {
		return ErrInvalidMsg.Wrapf("Invalid bor chain id %s", msg.BorChainId)
	}

	if bytes.Equal(msg.RootHash, common.Hash{}.Bytes()) {
		return ErrInvalidMsg.Wrapf("Invalid roothash %v", string(msg.RootHash))
	}

	if len(msg.RootHash) != common.HashLength {
		return ErrInvalidMsg.Wrapf("Invalid roothash length %v", len(msg.RootHash))
	}

	ac := addressCodec.NewHexCodec()
	addrBytes, err := ac.StringToBytes(msg.Proposer)
	if err != nil {
		return ErrInvalidMsg.Wrapf("Invalid proposer %s", msg.Proposer)
	}

	accAddr := sdk.AccAddress(addrBytes)

	if accAddr.Empty() {
		return ErrInvalidMsg.Wrapf("Invalid proposer %s", msg.Proposer)
	}

	if msg.StartBlock >= msg.EndBlock || msg.EndBlock == 0 {
		return ErrInvalidMsg.Wrapf("End should be greater than to start block start block=%d,end block=%d", msg.StartBlock, msg.EndBlock)
	}

	return nil
}

// GetSideSignBytes returns side sign bytes
func (msg MsgCheckpoint) GetSideSignBytes() []byte {
	// keccak256(abi.encoded(proposer, startBlock, endBlock, rootHash, accountRootHash, bor chain id))
	borChainID, _ := strconv.ParseUint(msg.BorChainId, 10, 64)

	ac := addressCodec.NewHexCodec()
	proposerBytes, err := ac.StringToBytes(msg.Proposer)
	if err != nil {
		panic(errors.New("invalid proposer while getting side sign bytes for checkpoint msg"))
	}

	return types.AppendBytes32(
		proposerBytes,
		new(big.Int).SetUint64(msg.StartBlock).Bytes(),
		new(big.Int).SetUint64(msg.EndBlock).Bytes(),
		msg.RootHash,
		msg.AccountRootHash,
		new(big.Int).SetUint64(borChainID).Bytes(),
	)
}

// UnpackCheckpointSideSignBytes reconstructs a MsgCheckpoint from the provided byte slice
func UnpackCheckpointSideSignBytes(data []byte) (*MsgCheckpoint, error) {
	chunkSize := 32
	if len(data) != chunkSize*6 { // 6 fields, each padded to 32 bytes
		return nil, errors.New("invalid data length")
	}

	offset := 0

	// Extract proposerBytes (address padded to 32 bytes)
	proposerBytes := data[offset : offset+chunkSize]
	offset += chunkSize

	proposerAddrBytes := proposerBytes[12:] // take the last 20 bytes
	ac := addressCodec.NewHexCodec()
	proposer, err := ac.BytesToString(proposerAddrBytes)
	if err != nil {
		return nil, err
	}

	// Extract StartBlock
	startBlockBytes := data[offset : offset+chunkSize]
	offset += chunkSize
	startBlock := new(big.Int).SetBytes(startBlockBytes).Uint64()

	// Extract EndBlock
	endBlockBytes := data[offset : offset+chunkSize]
	offset += chunkSize
	endBlock := new(big.Int).SetBytes(endBlockBytes).Uint64()

	// Extract RootHash
	rootHash := data[offset : offset+chunkSize]
	offset += chunkSize

	// Extract AccountRootHash
	accountRootHash := data[offset : offset+chunkSize]
	offset += chunkSize

	// Extract BorChainId
	borChainIDBytes := data[offset : offset+chunkSize]
	borChainIDUint := new(big.Int).SetBytes(borChainIDBytes).Uint64()
	borChainIDStr := strconv.FormatUint(borChainIDUint, 10)

	// Construct MsgCheckpoint
	msg := &MsgCheckpoint{
		Proposer:        proposer,
		StartBlock:      startBlock,
		EndBlock:        endBlock,
		RootHash:        rootHash,
		AccountRootHash: accountRootHash,
		BorChainId:      borChainIDStr,
	}

	return msg, nil
}

var _ sdk.Msg = &MsgCpAck{}

func NewMsgCpAck(
	from string,
	number uint64,
	proposer string,
	startBlock uint64,
	endBlock uint64,
	rootHash []byte,
) MsgCpAck {
	return MsgCpAck{
		From:       util.FormatAddress(from),
		Number:     number,
		Proposer:   proposer,
		StartBlock: startBlock,
		EndBlock:   endBlock,
		RootHash:   rootHash,
	}
}

// ValidateBasic validate basic
func (msg MsgCpAck) ValidateBasic() error {
	ac := addressCodec.NewHexCodec()

	// Validate sender (msg.From)
	fromBytes, err := ac.StringToBytes(msg.From)
	if err != nil {
		return ErrInvalidMsg.Wrapf("Invalid sender %s", msg.From)
	}

	fromAccAddr := sdk.AccAddress(fromBytes)
	if fromAccAddr.Empty() {
		return ErrInvalidMsg.Wrapf("Invalid sender %s", msg.From)
	}

	// Validate proposer (msg.Proposer)
	proposerBytes, err := ac.StringToBytes(msg.Proposer)
	if err != nil {
		return ErrInvalidMsg.Wrapf("Invalid proposer %s", msg.Proposer)
	}

	proposerAccAddr := sdk.AccAddress(proposerBytes)
	if proposerAccAddr.Empty() {
		return ErrInvalidMsg.Wrapf("Invalid proposer %s", msg.Proposer)
	}

	if msg.StartBlock >= msg.EndBlock {
		return ErrInvalidMsg.Wrapf("End should be greater than to start block start block=%d,end block=%d", msg.StartBlock, msg.EndBlock)
	}

	if bytes.Equal(msg.RootHash, common.Hash{}.Bytes()) {
		return ErrInvalidMsg.Wrapf("Invalid roothash %v", string(msg.RootHash))
	}

	if len(msg.RootHash) != common.HashLength {
		return ErrInvalidMsg.Wrapf("Invalid roothash length %v", len(msg.RootHash))
	}

	return nil
}

// GetSideSignBytes returns side sign bytes
func (msg MsgCpAck) GetSideSignBytes() []byte {
	return nil
}

var _ sdk.Msg = &MsgCpNoAck{}

func NewMsgCheckpointNoAck(from string) MsgCpNoAck {
	return MsgCpNoAck{
		From: util.FormatAddress(from),
	}
}

func (msg MsgCpNoAck) ValidateBasic() error {
	ac := addressCodec.NewHexCodec()

	addrBytes, err := ac.StringToBytes(msg.From)
	if err != nil {
		return ErrInvalidMsg.Wrapf("Invalid sender %s", msg.From)
	}

	accAddr := sdk.AccAddress(addrBytes)

	if accAddr.Empty() {
		return ErrInvalidMsg.Wrapf("Invalid sender %s", msg.From)
	}

	return nil
}

func IsCheckpointMsg(msg proto.Message) bool {
	return sdk.MsgTypeURL(msg) == sdk.MsgTypeURL(&MsgCheckpoint{})
}
