package electra

import (
	"encoding/json"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type SingleAttestation struct {
	CommitteeIndex common.CommitteeIndex  `json:"committee_index" yaml:"committee_index"`
	AttesterIndex  common.ValidatorIndex  `json:"attester_index" yaml:"attester_index"`
	Data           phase0.AttestationData `json:"data" yaml:"data"`
	Signature      common.BLSSignature    `json:"signature" yaml:"signature"`
}

func (a *SingleAttestation) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&a.CommitteeIndex, &a.AttesterIndex, &a.Data, &a.Signature)
}

func (a *SingleAttestation) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&a.CommitteeIndex, &a.AttesterIndex, &a.Data, &a.Signature)
}

func (a *SingleAttestation) ByteLength() uint64 {
	return SingleAttestationType.TypeByteLength()
}

func (*SingleAttestation) FixedLength() uint64 {
	return SingleAttestationType.TypeByteLength()
}

func (a *SingleAttestation) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(&a.CommitteeIndex, &a.AttesterIndex, &a.Data, &a.Signature)
}

var SingleAttestationType = ContainerType("SingleAttestation", []FieldDef{
	{"committee_index", common.CommitteeIndexType},
	{"attester_index", common.ValidatorIndexType},
	{"data", phase0.AttestationDataType},
	{"signature", common.BLSSignatureType},
})

type Attestation struct {
	// [Modified in Electra:EIP7549]
	AggregationBits AttestationBits        `json:"aggregation_bits" yaml:"aggregation_bits"`
	Data            phase0.AttestationData `json:"data" yaml:"data"`
	Signature       common.BLSSignature    `json:"signature" yaml:"signature"`
	// [New in Electra:EIP7549]
	CommitteeBits CommitteeBits `json:"committee_bits" yaml:"committee_bits"`
}

func (a *Attestation) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&a.AggregationBits), &a.Data, &a.Signature, spec.Wrap(&a.CommitteeBits))
}

func (a *Attestation) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&a.AggregationBits), &a.Data, &a.Signature, spec.Wrap(&a.CommitteeBits))
}

func (a *Attestation) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&a.AggregationBits), &a.Data, &a.Signature, spec.Wrap(&a.CommitteeBits))
}

func (a *Attestation) FixedLength(*common.Spec) uint64 {
	return 0
}

func (a *Attestation) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&a.AggregationBits), &a.Data, a.Signature, spec.Wrap(&a.CommitteeBits))
}

func BlockAttestationsType(spec *common.Spec) ListTypeDef {
	return ListType(AttestationType(spec), uint64(spec.MAX_ATTESTATIONS_ELECTRA))
}

func AttestationType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("Attestation", []FieldDef{
		{"aggregation_bits", AttestationBitsType(spec)},
		{"data", phase0.AttestationDataType},
		{"signature", common.BLSSignatureType},
		{"committee_bits", CommitteeBitsType(spec)},
	})
}

type IndexedAttestation struct {
	// [Modified in Electra:EIP7549]
	// List[ValidatorIndex, MAX_VALIDATORS_PER_COMMITTEE * MAX_COMMITTEES_PER_SLOT]
	AttestingIndices common.SlotCommitteeIndices `json:"attesting_indices" yaml:"attesting_indices"`
	Data             phase0.AttestationData      `json:"data" yaml:"data"`
	Signature        common.BLSSignature         `json:"signature" yaml:"signature"`
}

func (p *IndexedAttestation) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&p.AttestingIndices), &p.Data, &p.Signature)
}

func (a *IndexedAttestation) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&a.AttestingIndices), &a.Data, &a.Signature)
}

func (a *IndexedAttestation) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&a.AttestingIndices), &a.Data, &a.Signature)
}

func (*IndexedAttestation) FixedLength(*common.Spec) uint64 {
	return 0
}

func (p *IndexedAttestation) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&p.AttestingIndices), &p.Data, p.Signature)
}

func IndexedAttestationType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("IndexedAttestation", []FieldDef{
		{"attesting_indices", common.SlotCommitteeIndicesType(spec)},
		{"data", phase0.AttestationDataType},
		{"signature", common.BLSSignatureType},
	})
}

type Attestations []Attestation

func (a *Attestations) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, Attestation{})
		return spec.Wrap(&((*a)[i]))
	}, 0, uint64(spec.MAX_ATTESTATIONS_ELECTRA))
}

func (a Attestations) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return spec.Wrap(&a[i])
	}, 0, uint64(len(a)))
}

func (a Attestations) ByteLength(spec *common.Spec) (out uint64) {
	for _, v := range a {
		out += v.ByteLength(spec) + codec.OFFSET_SIZE
	}
	return
}

func (a *Attestations) FixedLength(*common.Spec) uint64 {
	return 0
}

func (li Attestations) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return spec.Wrap(&li[i])
		}
		return nil
	}, length, uint64(spec.MAX_ATTESTATIONS_ELECTRA))
}

func (li Attestations) MarshalJSON() ([]byte, error) {
	if li == nil {
		return json.Marshal([]Attestation{}) // encode as empty list, not null
	}
	return json.Marshal([]Attestation(li))
}
