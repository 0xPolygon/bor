package types

import "cosmossdk.io/collections"

const (
	// ModuleName is the name of the module
	ModuleName = "clerk"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// RouterKey defines the module's message routing key
	RouterKey = ModuleName
)

var (
	RecordsWithIDKeyPrefix   = collections.NewPrefix(0)
	RecordsWithTimeKeyPrefix = collections.NewPrefix(1)
	RecordSequencesKeyPrefix = collections.NewPrefix(2)

	// DefaultValue of the record sequence
	DefaultValue = []byte{0x01}
)
