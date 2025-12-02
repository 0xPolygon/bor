package stakinginfo

import "github.com/ethereum/go-ethereum/common"

const (
	// StakeUpdateEventID is the topic ID of StakeUpdate event
	StakeUpdateEventID = "0x35af9eea1f0e7b300b0a14fae90139a072470e44daa3f14b5069bebbc1265bda"
)

// GetStakeUpdateEventID returns the hash of StakeUpdate event ID
func GetStakeUpdateEventID() common.Hash {
	return common.HexToHash(StakeUpdateEventID)
}
