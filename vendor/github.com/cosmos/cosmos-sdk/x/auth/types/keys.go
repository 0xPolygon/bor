package types

import (
	"cosmossdk.io/collections"
)

const (
	// ModuleName defines the module name
	ModuleName = "auth"

	// StoreKey is string representation of the store key for auth
	StoreKey = ModuleName

	// FeeCollectorName the root string for the fee collector account address
	FeeCollectorName = "fee_collector"

	// FeeToken fee token name
	FeeToken = "pol"
)

var (
	// ParamsKey is the prefix for params key
	ParamsKey = collections.NewPrefix(0)

	// AddressStoreKeyPrefix prefix for account-by-address store
	AddressStoreKeyPrefix = collections.NewPrefix(1)

	// GlobalAccountNumberKey identifies the prefix where the monotonically increasing
	// account number is stored.
	GlobalAccountNumberKey = collections.NewPrefix(2)

	// AccountNumberStoreKeyPrefix prefix for account-by-id store
	AccountNumberStoreKeyPrefix = collections.NewPrefix("accountNumber")

	// ProposerKeyPrefix prefix for proposer
	ProposerKeyPrefix = collections.NewPrefix("proposer")
)

// ProposerKey returns proposer key
func ProposerKey() []byte {
	return ProposerKeyPrefix
}
