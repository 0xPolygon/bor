package types

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// Blob represents an Ethereum blob, a binary data structure introduced in
// EIP-4844 (Proto-Danksharding).
// Blobs are primarily used by Layer 2 rollups to post batched transaction data
// to Ethereum in a cost-efficient manner. Each blob is referenced in a blob
// transaction via a KZG commitment, which ensures data integrity without
// requiring full storage in execution clients.
//
// A "Blob" is typically an opaque byte slice up to ~128 KB. In our case, however,
// a single Blob slice can hold multiple blobs, potentially exceeding that size.
// The exact number of contained blobs is encoded within the block's transactions,
// but CometBFT neither parses nor uses this information. It simply gossips the
// blobs alongside their corresponding blocks.
//
// We introduce support for blobs in CometBFT to serve use-cases where CometBFT
// acts purely as a finalization gadget, so storing blobs in CometBFT's storage
// layer is unnecessary and undesirable.
type Blob []byte

// Hash returns the SHA-256 hash of the blob.
func (b Blob) Hash() []byte {
	if len(b) == 0 {
		return nil
	}
	return tmhash.Sum(b)
}

func (b Blob) IsNil() bool {
	return len(b) == 0
}

// String returns a hex-encoded representation of the blob.
func (b Blob) String() string {
	if b == nil {
		return "nil-Blob"
	}
	return "Blob#" + hex.EncodeToString(b)
}

// BlobID defines the unique ID of a block as its hash and its PartSetHeader.
// This structure is identical to BlobID, but we create a separate type to avoid
// confusion. Since BlobID and BlockID represent distinct concepts, defining a
// dedicated type with its own methods allows for independent changes later on.
type BlobID struct {
	Hash          cmtbytes.HexBytes `json:"hash"`
	PartSetHeader PartSetHeader     `json:"parts"`
}

// ValidateBasic performs basic validation.
func (b BlobID) ValidateBasic() error {
	if err := ValidateHash(b.Hash); err != nil {
		return errors.New("wrong Hash")
	}
	if err := b.PartSetHeader.ValidateBasic(); err != nil {
		return fmt.Errorf("wrong PartSetHeader: %w", err)
	}
	return nil
}

// IsNil returns true if this is the BlobID of a nil blob.
func (b BlobID) IsNil() bool {
	return len(b.Hash) == 0 && b.PartSetHeader.IsZero()
}

// IsComplete returns true if this is a valid BlobID of a non-nil blob.
func (b BlobID) IsComplete() bool {
	return len(b.Hash) == tmhash.Size &&
		b.PartSetHeader.Total > 0 &&
		len(b.PartSetHeader.Hash) == tmhash.Size
}

// String returns a human readable string representation of the BlobID.
//
// 1. hash
// 2. part set header
//
// See PartSetHeader#String.
func (b BlobID) String() string {
	return fmt.Sprintf(`%v:%v`, b.Hash, b.PartSetHeader)
}

// ToProto serializes BlobID to protobuf.
func (b BlobID) ToProto() cmtproto.BlobID {
	if b.IsNil() {
		return cmtproto.BlobID{}
	}

	return cmtproto.BlobID{
		Hash:          b.Hash,
		PartSetHeader: b.PartSetHeader.ToProto(),
	}
}

// BlobIDFromProto deserializes a protobuf BlobID to a BlobID type.
// It returns an error if the BlobID is invalid.
func BlobIDFromProto(b *cmtproto.BlobID) (BlobID, error) {
	if b == nil {
		return BlobID{}, errors.New("nil BlobID")
	}

	ph, err := PartSetHeaderFromProto(&b.PartSetHeader)
	if err != nil {
		return BlobID{}, err
	}

	blobID := BlobID{
		Hash:          b.Hash,
		PartSetHeader: *ph,
	}

	if err := blobID.ValidateBasic(); err != nil {
		return BlobID{}, fmt.Errorf("validating BlobID: %s", err)
	}

	return blobID, nil
}

// ProtoBlobIDIsNil is similar to the IsNil function on BlobID, but for the
// Protobuf representation.
func ProtoBlobIDIsNil(b *cmtproto.BlobID) bool {
	return len(b.Hash) == 0 && ProtoPartSetHeaderIsZero(&b.PartSetHeader)
}
