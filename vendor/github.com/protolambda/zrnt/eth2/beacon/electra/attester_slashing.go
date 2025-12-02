package electra

import (
	"encoding/json"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type AttesterSlashing struct {
	// [Modified in Electra:EIP7549]
	Attestation1 IndexedAttestation `json:"attestation_1" yaml:"attestation_1"`
	// [Modified in Electra:EIP7549]
	Attestation2 IndexedAttestation `json:"attestation_2" yaml:"attestation_2"`
}

func (a *AttesterSlashing) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&a.Attestation1), spec.Wrap(&a.Attestation2))
}

func (a *AttesterSlashing) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&a.Attestation1), spec.Wrap(&a.Attestation2))
}

func (a *AttesterSlashing) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&a.Attestation1), spec.Wrap(&a.Attestation2))
}

func (a *AttesterSlashing) FixedLength(*common.Spec) uint64 {
	return 0
}

func (a *AttesterSlashing) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&a.Attestation1), spec.Wrap(&a.Attestation2))
}

func BlockAttesterSlashingsType(spec *common.Spec) ListTypeDef {
	return ListType(AttesterSlashingType(spec), uint64(spec.MAX_ATTESTER_SLASHINGS))
}

func AttesterSlashingType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("AttesterSlashing", []FieldDef{
		{"attestation_1", IndexedAttestationType(spec)},
		{"attestation_2", IndexedAttestationType(spec)},
	})
}

type AttesterSlashings []AttesterSlashing

func (a *AttesterSlashings) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, AttesterSlashing{})
		return spec.Wrap(&((*a)[i]))
	}, 0, uint64(spec.MAX_ATTESTER_SLASHINGS_ELECTRA))
}

func (a AttesterSlashings) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return spec.Wrap(&a[i])
	}, 0, uint64(len(a)))
}

func (a AttesterSlashings) ByteLength(spec *common.Spec) (out uint64) {
	for _, v := range a {
		out += v.ByteLength(spec) + codec.OFFSET_SIZE
	}
	return
}

func (a *AttesterSlashings) FixedLength(*common.Spec) uint64 {
	return 0
}

func (li AttesterSlashings) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return spec.Wrap(&li[i])
		}
		return nil
	}, length, uint64(spec.MAX_ATTESTER_SLASHINGS_ELECTRA))
}

func (li AttesterSlashings) MarshalJSON() ([]byte, error) {
	if li == nil {
		return json.Marshal([]AttesterSlashing{}) // encode as empty list, not null
	}
	return json.Marshal([]AttesterSlashing(li))
}
