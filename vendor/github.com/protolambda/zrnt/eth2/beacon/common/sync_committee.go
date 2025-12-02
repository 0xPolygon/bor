package common

import (
	"encoding/binary"
	"errors"
	"fmt"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/zrnt/eth2/util/hashing"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

func SyncCommitteePubkeysType(spec *Spec) *ComplexVectorTypeDef {
	return ComplexVectorType(BLSPubkeyType, uint64(spec.SYNC_COMMITTEE_SIZE))
}

type SyncCommitteePubkeys []BLSPubkey

func (li *SyncCommitteePubkeys) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	*li = make([]BLSPubkey, spec.SYNC_COMMITTEE_SIZE, spec.SYNC_COMMITTEE_SIZE)
	return dr.Vector(func(i uint64) codec.Deserializable {
		return &(*li)[i]
	}, BLSPubkeyType.Size, uint64(spec.SYNC_COMMITTEE_SIZE))
}

func (a SyncCommitteePubkeys) Serialize(spec *Spec, w *codec.EncodingWriter) error {
	return w.Vector(func(i uint64) codec.Serializable {
		return &a[i]
	}, BLSPubkeyType.Size, uint64(spec.SYNC_COMMITTEE_SIZE))
}

func (a SyncCommitteePubkeys) ByteLength(spec *Spec) uint64 {
	return uint64(spec.SYNC_COMMITTEE_SIZE) * BLSPubkeyType.Size
}

func (a *SyncCommitteePubkeys) FixedLength(spec *Spec) uint64 {
	return uint64(spec.SYNC_COMMITTEE_SIZE) * BLSPubkeyType.Size
}

func (li SyncCommitteePubkeys) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	return hFn.ComplexVectorHTR(func(i uint64) tree.HTR {
		return &li[i]
	}, uint64(spec.SYNC_COMMITTEE_SIZE))
}

type SyncCommitteePubkeysView struct {
	*ComplexVectorView
}

func (v *SyncCommitteePubkeysView) Flatten() ([]BLSPubkey, error) {
	out := make([]BLSPubkey, v.VectorLength, v.VectorLength)
	iter := v.ReadonlyIter()
	for i := 0; i < len(out); i++ {
		elem, ok, err := iter.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("expected sync committee pubkey %d, iter stopped prematurely", i)
		}
		pub, err := AsBLSPubkey(elem, nil)
		if err != nil {
			return nil, fmt.Errorf("invalid pubkey view: %v", err)
		}
		out[i] = pub
	}
	return out, nil
}

func AsSyncCommitteePubkeys(v View, err error) (*SyncCommitteePubkeysView, error) {
	c, err := AsComplexVector(v, err)
	return &SyncCommitteePubkeysView{c}, err
}

func SyncCommitteeType(spec *Spec) *ContainerTypeDef {
	return ContainerType("SyncCommittee", []FieldDef{
		{"pubkeys", SyncCommitteePubkeysType(spec)},
		{"aggregate_pubkey", BLSPubkeyType},
	})
}

type SyncCommittee struct {
	Pubkeys         SyncCommitteePubkeys `json:"pubkeys" yaml:"pubkeys"`
	AggregatePubkey BLSPubkey            `json:"aggregate_pubkey" yaml:"aggregate_pubkey"`
}

func (a *SyncCommittee) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(spec.Wrap(&a.Pubkeys), &a.AggregatePubkey)
}

func (a *SyncCommittee) Serialize(spec *Spec, w *codec.EncodingWriter) error {
	return w.FixedLenContainer(spec.Wrap(&a.Pubkeys), &a.AggregatePubkey)
}

func (a *SyncCommittee) ByteLength(spec *Spec) uint64 {
	return (uint64(spec.SYNC_COMMITTEE_SIZE) + 1) * BLSPubkeyType.Size
}

func (*SyncCommittee) FixedLength(spec *Spec) uint64 {
	return (uint64(spec.SYNC_COMMITTEE_SIZE) + 1) * BLSPubkeyType.Size
}

func (p *SyncCommittee) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(spec.Wrap(&p.Pubkeys), &p.AggregatePubkey)
}

func (p *SyncCommittee) View(spec *Spec) (*SyncCommitteeView, error) {
	elems := make([]View, len(p.Pubkeys), len(p.Pubkeys))
	for i := 0; i < len(p.Pubkeys); i++ {
		elems[i] = ViewPubkey(&p.Pubkeys[i])
	}
	pubs, err := SyncCommitteePubkeysType(spec).FromElements(elems...)
	if err != nil {
		return nil, err
	}
	return AsSyncCommittee(SyncCommitteeType(spec).FromFields(pubs, ViewPubkey(&p.AggregatePubkey)))
}

type SyncCommitteeView struct {
	*ContainerView
}

func (p *SyncCommitteeView) Pubkeys() (*SyncCommitteePubkeysView, error) {
	return AsSyncCommitteePubkeys(p.Get(0))
}

func (p *SyncCommitteeView) AggregatePubkey() (BLSPubkey, error) {
	return AsBLSPubkey(p.Get(1))
}

func AsSyncCommittee(v View, err error) (*SyncCommitteeView, error) {
	c, err := AsContainer(v, err)
	return &SyncCommitteeView{c}, err
}

func ComputeNextSyncCommittee(spec *Spec, epc *EpochsContext, state BeaconState) (*SyncCommittee, error) {
	indices, err := ComputeSyncCommitteeIndices(spec, state, epc.NextEpoch.Epoch, epc.NextEpoch.ActiveIndices)
	if err != nil {
		return nil, fmt.Errorf("failed to compute sync committee indices for next epoch: %d", epc.NextEpoch.Epoch)
	}
	return IndicesToSyncCommittee(indices, epc.ValidatorPubkeyCache)
}

func IndicesToSyncCommittee(indices []ValidatorIndex, pubCache *PubkeyCache) (*SyncCommittee, error) {
	var pubs []BLSPubkey
	var blsPubs []*blsu.Pubkey
	for _, idx := range indices {
		pub, ok := pubCache.Pubkey(idx)
		if !ok {
			return nil, fmt.Errorf("failed to get sync committee data, pubkey cache is missing pubkey for index %d", idx)
		}
		blsPub, err := pub.Pubkey()
		if err != nil {
			return nil, fmt.Errorf("pubkey cache contains invalid pubkey at index %d: %v", idx, err)
		}
		pubs = append(pubs, pub.Compressed)
		blsPubs = append(blsPubs, blsPub)
	}
	blsAggregate, err := blsu.AggregatePubkeys(blsPubs)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate sync-committee bls pubkeys")
	}
	aggregate := BLSPubkey(blsAggregate.Serialize())
	return &SyncCommittee{
		Pubkeys:         pubs,
		AggregatePubkey: aggregate,
	}, nil
}

// Return the sequence of sync committee indices (which may include duplicate indices)
// for the next sync committee, given a state at a sync committee period boundary.
//
// Note: Committee can contain duplicate indices for small validator sets (< SYNC_COMMITTEE_SIZE + 128)
func ComputeSyncCommitteeIndices(spec *Spec, state BeaconState, baseEpoch Epoch, active []ValidatorIndex) ([]ValidatorIndex, error) {
	if len(active) == 0 {
		return nil, errors.New("no active validators to compute sync committee from")
	}
	slot, err := state.Slot()
	if err != nil {
		return nil, err
	}
	if epoch := spec.SlotToEpoch(slot); baseEpoch > epoch+1 {
		return nil, fmt.Errorf("stat at slot %d (epoch %d) is not far along enough to compute sync committee data for epoch %d", slot, epoch, baseEpoch)
	}
	syncCommitteeIndices := make([]ValidatorIndex, 0, spec.SYNC_COMMITTEE_SIZE)
	mixes, err := state.RandaoMixes()
	if err != nil {
		return nil, err
	}
	periodSeed, err := GetSeed(spec, mixes, baseEpoch, DOMAIN_SYNC_COMMITTEE)
	if err != nil {
		return nil, err
	}
	vals, err := state.Validators()
	if err != nil {
		return nil, err
	}
	hFn := hashing.GetHashFn()
	var buf [32 + 8]byte
	copy(buf[0:32], periodSeed[:])
	var h [32]byte
	i := ValidatorIndex(0)
	for uint64(len(syncCommitteeIndices)) < uint64(spec.SYNC_COMMITTEE_SIZE) {
		shuffledIndex := PermuteIndex(uint8(spec.SHUFFLE_ROUND_COUNT), i%ValidatorIndex(len(active)),
			uint64(len(active)), periodSeed)
		candidateIndex := active[shuffledIndex]
		validator, err := vals.Validator(candidateIndex)
		if err != nil {
			return nil, err
		}
		effectiveBalance, err := validator.EffectiveBalance()
		if err != nil {
			return nil, err
		}
		// every 32 rounds, create a new source for randomByte
		if i%32 == 0 {
			binary.LittleEndian.PutUint64(buf[32:32+8], uint64(i/32))
			h = hFn(buf[:])
		}
		randomByte := h[i%32]
		if effectiveBalance*0xff >= spec.MAX_EFFECTIVE_BALANCE*Gwei(randomByte) {
			syncCommitteeIndices = append(syncCommitteeIndices, candidateIndex)
		}
		i += 1
	}
	return syncCommitteeIndices, nil
}
