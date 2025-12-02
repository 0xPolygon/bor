package types

import "cosmossdk.io/collections"

const (
	// ModuleName is the name of the module
	ModuleName = "chainmanager"

	// StoreKey is the store key string for bor
	StoreKey = ModuleName

	// RouterKey is the message route for bor
	RouterKey = ModuleName
)

var ParamsKey = collections.NewPrefix(0) // ParamsKey is the key to store the params in the store
