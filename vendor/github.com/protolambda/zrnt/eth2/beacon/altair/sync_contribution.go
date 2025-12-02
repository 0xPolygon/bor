package altair

import (
	"errors"
	"fmt"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type SyncCommitteeContribution struct {
	// Slot to which this contribution pertains
	Slot common.Slot `yaml:"slot" json:"slot"`
	// Block root for this contribution
	BeaconBlockRoot common.Root `yaml:"beacon_block_root" json:"beacon_block_root"`
	// The subcommittee this contribution pertains to out of the broader sync committee
	SubcommitteeIndex Uint64View `yaml:"subcommittee_index" json:"subcommittee_index"`
	// A bit is set if a signature from the validator at the corresponding
	// index in the subcommittee is present in the aggregate `signature`.
	AggregationBits SyncCommitteeSubnetBits `yaml:"aggregation_bits" json:"aggregation_bits"`
	// Signature by the validator(s) over the block root of `slot`
	Signature common.BLSSignature `yaml:"signature" json:"signature"`
}

func SyncCommitteeContributionType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("SyncCommitteeContribution", []FieldDef{
		{"slot", common.SlotType},
		{"beacon_block_root", RootType},
		{"subcommittee_index", Uint64Type},
		{"aggregation_bits", SyncCommitteeSubnetBitsType(spec)},
		{"signature", common.BLSSignatureType},
	})
}

func (sc *SyncCommitteeContribution) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(
		&sc.Slot,
		&sc.BeaconBlockRoot,
		&sc.SubcommitteeIndex,
		spec.Wrap(&sc.AggregationBits),
		&sc.Signature,
	)
}

func (sc *SyncCommitteeContribution) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.FixedLenContainer(
		&sc.Slot,
		&sc.BeaconBlockRoot,
		&sc.SubcommitteeIndex,
		spec.Wrap(&sc.AggregationBits),
		&sc.Signature,
	)
}

func (sc *SyncCommitteeContribution) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(
		&sc.Slot,
		&sc.BeaconBlockRoot,
		&sc.SubcommitteeIndex,
		spec.Wrap(&sc.AggregationBits),
		&sc.Signature,
	)
}

func (sc *SyncCommitteeContribution) FixedLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(
		&sc.Slot,
		&sc.BeaconBlockRoot,
		&sc.SubcommitteeIndex,
		spec.Wrap(&sc.AggregationBits),
		&sc.Signature,
	)
}

func (sc *SyncCommitteeContribution) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(
		&sc.Slot,
		&sc.BeaconBlockRoot,
		&sc.SubcommitteeIndex,
		spec.Wrap(&sc.AggregationBits),
		&sc.Signature,
	)
}

func (sc *SyncCommitteeContribution) VerifySignature(spec *common.Spec, subcommitteePubkeys []*common.CachedPubkey, domFn common.BLSDomainFn) error {
	pubkeys := make([]*blsu.Pubkey, 0, len(subcommitteePubkeys))
	for i, pub := range subcommitteePubkeys {
		if sc.AggregationBits.GetBit(uint64(i)) {
			p, err := pub.Pubkey()
			if err != nil {
				return fmt.Errorf("found invalid pubkey in cache")
			}
			pubkeys = append(pubkeys, p)
		}
	}
	dom, err := domFn(common.DOMAIN_SYNC_COMMITTEE, spec.SlotToEpoch(sc.Slot))
	if err != nil {
		return err
	}
	signingRoot := common.ComputeSigningRoot(sc.BeaconBlockRoot, dom)
	sig, err := sc.Signature.Signature()
	if err != nil {
		return fmt.Errorf("failed to deserialize and sub-group check sync committee contribution signature: %v", err)
	}
	if !blsu.Eth2FastAggregateVerify(pubkeys, signingRoot[:], sig) {
		return errors.New("could not verify BLS signature for sync committee contribution")
	}
	return nil
}

type SyncCommitteeContributionView struct {
	*ContainerView
}

func AsSyncCommitteeContribution(v View, err error) (*SyncCommitteeContributionView, error) {
	c, err := AsContainer(v, err)
	return &SyncCommitteeContributionView{c}, err
}
