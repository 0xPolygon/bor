package types

import "cosmossdk.io/collections"

const (
	// ModuleName is the name of the module
	ModuleName = "topup"

	// StoreKey is the store key string for topup
	StoreKey = ModuleName

	// RouterKey is the msg router key for the topup module
	RouterKey = ModuleName

	// DefaultLogIndexUnit represents the default unit for (txHash + logIndex)
	DefaultLogIndexUnit = 100000
)

var (
	// TopupSequencePrefixKey represents the topup sequence prefix key
	TopupSequencePrefixKey = collections.NewPrefix([]byte{0x81})
	// DividendAccountMapKey represents the prefix for each key for the dividend account map
	DividendAccountMapKey = collections.NewPrefix([]byte{0x82})
)
