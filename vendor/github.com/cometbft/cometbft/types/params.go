package types

import (
	"errors"
	"fmt"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

const (
	// MaxBlockSizeBytes is the maximum permitted size of the blocks.
	MaxBlockSizeBytes = 104857600 // 100MB

	// MaxBlobSizeBytes is the maximum permitted size of the blob.
	MaxBlobSizeBytes = 800 * 1024

	// PartSizeBytes defines the size (in bytes) of each part when splitting
	// a large object for transmission over the wire.
	// CometBFT avoids sending large messages in a single payload; instead, it breaks
	// them into smaller parts and transmits them sequentially.
	// This applies to structures like blocks and blobs.
	// Recipients are responsible for reassembling the full object from its parts.
	PartSizeBytes uint32 = 65536 // 64kB

	// MaxBlockPartsCount is the maximum number of block parts.
	MaxBlockPartsCount = (MaxBlockSizeBytes + PartSizeBytes - 1) / PartSizeBytes

	ABCIPubKeyTypeEd25519   = ed25519.KeyType
	ABCIPubKeyTypeSecp256k1 = secp256k1.KeyType
)

var ABCIPubKeyTypesToNames = map[string]string{
	ABCIPubKeyTypeEd25519:   ed25519.PubKeyName,
	ABCIPubKeyTypeSecp256k1: secp256k1.PubKeyName,
}

// ConsensusParams contains consensus critical parameters that determine the
// validity of blocks.
type ConsensusParams struct {
	Block     BlockParams     `json:"block"`
	Blob      BlobParams      `json:"blob"`
	Evidence  EvidenceParams  `json:"evidence"`
	Validator ValidatorParams `json:"validator"`
	Version   VersionParams   `json:"version"`
	ABCI      ABCIParams      `json:"abci"`
}

// BlockParams define limits on the block size and gas plus minimum time
// between blocks.
type BlockParams struct {
	MaxBytes int64 `json:"max_bytes"`
	MaxGas   int64 `json:"max_gas"`
}

// BlobParams define limits on the blob size.
type BlobParams struct {
	MaxBytes int64 `json:"max_bytes"`
}

// EvidenceParams determine how we handle evidence of malfeasance.
type EvidenceParams struct {
	MaxAgeNumBlocks int64         `json:"max_age_num_blocks"` // only accept new evidence more recent than this
	MaxAgeDuration  time.Duration `json:"max_age_duration"`
	MaxBytes        int64         `json:"max_bytes"`
}

// ValidatorParams restrict the public key types validators can use.
// NOTE: uses ABCI pubkey naming, not Amino names.
type ValidatorParams struct {
	PubKeyTypes []string `json:"pub_key_types"`
}

type VersionParams struct {
	App uint64 `json:"app"`
}

// ABCIParams configure ABCI functionality specific to the Application Blockchain
// Interface.
type ABCIParams struct {
	VoteExtensionsEnableHeight int64 `json:"vote_extensions_enable_height"`
}

// VoteExtensionsEnabled returns true if vote extensions are enabled at height h
// and false otherwise.
func (a ABCIParams) VoteExtensionsEnabled(h int64) bool {
	if h < 1 {
		panic(fmt.Errorf("cannot check if vote extensions enabled for height %d (< 1)", h))
	}
	if a.VoteExtensionsEnableHeight == 0 {
		return false
	}
	return a.VoteExtensionsEnableHeight <= h
}

// DefaultConsensusParams returns a default ConsensusParams.
func DefaultConsensusParams() *ConsensusParams {
	return &ConsensusParams{
		Block:     DefaultBlockParams(),
		Blob:      DefaultBlobParams(),
		Evidence:  DefaultEvidenceParams(),
		Validator: DefaultValidatorParams(),
		Version:   DefaultVersionParams(),
		ABCI:      DefaultABCIParams(),
	}
}

// DefaultBlockParams returns a default BlockParams.
func DefaultBlockParams() BlockParams {
	return BlockParams{
		MaxBytes: 22020096, // 21MB
		MaxGas:   -1,
	}
}

// DefaultBlobParams returns a default BlobParams.
func DefaultBlobParams() BlobParams {
	return BlobParams{
		MaxBytes: 0, // 0 bytes means no blob. To use the blob feature
		// there needs to be a government proposal to set this to some other value
	}
}

// DefaultEvidenceParams returns a default EvidenceParams.
func DefaultEvidenceParams() EvidenceParams {
	return EvidenceParams{
		MaxAgeNumBlocks: 100000, // 27.8 hrs at 1block/s
		MaxAgeDuration:  48 * time.Hour,
		MaxBytes:        1048576, // 1MB
	}
}

// DefaultValidatorParams returns a default ValidatorParams, which allows
// only Secp256k1 pubkeys.
func DefaultValidatorParams() ValidatorParams {
	return ValidatorParams{
		PubKeyTypes: []string{ABCIPubKeyTypeSecp256k1},
	}
}

func DefaultVersionParams() VersionParams {
	return VersionParams{
		App: 0,
	}
}

func DefaultABCIParams() ABCIParams {
	return ABCIParams{
		// When set to 0, vote extensions are not required.
		VoteExtensionsEnableHeight: 0,
	}
}

func IsValidPubkeyType(params ValidatorParams, pubkeyType string) bool {
	for i := 0; i < len(params.PubKeyTypes); i++ {
		if params.PubKeyTypes[i] == pubkeyType {
			return true
		}
	}
	return false
}

// Validate validates the ConsensusParams to ensure all values are within their
// allowed limits, and returns an error if they are not.
func (params ConsensusParams) ValidateBasic() error {
	if params.Block.MaxBytes == 0 {
		return fmt.Errorf("block.MaxBytes cannot be 0")
	}
	if params.Block.MaxBytes < -1 {
		return fmt.Errorf("block.MaxBytes must be -1 or greater than 0. Got %d",

			params.Block.MaxBytes)
	}
	if params.Block.MaxBytes > MaxBlockSizeBytes {
		return fmt.Errorf("block.MaxBytes is too big. %d > %d",
			params.Block.MaxBytes, MaxBlockSizeBytes)
	}

	if params.Block.MaxGas < -1 {
		return fmt.Errorf("block.MaxGas must be greater or equal to -1. Got %d",
			params.Block.MaxGas)
	}

	if params.Blob.MaxBytes < -1 {
		return fmt.Errorf("blob.MaxBytes must be -1 or greater than 0. Got %d",
			params.Blob.MaxBytes)
	}
	if params.Blob.MaxBytes > MaxBlobSizeBytes {
		return fmt.Errorf("blob.MaxBytes is too big. %d > %d",
			params.Blob.MaxBytes, MaxBlobSizeBytes)
	}

	if params.Evidence.MaxAgeNumBlocks <= 0 {
		return fmt.Errorf("evidence.MaxAgeNumBlocks must be greater than 0. Got %d",
			params.Evidence.MaxAgeNumBlocks)
	}

	if params.Evidence.MaxAgeDuration <= 0 {
		return fmt.Errorf("evidence.MaxAgeDuration must be grater than 0 if provided, Got %v",
			params.Evidence.MaxAgeDuration)
	}

	maxBytes := params.Block.MaxBytes
	if maxBytes == -1 {
		maxBytes = int64(MaxBlockSizeBytes)
	}
	if params.Evidence.MaxBytes > maxBytes {
		return fmt.Errorf("evidence.MaxBytesEvidence is greater than upper bound, %d > %d",
			params.Evidence.MaxBytes, params.Block.MaxBytes)
	}

	if params.Evidence.MaxBytes < 0 {
		return fmt.Errorf("evidence.MaxBytes must be non negative. Got: %d",
			params.Evidence.MaxBytes)
	}

	if params.ABCI.VoteExtensionsEnableHeight < 0 {
		return fmt.Errorf("ABCI.VoteExtensionsEnableHeight cannot be negative. Got: %d", params.ABCI.VoteExtensionsEnableHeight)
	}

	if len(params.Validator.PubKeyTypes) == 0 {
		return errors.New("len(Validator.PubKeyTypes) must be greater than 0")
	}

	// Check if keyType is a known ABCIPubKeyType
	for i := 0; i < len(params.Validator.PubKeyTypes); i++ {
		keyType := params.Validator.PubKeyTypes[i]
		if _, ok := ABCIPubKeyTypesToNames[keyType]; !ok {
			return fmt.Errorf("params.Validator.PubKeyTypes[%d], %s, is an unknown pubkey type",
				i, keyType)
		}
	}

	return nil
}

// ValidateUpdate validates the updated VoteExtensionsEnableHeight.
// | r | params...EnableHeight | updated...EnableHeight | result (nil == pass)
// |  1 | *                    | (nil)                  | nil
// |  2 | *                    | < 0                    | VoteExtensionsEnableHeight must be positive
// |  3 | <=0                  | 0                      | nil
// |  4 | X                    | X (>=0)                | nil
// |  5 | > 0; <=height        | 0                      | vote extensions cannot be disabled once enabled
// |  6 | > 0; > height        | 0                      | nil (disable a previous proposal)
// |  7 | *                    | <=height               | vote extensions cannot be updated to a past height
// |  8 | <=0                  | > height (*)           | nil
// |  9 | (> 0) <=height       | > height (*)           | vote extensions cannot be modified once enabled
// | 10 | (> 0) > height       | > height (*)           | nil
func (params ConsensusParams) ValidateUpdate(updated *cmtproto.ConsensusParams, h int64) error {
	// 1
	if updated == nil || updated.Abci == nil {
		return nil
	}
	// 2
	if updated.Abci.VoteExtensionsEnableHeight < 0 {
		return errors.New("VoteExtensionsEnableHeight must be positive")
	}
	// 3
	if params.ABCI.VoteExtensionsEnableHeight <= 0 && updated.Abci.VoteExtensionsEnableHeight == 0 {
		return nil
	}
	// 4 (implicit: updated.Abci.VoteExtensionsEnableHeight >= 0)
	if params.ABCI.VoteExtensionsEnableHeight == updated.Abci.VoteExtensionsEnableHeight {
		return nil
	}
	// 5 & 6
	if params.ABCI.VoteExtensionsEnableHeight > 0 && updated.Abci.VoteExtensionsEnableHeight == 0 {
		// 5
		if params.ABCI.VoteExtensionsEnableHeight <= h {
			return fmt.Errorf("vote extensions cannot be disabled once enabled"+
				"old enable height: %d, current height %d",
				params.ABCI.VoteExtensionsEnableHeight, h)
		}
		// 6
		return nil
	}
	// 7 (implicit: updated.Abci.VoteExtensionsEnableHeight > 0)
	if updated.Abci.VoteExtensionsEnableHeight <= h {
		return fmt.Errorf("vote extensions cannot be updated to a past or current height, "+
			"enable height: %d, current height %d",
			updated.Abci.VoteExtensionsEnableHeight, h)
	}
	// 8 (implicit: updated.Abci.VoteExtensionsEnableHeight > h)
	if params.ABCI.VoteExtensionsEnableHeight <= 0 {
		return nil
	}
	// 9 (implicit: params.ABCI.VoteExtensionsEnableHeight > 0 && updated.Abci.VoteExtensionsEnableHeight > h)
	if params.ABCI.VoteExtensionsEnableHeight <= h {
		return fmt.Errorf("vote extensions cannot be modified once enabled"+
			"enable height: %d, current height %d",
			params.ABCI.VoteExtensionsEnableHeight, h)
	}
	// 10 (implicit: params.ABCI.VoteExtensionsEnableHeight > h && updated.Abci.VoteExtensionsEnableHeight > h)
	return nil
}

// Hash returns a hash of a subset of the parameters to store in the block header.
// Only the Block.MaxBytes and Block.MaxGas are included in the hash.
// This allows the ConsensusParams to evolve more without breaking the block
// protocol. No need for a Merkle tree here, just a small struct to hash.
func (params ConsensusParams) Hash() []byte {
	hasher := tmhash.New()

	hp := cmtproto.HashedParams{
		BlockMaxBytes: params.Block.MaxBytes,
		BlockMaxGas:   params.Block.MaxGas,
	}

	bz, err := hp.Marshal()
	if err != nil {
		panic(err)
	}

	_, err = hasher.Write(bz)
	if err != nil {
		panic(err)
	}
	return hasher.Sum(nil)
}

// Update returns a copy of the params with updates from the non-zero fields of p2.
// NOTE: note: must not modify the original
func (params ConsensusParams) Update(params2 *cmtproto.ConsensusParams) ConsensusParams {
	res := params // explicit copy

	if params2 == nil {
		return res
	}

	// we must defensively consider any structs may be nil
	if params2.Block != nil {
		res.Block.MaxBytes = params2.Block.MaxBytes
		res.Block.MaxGas = params2.Block.MaxGas
	}
	if params2.Blob != nil {
		res.Blob.MaxBytes = params2.Blob.MaxBytes
	}
	if params2.Evidence != nil {
		res.Evidence.MaxAgeNumBlocks = params2.Evidence.MaxAgeNumBlocks
		res.Evidence.MaxAgeDuration = params2.Evidence.MaxAgeDuration
		res.Evidence.MaxBytes = params2.Evidence.MaxBytes
	}
	if params2.Validator != nil {
		// Copy params2.Validator.PubkeyTypes, and set result's value to the copy.
		// This avoids having to initialize the slice to 0 values, and then write to it again.
		res.Validator.PubKeyTypes = append([]string{}, params2.Validator.PubKeyTypes...)
	}
	if params2.Version != nil {
		res.Version.App = params2.Version.App
	}
	if params2.Abci != nil {
		res.ABCI.VoteExtensionsEnableHeight = params2.Abci.GetVoteExtensionsEnableHeight()
	}
	return res
}

func (params *ConsensusParams) ToProto() cmtproto.ConsensusParams {
	return cmtproto.ConsensusParams{
		Block: &cmtproto.BlockParams{
			MaxBytes: params.Block.MaxBytes,
			MaxGas:   params.Block.MaxGas,
		},
		Blob: &cmtproto.BlobParams{
			MaxBytes: params.Blob.MaxBytes,
		},
		Evidence: &cmtproto.EvidenceParams{
			MaxAgeNumBlocks: params.Evidence.MaxAgeNumBlocks,
			MaxAgeDuration:  params.Evidence.MaxAgeDuration,
			MaxBytes:        params.Evidence.MaxBytes,
		},
		Validator: &cmtproto.ValidatorParams{
			PubKeyTypes: params.Validator.PubKeyTypes,
		},
		Version: &cmtproto.VersionParams{
			App: params.Version.App,
		},
		Abci: &cmtproto.ABCIParams{
			VoteExtensionsEnableHeight: params.ABCI.VoteExtensionsEnableHeight,
		},
	}
}

func ConsensusParamsFromProto(pbParams cmtproto.ConsensusParams) ConsensusParams {
	c := ConsensusParams{
		Block: BlockParams{
			MaxBytes: pbParams.Block.MaxBytes,
			MaxGas:   pbParams.Block.MaxGas,
		},
		Blob: BlobParams{
			MaxBytes: pbParams.Blob.MaxBytes,
		},
		Evidence: EvidenceParams{
			MaxAgeNumBlocks: pbParams.Evidence.MaxAgeNumBlocks,
			MaxAgeDuration:  pbParams.Evidence.MaxAgeDuration,
			MaxBytes:        pbParams.Evidence.MaxBytes,
		},
		Validator: ValidatorParams{
			PubKeyTypes: pbParams.Validator.PubKeyTypes,
		},
		Version: VersionParams{
			App: pbParams.Version.App,
		},
	}
	if pbParams.Abci != nil {
		c.ABCI.VoteExtensionsEnableHeight = pbParams.Abci.GetVoteExtensionsEnableHeight()
	}
	return c
}
