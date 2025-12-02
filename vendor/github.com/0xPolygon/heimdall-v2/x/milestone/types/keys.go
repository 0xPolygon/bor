package types

import (
	"cosmossdk.io/collections"
)

const (
	// ModuleName is the name of the milestone module
	ModuleName = "milestone"

	// StoreKey is the string store representation
	StoreKey = ModuleName

	// RouterKey is the msg router key for the milestone module
	RouterKey = ModuleName
)

var (
	ParamsPrefixKey             = collections.NewPrefix([]byte{0x80})
	MilestoneMapPrefixKey       = collections.NewPrefix([]byte{0x81})
	CountPrefixKey              = collections.NewPrefix([]byte{0x83})
	LastMilestoneBlockPrefixKey = collections.NewPrefix([]byte{0x84})
)
