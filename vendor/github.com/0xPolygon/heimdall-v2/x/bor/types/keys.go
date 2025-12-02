package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the module name
	ModuleName = "bor"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// RouterKey defines the module's message routing key
	RouterKey = ModuleName
)

// Keys for store prefixes
var (
	LastSpanIDKey            = collections.NewPrefix(0x35) // Key to store last span
	SpanPrefixKey            = collections.NewPrefix(0x36) // Prefix key to store span
	SeedLastBlockProducerKey = collections.NewPrefix(0x37) // key to store the last bor blocks producer seed
	ParamsKey                = collections.NewPrefix(0x38) // Key to store the params in the store
	ProducerVotesKey         = collections.NewPrefix(0x39) // Key to store the producer votes in the store
	PerformanceScoreKey      = collections.NewPrefix(0x3A) // Key to store the performance score in the store
	LatestActiveProducerKey  = collections.NewPrefix(0x3B) // Key to store the latest active producer in the store
	LatestFailedProducerKey  = collections.NewPrefix(0x3C) // Key to store the latest failed producer in the store
	LastSpanBlockKey         = collections.NewPrefix(0x3D) // Key to store the last span block in the store
)
