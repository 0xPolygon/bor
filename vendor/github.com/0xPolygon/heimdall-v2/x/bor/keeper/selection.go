package keeper

import (
	"encoding/binary"
	"math"
	"math/rand"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0xPolygon/heimdall-v2/helper"
	staketypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// selectNextProducers selects producers for the next span
func selectNextProducers(blkHash common.Hash, spanEligibleValidators []staketypes.Validator, producerCount uint64) []uint64 {
	selectedProducers := make([]uint64, 0)

	if len(spanEligibleValidators) <= int(producerCount) {
		for _, validator := range spanEligibleValidators {
			selectedProducers = append(selectedProducers, validator.ValId)
		}

		return selectedProducers
	}

	// extract seed from hash
	seedBytes := helper.ToBytes32(blkHash.Bytes()[:32])
	seed := int64(binary.BigEndian.Uint64(seedBytes[:]))
	// #nosec G404 -- suppress warning as predictable randomness is required
	r := rand.New(rand.NewSource(seed))

	// weighted range from validators' voting power
	votingPower := make([]uint64, len(spanEligibleValidators))
	for idx, validator := range spanEligibleValidators {
		votingPower[idx] = uint64(validator.VotingPower)
	}

	weightedRanges, totalVotingPower := createWeightedRanges(votingPower)
	// select producers, with replacement
	for i := uint64(0); i < producerCount; i++ {
		/*
			Random must be in [1, totalVotingPower] to avoid the situation such as
			2 validators with 1 staking power each.
			Weighted range will look like (1, 2)
			Rolling inclusive will have a range of 0-2, making validator with staking power 1 chance of selection = 66%
		*/
		targetWeight := randomRangeInclusive(1, totalVotingPower, r)
		index := binarySearch(weightedRanges, targetWeight)
		selectedProducers = append(selectedProducers, spanEligibleValidators[index].ValId)
	}

	return selectedProducers[:producerCount]
}

func binarySearch(array []uint64, search uint64) int {
	if len(array) == 0 {
		return -1
	}

	l := 0
	r := len(array) - 1

	for l < r {
		mid := (l + r) / 2
		if array[mid] >= search {
			r = mid
		} else {
			l = mid + 1
		}
	}

	return l
}

// randomRangeInclusive produces unbiased pseudo random in the range [min, max]. Uses rand.Uint64() and can be seeded beforehand.
func randomRangeInclusive(minValue uint64, maxValue uint64, rand *rand.Rand) uint64 {
	if maxValue <= minValue {
		return maxValue
	}

	rangeLength := maxValue - minValue + 1
	maxAllowedValue := math.MaxUint64 - math.MaxUint64%rangeLength - 1
	randomValue := rand.Uint64()

	// reject anything beyond the reminder to avoid bias
	for randomValue >= maxAllowedValue {
		randomValue = rand.Uint64()
	}

	return minValue + randomValue%rangeLength
}

// createWeightedRanges converts array [1, 2, 3] into cumulative form [1, 3, 6]
func createWeightedRanges(weights []uint64) ([]uint64, uint64) {
	weightedRanges := make([]uint64, len(weights))

	totalWeight := uint64(0)
	for i := 0; i < len(weightedRanges); i++ {
		totalWeight += weights[i]
		weightedRanges[i] = totalWeight
	}

	return weightedRanges, totalWeight
}
