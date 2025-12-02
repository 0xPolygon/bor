package altair

import (
	"encoding/binary"
	"fmt"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/util/hashing"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type ContributionAndProof struct {
	AggregatorIndex common.ValidatorIndex     `yaml:"aggregator_index" json:"aggregator_index"`
	Contribution    SyncCommitteeContribution `yaml:"contribution" json:"contribution"`
	SelectionProof  common.BLSSignature       `yaml:"selection_proof" json:"selection_proof"`
}

func ContributionAndProofType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("ContributionAndProof", []FieldDef{
		{"aggregator_index", common.ValidatorIndexType},
		{"contribution", SyncCommitteeContributionType(spec)},
		{"selection_proof", common.BLSSignatureType},
	})
}

func (cnp *ContributionAndProof) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(
		&cnp.AggregatorIndex,
		spec.Wrap(&cnp.Contribution),
		&cnp.SelectionProof,
	)
}

func (cnp *ContributionAndProof) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.FixedLenContainer(
		&cnp.AggregatorIndex,
		spec.Wrap(&cnp.Contribution),
		&cnp.SelectionProof,
	)
}

func (cnp *ContributionAndProof) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(
		&cnp.AggregatorIndex,
		spec.Wrap(&cnp.Contribution),
		&cnp.SelectionProof,
	)
}

func (cnp *ContributionAndProof) FixedLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(
		&cnp.AggregatorIndex,
		spec.Wrap(&cnp.Contribution),
		&cnp.SelectionProof,
	)
}

func (cnp *ContributionAndProof) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(
		&cnp.AggregatorIndex,
		spec.Wrap(&cnp.Contribution),
		&cnp.SelectionProof,
	)
}

type ContributionAndProofView struct {
	*ContainerView
}

func AsContributionAndProof(v View, err error) (*ContributionAndProofView, error) {
	c, err := AsContainer(v, err)
	return &ContributionAndProofView{c}, err
}

func SignedContributionAndProofType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("SignedContributionAndProof", []FieldDef{
		{"message", ContributionAndProofType(spec)},
		{"signature", common.BLSSignatureType},
	})
}

type SignedContributionAndProof struct {
	Message   ContributionAndProof `yaml:"message" json:"message"`
	Signature common.BLSSignature  `yaml:"signature" json:"signature"`
}

func (b *SignedContributionAndProof) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&b.Message), &b.Signature)
}

func (b *SignedContributionAndProof) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&b.Message), &b.Signature)
}

func (b *SignedContributionAndProof) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&b.Message), &b.Signature)
}

func (a *SignedContributionAndProof) FixedLength(*common.Spec) uint64 {
	return 0
}

func (b *SignedContributionAndProof) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&b.Message), b.Signature)
}

// VerifySignature verifies the outer Signature ONLY. This does not verify the selection proof or contribution contents.
func (b *SignedContributionAndProof) VerifySignature(spec *common.Spec, epc *common.EpochsContext, domainFn common.BLSDomainFn) error {
	dom, err := domainFn(common.DOMAIN_CONTRIBUTION_AND_PROOF, spec.SlotToEpoch(b.Message.Contribution.Slot))
	if err != nil {
		return err
	}
	sigRoot := common.ComputeSigningRoot(b.Message.HashTreeRoot(spec, tree.GetHashFn()), dom)
	pub, ok := epc.ValidatorPubkeyCache.Pubkey(b.Message.AggregatorIndex)
	if !ok {
		return fmt.Errorf("could not fetch pubkey for aggregator %d", b.Message.AggregatorIndex)
	}
	blsPub, err := pub.Pubkey()
	if err != nil {
		return err
	}
	sig, err := b.Signature.Signature()
	if err != nil {
		return err
	}
	if !blsu.Verify(blsPub, sigRoot[:], sig) {
		return fmt.Errorf("invalid contribution and proof signature %s", b.Signature)
	}
	return nil
}

type SignedContributionAndProofView struct {
	*ContainerView
}

func AsSignedContributionAndProof(v View, err error) (*SignedContributionAndProofView, error) {
	c, err := AsContainer(v, err)
	return &SignedContributionAndProofView{c}, err
}

func IsSyncCommitteeAggregator(spec *common.Spec, sig common.BLSSignature) bool {
	modulo := uint64(spec.SYNC_COMMITTEE_SIZE) / common.SYNC_COMMITTEE_SUBNET_COUNT / common.TARGET_AGGREGATORS_PER_SYNC_SUBCOMMITTEE
	if modulo < 1 {
		modulo = 1
	}
	sigHash := hashing.Hash(sig[:])
	return binary.LittleEndian.Uint64(sigHash[0:8])%modulo == 0
}

func SyncAggregatorSelectionSigningRoot(spec *common.Spec, domainFn common.BLSDomainFn, slot common.Slot, subcommitteeIndex uint64) (common.Root, error) {
	domain, err := domainFn(common.DOMAIN_SYNC_COMMITTEE_SELECTION_PROOF, spec.SlotToEpoch(slot))
	if err != nil {
		return common.Root{}, err
	}
	singingData := SyncAggregatorSelectionData{Slot: slot, SubcommitteeIndex: Uint64View(subcommitteeIndex)}
	return common.ComputeSigningRoot(singingData.HashTreeRoot(tree.GetHashFn()), domain), nil
}

func ValidateSyncAggregatorSelectionProof(spec *common.Spec, epc *common.EpochsContext, domainFn common.BLSDomainFn,
	aggregator common.ValidatorIndex, selectionProof common.BLSSignature, slot common.Slot, subcommitteeIndex uint64) error {
	sigRoot, err := SyncAggregatorSelectionSigningRoot(spec, domainFn, slot, subcommitteeIndex)
	if err != nil {
		return err
	}
	pub, ok := epc.ValidatorPubkeyCache.Pubkey(aggregator)
	if !ok {
		return fmt.Errorf("could not fetch pubkey for aggregator %d", aggregator)
	}
	blsPub, err := pub.Pubkey()
	if err != nil {
		return fmt.Errorf("could not deserialize cached pubkey: %v", err)
	}
	sig, err := selectionProof.Signature()
	if err != nil {
		return fmt.Errorf("failed to deserialize and sub-group check selection proof signature")
	}
	if !blsu.Verify(blsPub, sigRoot[:], sig) {
		return fmt.Errorf("invalid sync agg selection proof signature %s for aggregator %d at slot %d subnet %d",
			selectionProof, aggregator, slot, subcommitteeIndex)
	}
	return nil
}
