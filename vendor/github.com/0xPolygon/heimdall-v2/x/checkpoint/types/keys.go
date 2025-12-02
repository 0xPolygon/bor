package types

import (
	"cosmossdk.io/collections"
)

const (
	// ModuleName is the name of the staking module
	ModuleName = "checkpoint"

	// StoreKey is the string store representation
	StoreKey = ModuleName

	// RouterKey is the msg router key for the staking module
	RouterKey = ModuleName
)

var (
	// ParamsPrefixKey represents the prefix for param
	ParamsPrefixKey = collections.NewPrefix([]byte{0x80})

	// CheckpointMapPrefixKey represents the key for each key for the checkpoint map
	CheckpointMapPrefixKey = collections.NewPrefix([]byte{0x81})
	// BufferedCheckpointPrefixKey represents the prefix for the buffered checkpoint
	BufferedCheckpointPrefixKey = collections.NewPrefix([]byte{0x82})

	// AckCountPrefixKey represents the prefix for ack count
	AckCountPrefixKey = collections.NewPrefix([]byte{0x83})

	// LastNoAckPrefixKey represents the prefix for last no ack
	LastNoAckPrefixKey = collections.NewPrefix([]byte{0x84})

	// CheckpointSignaturesPrefixKey represents the prefix for checkpoint signatures
	CheckpointSignaturesPrefixKey = collections.NewPrefix([]byte{0x85})

	// CheckpointSignaturesTxHashPrefixKey represents the prefix for checkpoint signatures tx hash
	CheckpointSignaturesTxHashPrefixKey = collections.NewPrefix([]byte{0x86})
)
