// Package commitment defines the sequence store's commitment chain: a running
// keccak256 hash over the entry sequence, advanced by a per-item fold
//
//	head' = keccak256(head ‖ tag ‖ item)
//
// where an item is an open record's context fields, one transaction, or a
// sealed block hash, and tag is the item's entry-class domain tag. The fold
// operates over the canonical byte encodings defined here — never over
// serialized protobuf, which is not canonical across implementations.
// Producers, the store ingress, and consumers must compute bit-identical
// heads; this package is the normative definition for all three.
package commitment

import (
	"errors"
	"fmt"
	"math/big"

	"golang.org/x/crypto/sha3"
)

// Domain seeds the chain's genesis head; it never varies at runtime.
const Domain = "polygon-pos/sequence-store"

// FormatVersion is folded into the genesis seed; bump it on any change to the
// wire format or to the canonical encodings below, so streams from different
// revisions fail structurally at the first head check.
const FormatVersion uint32 = 1

// Entry-class domain-separation tags. One per entry class, so no folded item
// can collide across types. Normative.
const (
	TagOpen byte = 0x01
	TagTx   byte = 0x02
	TagSeal byte = 0x03
)

// Head is a commitment-chain head — the post-fold state after some prefix of
// the sequence.
type Head [32]byte

// Bytes returns a copy of the head. Use it to fill a prefix_commitment field
// from a variable that keeps advancing: h[:] aliases the variable's storage,
// so the field would silently change on the next fold.
func (h Head) Bytes() []byte {
	return append([]byte(nil), h[:]...)
}

// Seed returns head_0 = keccak256(Domain ‖ chain_id ‖ format_version), the
// head of the empty sequence. The first entry ever appended must carry it as
// prefix_commitment. Encoding: Domain bytes, then chainID as 8-byte
// big-endian, then FormatVersion as 4-byte big-endian.
func Seed(chainID uint64) Head {
	buf := make([]byte, 0, len(Domain)+12)
	buf = append(buf, Domain...)
	buf = appendUint64(buf, chainID)
	buf = appendUint32(buf, FormatVersion)

	return keccak256(buf)
}

// Fold advances a head by one item: keccak256(head ‖ tag ‖ item).
func Fold(head Head, tag byte, item []byte) Head {
	buf := make([]byte, 0, 33+len(item))
	buf = append(buf, head[:]...)
	buf = append(buf, tag)
	buf = append(buf, item...)

	return keccak256(buf)
}

// OpenContext is the header context fixed by a block-open record — every
// header field execution depends on.
type OpenContext struct {
	Number     uint64
	Timestamp  uint64
	ParentHash [32]byte
	GasLimit   uint64
	BaseFee    *big.Int
}

// FoldOpen folds a block-open record's context, producing C_open(N). The
// canonical encoding is fixed-width so concatenation is unambiguous: number
// and timestamp as 8-byte big-endian, the parent hash as 32 bytes, gas limit
// as 8-byte big-endian, base fee as 32-byte big-endian. It errors if the base
// fee is nil, negative, or wider than 256 bits.
func FoldOpen(head Head, open OpenContext) (Head, error) {
	buf := make([]byte, 0, 88)
	buf = appendUint64(buf, open.Number)
	buf = appendUint64(buf, open.Timestamp)
	buf = append(buf, open.ParentHash[:]...)
	buf = appendUint64(buf, open.GasLimit)

	if open.BaseFee == nil || open.BaseFee.Sign() < 0 || open.BaseFee.BitLen() > 256 {
		return Head{}, fmt.Errorf("fold open context: %w", ErrInvalidBaseFee)
	}

	var fee [32]byte

	open.BaseFee.FillBytes(fee[:])
	buf = append(buf, fee[:]...)

	return Fold(head, TagOpen, buf), nil
}

// ErrInvalidBaseFee reports an open context whose base fee is nil, negative,
// or wider than 256 bits.
var ErrInvalidBaseFee = errors.New("base fee must be a non-negative integer of at most 256 bits")

// FoldTx folds one raw signed transaction. Folding per transaction — not per
// record — is normative: it makes the chain independent of how transactions
// are batched into records.
func FoldTx(head Head, rawTx []byte) Head {
	return Fold(head, TagTx, rawTx)
}

// FoldTxs folds transactions in order, one at a time; after a block's last
// transaction the head is C_block(N).
func FoldTxs(head Head, rawTxs [][]byte) Head {
	for _, tx := range rawTxs {
		head = FoldTx(head, tx)
	}

	return head
}

// FoldSeal folds a block's sealed hash (keccak256 of its sealed header),
// producing C_seal(N) — the head the next block's open record extends on the
// normal path.
func FoldSeal(head Head, sealedHash [32]byte) Head {
	return Fold(head, TagSeal, sealedHash[:])
}

// SealedHash returns keccak256 of a sealed RLP header — the block hash a seal
// record commits to and the item FoldSeal folds.
func SealedHash(header []byte) [32]byte {
	return keccak256(header)
}

func keccak256(data []byte) Head {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)

	var out Head

	h.Sum(out[:0])

	return out
}

func appendUint64(buf []byte, v uint64) []byte {
	return append(buf,
		byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
		byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendUint32(buf []byte, v uint32) []byte {
	return append(buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
