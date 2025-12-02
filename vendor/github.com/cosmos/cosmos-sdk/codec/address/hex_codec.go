package address

import (
	"errors"
	"strings"

	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/ethereum/go-ethereum/common"
)

type HexCodec struct{}

var _ address.Codec = &HexCodec{}

func NewHexCodec() address.Codec {
	return HexCodec{}
}

// StringToBytes encodes text to bytes
func (bc HexCodec) StringToBytes(hexAddr string) ([]byte, error) {
	if len(strings.TrimSpace(hexAddr)) == 0 {
		return []byte{}, errors.New("empty address string is not allowed")
	}

	hexAddr = "0x" + strings.TrimPrefix(strings.ToLower(hexAddr), "0x")

	bz := common.FromHex(hexAddr)

	if err := VerifyAddressFormat(bz); err != nil {
		return nil, err
	}

	return bz, nil
}

// BytesToString decodes bytes to text
func (bc HexCodec) BytesToString(bz []byte) (string, error) {
	if len(bz) == 0 || bz == nil {
		return "", nil
	}

	if err := VerifyAddressFormat(bz); err != nil {
		return "", err
	}

	hexAddr := "0x" + strings.TrimPrefix(strings.ToLower(common.Bytes2Hex(bz)), "0x")

	return hexAddr, nil
}

// VerifyAddressFormat verifies that the provided bytes form a valid address
func VerifyAddressFormat(bz []byte) error {
	if len(bz) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrUnknownAddress, "addresses cannot be empty")
	}

	if !common.IsHexAddress(common.Bytes2Hex(bz)) {
		return errorsmod.Wrapf(sdkerrors.ErrUnknownAddress, "invalid address")
	}

	return nil
}
