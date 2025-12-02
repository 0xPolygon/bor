package statesender

import "github.com/ethereum/go-ethereum/common"

const (
	// StateSyncedEventID is the topic ID of StateSynced event
	StateSyncedEventID = "0x103fed9db65eac19c4d870f49ab7520fe03b99f1838e5996caf47e9e43308392"
)

// GetStateSyncedEventID returns the hash of StateSynced event ID
func GetStateSyncedEventID() common.Hash {
	return common.HexToHash(StateSyncedEventID)
}
