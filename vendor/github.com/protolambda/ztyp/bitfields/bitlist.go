package bitfields

import (
	"errors"
	"fmt"
	"math/bits"
)

// Returns the length of the bitlist.
// And although strictly speaking invalid. a sane default is returned for:
//  - an empty raw bitlist: a default 0 bitlist will be of length 0 too.
//  - a bitlist with a leading 0 byte: return the bitlist raw bit length,
//     excluding the last byte (As if it was full 0 padding).
func BitlistLen(b []byte) uint64 {
	byteLen := uint64(len(b))
	if byteLen == 0 {
		return 0
	}
	last := b[byteLen-1]
	return ((byteLen - 1) << 3) | BitIndex(last)
}

// Helper function to implement Bitlist with.
// It checks if:
//  0. the raw bitlist is not empty, there must be a 1 bit to determine the length.
//  1. the bitlist has a leading 1 bit in the last byte to determine the length with.
//  2. if b has no more than given limit in bits.
func BitlistCheck(b []byte, limit uint64) error {
	byteLen := uint64(len(b))
	if err := BitlistCheckByteLen(byteLen, limit); err != nil {
		return err
	}
	last := b[byteLen-1]
	return BitlistCheckLastByte(last, limit-((byteLen-1)<<3))
}

func BitlistCheckByteLen(byteLen uint64, bitLimit uint64) error {
	if byteLen == 0 {
		return errors.New("bitlist is missing length delimit bit")
	}
	// there may not be more bytes than necessary for the N bits, +1 for the delimiting bit. (also rounds up)
	if byteLimitWithDelimiter := (bitLimit >> 3) + 1; byteLen > byteLimitWithDelimiter {
		return fmt.Errorf("got %d bytes, expected no more than %d bytes for bitlist",
			byteLen, byteLimitWithDelimiter)
	}
	return nil
}

func BitlistCheckLastByte(last byte, limit uint64) error {
	if last == 0 {
		return errors.New("bitlist is invalid, trailing 0 byte")
	}
	if BitIndex(last) > limit {
		return errors.New("bitlist is invalid, too many bits")
	}
	return nil
}

// Counts the bits set to 1, excluding the delimiter bit.
func BitlistOnesCount(v []byte) uint64 {
	if len(v) == 0 {
		return 0
	}
	count := uint64(0)
	for i := 0; i < len(v)-1; i++ {
		count += uint64(bits.OnesCount8(v[i]))
	}
	last := v[len(v)-1]
	if last == 0 { // trailing zero byte is technically an invalid bitlist, but we can ignore it.
		return count
	}
	// ignore the delimiter bit.
	last ^= uint8(1) << BitIndex(last)
	count += uint64(bits.OnesCount8(last))
	return count
}
