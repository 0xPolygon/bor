package common

import (
	"encoding/binary"

	. "github.com/protolambda/zrnt/eth2/util/hashing"
)

// Prepare the randao mix for the given epoch by copying over the mix from the previous epoch.
func PrepareRandao(mixes RandaoMixes, epoch Epoch) error {
	prev, err := mixes.GetRandomMix(epoch.Previous())
	if err != nil {
		return err
	}
	return mixes.SetRandomMix(epoch, prev)
}

func GetSeed(spec *Spec, mixes RandaoMixes, epoch Epoch, domainType BLSDomainType) (Root, error) {
	buf := make([]byte, 4+8+32)

	// domain type
	copy(buf[0:4], domainType[:])

	// epoch
	binary.LittleEndian.PutUint64(buf[4:4+8], uint64(epoch))

	// Avoid underflow
	mix, err := mixes.GetRandomMix(epoch + spec.EPOCHS_PER_HISTORICAL_VECTOR - spec.MIN_SEED_LOOKAHEAD - 1)
	if err != nil {
		return Root{}, err
	}
	copy(buf[4+8:], mix[:])

	return Hash(buf), nil
}
