package conv

import (
	"errors"
	"fmt"
	"github.com/holiman/uint256"
	"math/big"
	"strconv"
)

var DestNilErr = errors.New("destination is nil")
var EmptyInputErr = errors.New("input is empty")
var MissingQuoteErr = errors.New("input has quote open without close")

func Uint256Unmarshal(v *uint256.Int, b []byte) error {
	if v == nil {
		return DestNilErr
	}
	if len(b) == 0 {
		return EmptyInputErr
	}
	if b[0] == '"' {
		if len(b) == 1 || b[len(b)-1] != b[0] {
			return MissingQuoteErr
		}
		b = b[1 : len(b)-1]
	}
	x := new(big.Int)
	err := x.UnmarshalText(b)
	if err != nil {
		return fmt.Errorf("failed to unmarshal Uint256View: %w", err)
	}
	if x.Sign() < 0 {
		return strconv.ErrRange
	}
	if v.SetFromBig(x) {
		return strconv.ErrRange
	}
	return nil
}

// Parse a uint of bitSize bits into a uint64, with or without quotes, in any base,
// with common prefixes accepted to change base.
func uintUnmarshal(v *uint64, b []byte, bitSize int) error {
	if v == nil {
		return DestNilErr
	}
	if len(b) == 0 {
		return EmptyInputErr
	}
	if b[0] == '"' {
		if len(b) == 1 || b[len(b)-1] != b[0] {
			return MissingQuoteErr
		}
		b = b[1 : len(b)-1]
	}
	n, err := strconv.ParseUint(string(b), 0, bitSize)
	if err != nil {
		return err
	}
	*v = n
	return nil
}

func Uint64Unmarshal(v *uint64, b []byte) error {
	return uintUnmarshal(v, b, 64)
}

func Uint32Unmarshal(v *uint32, b []byte) error {
	var x uint64
	if err := uintUnmarshal(&x, b, 32); err != nil {
		return err
	}
	*v = uint32(x)
	return nil
}

func Uint16Unmarshal(v *uint16, b []byte) error {
	var x uint64
	if err := uintUnmarshal(&x, b, 16); err != nil {
		return err
	}
	*v = uint16(x)
	return nil
}

func Uint8Unmarshal(v *uint8, b []byte) error {
	var x uint64
	if err := uintUnmarshal(&x, b, 8); err != nil {
		return err
	}
	*v = uint8(x)
	return nil
}

// Uint256Marshal to decimal number, with quotes
func Uint256Marshal(v *uint256.Int) ([]byte, error) {
	return []byte(fmt.Sprintf("\"%d\"", v)), nil
}

// Uint64Marshal to decimal number, with quotes
func Uint64Marshal(v uint64) ([]byte, error) {
	var dest [22]byte // ceil(log10(2**64)) + 2 = 22
	dest[0] = '"'
	res := strconv.AppendUint(dest[0:1], v, 10)
	res = append(res, '"')
	return res, nil
}

// Uint32Marshal to decimal number, with quotes
func Uint32Marshal(v uint32) ([]byte, error) {
	return Uint64Marshal(uint64(v))
}

// Uint16Marshal to decimal number, with quotes
func Uint16Marshal(v uint16) ([]byte, error) {
	return Uint64Marshal(uint64(v))
}

// Uint8Marshal to decimal number, with quotes
func Uint8Marshal(v uint8) ([]byte, error) {
	return Uint64Marshal(uint64(v))
}
