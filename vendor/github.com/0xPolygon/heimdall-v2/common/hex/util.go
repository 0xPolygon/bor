package hex

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

const MaxProofLength = 1024

// FormatHex returns a checksum hex string prefixed with 0x.
func FormatHex(data []byte) string {
	return "0x" + common.Bytes2Hex(data)
}

// FormatAddress makes sure the address is compliant with the heimdall-v2 format
func FormatAddress(hexAddr string) string {
	hexAddr = strings.TrimSpace(strings.ToLower(hexAddr))
	return "0x" + strings.TrimPrefix(hexAddr, "0x")
}

// IsTxHashNonEmpty returns true if the input is a non-empty string.
func IsTxHashNonEmpty(s string) bool {
	return strings.TrimSpace(s) != ""
}

// ValidateProof checks if the proof is a valid hex string representing N 32-byte chunks, and not too long.
func ValidateProof(proof string) error {
	proofBytes := common.FromHex(proof)
	if len(proofBytes) == 0 {
		return errors.New("proof is empty or invalid hex")
	}
	if len(proofBytes)%32 != 0 {
		return errors.New("proof length must be a multiple of 32 bytes")
	}
	if len(proofBytes) > MaxProofLength {
		return fmt.Errorf("proof exceeds maximum allowed size of %d bytes", MaxProofLength)
	}
	return nil
}
