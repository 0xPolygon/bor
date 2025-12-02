package types

import (
	"sort"
)

// CreateCheckpoint generate new checkpoint
func CreateCheckpoint(
	id uint64,
	start uint64,
	end uint64,
	rootHash []byte,
	proposer string,
	borChainID string,
	timestamp uint64,
) Checkpoint {
	return Checkpoint{
		Id:         id,
		StartBlock: start,
		EndBlock:   end,
		RootHash:   rootHash,
		Proposer:   proposer,
		BorChainId: borChainID,
		Timestamp:  timestamp,
	}
}

// SortCheckpoints sorts the array of checkpoints on the basis for timestamps
func SortCheckpoints(checkpoints []Checkpoint) []Checkpoint {
	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].Timestamp < checkpoints[j].Timestamp
	})

	return checkpoints
}
