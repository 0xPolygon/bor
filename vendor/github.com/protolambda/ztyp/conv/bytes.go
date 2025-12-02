package conv

import (
	"encoding/hex"
	"errors"
	"fmt"
)

func BytesString(b []byte) string {
	l := 2 + (len(b) * 2)
	res := make([]byte, l, l)
	res[0] = '0'
	res[1] = 'x'
	hex.Encode(res[2:], b)
	return string(res)
}

func BytesMarshalText(b []byte) ([]byte, error) {
	l := 2 + (len(b) * 2)
	res := make([]byte, l, l)
	res[0] = '0'
	res[1] = 'x'
	hex.Encode(res[2:], b)
	return res, nil
}

// Decodes hex data, with optional 0x prefix, and lower/upper/mixed case.
// The decoded byte length must match the dst length.
func FixedBytesUnmarshalText(dst []byte, text []byte) error {
	if dst == nil {
		return DestNilErr
	}
	if len(text) >= 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = text[2:]
	}
	if len(text) != 2*len(dst) {
		return fmt.Errorf("unexpected length %d, expected %d, got string '%s'", len(text), len(dst), string(text))
	}
	_, err := hex.Decode(dst, text)
	return err
}

// Decodes hex data, with optional 0x prefix, and lower/upper/mixed case.
// Reuses the dst space if possible.
// If dst is too small, a new dst is allocated and the old header is overwritten
func DynamicBytesUnmarshalText(dst *[]byte, text []byte) error {
	if dst == nil {
		return errors.New("nil dst, cannot decode")
	}
	if len(text) >= 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = text[2:]
	}
	size := len(text) / 2
	if cap(*dst) < size {
		*dst = make([]byte, size, size)
	}
	*dst = (*dst)[:size]
	_, err := hex.Decode(*dst, text)
	return err
}
