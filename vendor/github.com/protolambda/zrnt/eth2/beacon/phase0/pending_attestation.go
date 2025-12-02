package phase0

import (
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type PendingAttestation struct {
	AggregationBits AttestationBits       `json:"aggregation_bits" yaml:"aggregation_bits"`
	Data            AttestationData       `json:"data" yaml:"data"`
	InclusionDelay  common.Slot           `json:"inclusion_delay" yaml:"inclusion_delay"`
	ProposerIndex   common.ValidatorIndex `json:"proposer_index" yaml:"proposer_index"`
}

func (a *PendingAttestation) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&a.AggregationBits), &a.Data, &a.InclusionDelay, &a.ProposerIndex)
}

func (a *PendingAttestation) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&a.AggregationBits), &a.Data, a.InclusionDelay, a.ProposerIndex)
}

func (a *PendingAttestation) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&a.AggregationBits), &a.Data, a.InclusionDelay, a.ProposerIndex)
}

func (a *PendingAttestation) FixedLength(*common.Spec) uint64 {
	return 0
}

func (a *PendingAttestation) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&a.AggregationBits), &a.Data, a.InclusionDelay, a.ProposerIndex)
}

func (att *PendingAttestation) View(spec *common.Spec) *PendingAttestationView {
	bits := att.AggregationBits.View(spec)
	t := ContainerType("PendingAttestation", []FieldDef{
		{"aggregation_bits", bits.Type()},
		{"data", AttestationDataType},
		{"inclusion_delay", common.SlotType},
		{"proposer_index", common.ValidatorIndexType},
	})
	c, _ := t.FromFields(
		bits,
		att.Data.View(),
		Uint64View(att.InclusionDelay),
		Uint64View(att.ProposerIndex),
	)
	return &PendingAttestationView{c}
}

type AttestationData struct {
	Slot  common.Slot           `json:"slot" yaml:"slot"`
	Index common.CommitteeIndex `json:"index" yaml:"index"`

	// LMD GHOST vote
	BeaconBlockRoot common.Root `json:"beacon_block_root" yaml:"beacon_block_root"`

	// FFG vote
	Source common.Checkpoint `json:"source" yaml:"source"`
	Target common.Checkpoint `json:"target" yaml:"target"`
}

func (a *AttestationData) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&a.Slot, &a.Index, &a.BeaconBlockRoot, &a.Source, &a.Target)
}

func (a *AttestationData) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(a.Slot, a.Index, &a.BeaconBlockRoot, &a.Source, &a.Target)
}

func (a *AttestationData) ByteLength() uint64 {
	return AttestationDataType.TypeByteLength()
}

func (*AttestationData) FixedLength() uint64 {
	return AttestationDataType.TypeByteLength()
}

func (p *AttestationData) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(p.Slot, p.Index, p.BeaconBlockRoot, &p.Source, &p.Target)
}

func (data *AttestationData) View() *AttestationDataView {
	rv := RootView(data.BeaconBlockRoot)
	c, _ := AttestationDataType.FromFields(
		Uint64View(data.Slot),
		Uint64View(data.Index),
		&rv,
		data.Source.View(),
		data.Target.View(),
	)
	return &AttestationDataView{c}
}

var AttestationDataType = ContainerType("AttestationData", []FieldDef{
	{"slot", common.SlotType},
	{"index", common.CommitteeIndexType},
	// LMD GHOST vote
	{"beacon_block_root", RootType},
	// FFG vote
	{"source", common.CheckpointType},
	{"target", common.CheckpointType},
})

type AttestationDataView struct{ *ContainerView }

func (v *AttestationDataView) Raw() (*AttestationData, error) {
	fields, err := v.FieldValues()
	slot, err := common.AsSlot(fields[0], err)
	comm, err := common.AsCommitteeIndex(fields[1], err)
	root, err := AsRoot(fields[2], err)
	source, err := common.AsCheckPoint(fields[3], err)
	target, err := common.AsCheckPoint(fields[4], err)
	if err != nil {
		return nil, err
	}
	rawSource, err := source.Raw()
	if err != nil {
		return nil, err
	}
	rawTarget, err := target.Raw()
	if err != nil {
		return nil, err
	}
	return &AttestationData{
		Slot:            slot,
		Index:           comm,
		BeaconBlockRoot: root,
		Source:          rawSource,
		Target:          rawTarget,
	}, nil
}

func AsAttestationData(v View, err error) (*AttestationDataView, error) {
	c, err := AsContainer(v, err)
	return &AttestationDataView{c}, err
}

func PendingAttestationType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("PendingAttestation", []FieldDef{
		{"aggregation_bits", AttestationBitsType(spec)},
		{"data", AttestationDataType},
		{"inclusion_delay", common.SlotType},
		{"proposer_index", common.ValidatorIndexType},
	})
}

type PendingAttestationView struct{ *ContainerView }

func (v *PendingAttestationView) Raw() (*PendingAttestation, error) {
	// load aggregation bits
	fields, err := v.FieldValues()
	bits, err := AsAttestationBits(fields[0], err)
	data, err := AsAttestationData(fields[1], err)
	delay, err := common.AsSlot(fields[2], err)
	proposerIndex, err := common.AsValidatorIndex(fields[3], err)
	if err != nil {
		return nil, err
	}
	rawBits, err := bits.Raw()
	if err != nil {
		return nil, err
	}
	rawData, err := data.Raw()
	if err != nil {
		return nil, err
	}
	return &PendingAttestation{
		AggregationBits: rawBits,
		Data:            *rawData,
		InclusionDelay:  delay,
		ProposerIndex:   proposerIndex,
	}, nil
}

func AsPendingAttestation(v View, err error) (*PendingAttestationView, error) {
	c, err := AsContainer(v, err)
	return &PendingAttestationView{c}, err
}

type PendingAttestations []*PendingAttestation

func (a *PendingAttestations) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, &PendingAttestation{})
		return spec.Wrap((*a)[i])
	}, 0, uint64(spec.MAX_ATTESTATIONS)*uint64(spec.SLOTS_PER_EPOCH))
}

func (a PendingAttestations) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return spec.Wrap(a[i])
	}, 0, uint64(len(a)))
}

func (p PendingAttestations) ByteLength(spec *common.Spec) (out uint64) {
	for _, a := range p {
		out += a.ByteLength(spec) + codec.OFFSET_SIZE
	}
	return
}

func (a *PendingAttestations) FixedLength(*common.Spec) uint64 {
	return 0 // it's a list, no fixed length
}

func (li PendingAttestations) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return spec.Wrap(li[i])
		}
		return nil
	}, length, uint64(spec.MAX_ATTESTATIONS)*uint64(spec.SLOTS_PER_EPOCH))
}

func PendingAttestationsType(spec *common.Spec) ListTypeDef {
	return ComplexListType(PendingAttestationType(spec), uint64(spec.MAX_ATTESTATIONS)*uint64(spec.SLOTS_PER_EPOCH))
}

type PendingAttestationsView struct{ *ComplexListView }

func AsPendingAttestations(v View, err error) (*PendingAttestationsView, error) {
	c, err := AsComplexList(v, err)
	return &PendingAttestationsView{c}, err
}
