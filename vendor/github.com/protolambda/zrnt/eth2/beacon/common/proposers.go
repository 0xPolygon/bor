package common

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/protolambda/zrnt/eth2/util/hashing"
)

type ProposersEpoch struct {
	Spec  *Spec
	Epoch Epoch
	// Proposers is a slice of SLOTS_PER_EPOCH proposer indices for the epoch
	Proposers []ValidatorIndex

	CommitteesPerSlot uint64
}

func (epc *ProposersEpoch) GetBeaconProposer(slot Slot) (ValidatorIndex, error) {
	epoch := epc.Spec.SlotToEpoch(slot)
	if epoch != epc.Epoch {
		return 0, fmt.Errorf("expected epoch %d for beacon proposer lookup, but lookup was at slot %d (epoch %d)", epc.Epoch, slot, epoch)
	}
	return epc.Proposers[slot%epc.Spec.SLOTS_PER_EPOCH], nil
}

func ComputeProposers(spec *Spec, state BeaconState, epoch Epoch, active []ValidatorIndex) (*ProposersEpoch, error) {
	if len(active) == 0 {
		return nil, errors.New("no active validators available to compute proposers")
	}
	proposers := make([]ValidatorIndex, spec.SLOTS_PER_EPOCH, spec.SLOTS_PER_EPOCH)
	mixes, err := state.RandaoMixes()
	if err != nil {
		return nil, err
	}
	startSlot, err := spec.EpochStartSlot(epoch)
	if err != nil {
		return nil, err
	}
	vals, err := state.Validators()
	if err != nil {
		return nil, err
	}

	hFn := hashing.GetHashFn()
	// compute beacon proposers
	{
		epochSeed, err := GetSeed(spec, mixes, epoch, DOMAIN_BEACON_PROPOSER)
		if err != nil {
			return nil, err
		}
		var buf [32 + 8]byte
		copy(buf[0:32], epochSeed[:])
		for i := Slot(0); i < spec.SLOTS_PER_EPOCH; i++ {
			binary.LittleEndian.PutUint64(buf[32:], uint64(startSlot+i))
			seed := hFn(buf[:])
			proposer, err := ComputeProposerIndex(spec, vals, active, seed)
			if err != nil {
				return nil, err
			}
			proposers[i] = proposer
		}
	}

	validatorsPerSlot := uint64(len(active)) / uint64(spec.SLOTS_PER_EPOCH)
	committeesPerSlot := validatorsPerSlot / uint64(spec.TARGET_COMMITTEE_SIZE)

	return &ProposersEpoch{
		Spec:              spec,
		Epoch:             epoch,
		Proposers:         proposers,
		CommitteesPerSlot: committeesPerSlot,
	}, nil
}

func ComputeProposerIndex(spec *Spec, registry ValidatorRegistry, active []ValidatorIndex, seed Root) (ValidatorIndex, error) {
	if len(active) == 0 {
		return 0, errors.New("no active validators available to compute proposer")
	}
	var buf [32 + 8]byte
	copy(buf[0:32], seed[:])

	hFn := hashing.GetHashFn()
	for i := uint64(0); i < 1000; i++ {
		binary.LittleEndian.PutUint64(buf[32:], i)
		h := hFn(buf[:])
		for j := uint64(0); j < 32; j++ {
			randomByte := h[j]
			absI := ValidatorIndex(((i << 5) | j) % uint64(len(active)))
			shuffledI := PermuteIndex(uint8(spec.SHUFFLE_ROUND_COUNT), absI, uint64(len(active)), seed)
			candidateIndex := active[int(shuffledI)]
			validator, err := registry.Validator(candidateIndex)
			if err != nil {
				return 0, err
			}
			effectiveBalance, err := validator.EffectiveBalance()
			if err != nil {
				return 0, err
			}
			if effectiveBalance*0xff >= spec.MAX_EFFECTIVE_BALANCE*Gwei(randomByte) {
				return candidateIndex, nil
			}
		}
	}
	return 0, errors.New("random (but balance-biased) infinite scrolling should always find a proposer")
}
