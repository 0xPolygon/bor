package phase0

import (
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type SignedBeaconBlock struct {
	Message   BeaconBlock         `json:"message" yaml:"message"`
	Signature common.BLSSignature `json:"signature" yaml:"signature"`
}

var _ common.EnvelopeBuilder = (*SignedBeaconBlock)(nil)

func (b *SignedBeaconBlock) Envelope(spec *common.Spec, digest common.ForkDigest) *common.BeaconBlockEnvelope {
	header := b.Message.Header(spec)
	return &common.BeaconBlockEnvelope{
		ForkDigest:        digest,
		BeaconBlockHeader: *header,
		Body:              &b.Message.Body,
		BlockRoot:         header.HashTreeRoot(tree.GetHashFn()),
		Signature:         b.Signature,
	}
}

func (b *SignedBeaconBlock) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&b.Message), &b.Signature)
}

func (b *SignedBeaconBlock) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&b.Message), &b.Signature)
}

func (b *SignedBeaconBlock) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&b.Message), &b.Signature)
}

func (a *SignedBeaconBlock) FixedLength(*common.Spec) uint64 {
	return 0
}

func (b *SignedBeaconBlock) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&b.Message), b.Signature)
}

func (block *SignedBeaconBlock) SignedHeader(spec *common.Spec) *common.SignedBeaconBlockHeader {
	return &common.SignedBeaconBlockHeader{
		Message:   *block.Message.Header(spec),
		Signature: block.Signature,
	}
}

type BeaconBlock struct {
	Slot          common.Slot           `json:"slot" yaml:"slot"`
	ProposerIndex common.ValidatorIndex `json:"proposer_index" yaml:"proposer_index"`
	ParentRoot    common.Root           `json:"parent_root" yaml:"parent_root"`
	StateRoot     common.Root           `json:"state_root" yaml:"state_root"`
	Body          BeaconBlockBody       `json:"body" yaml:"body"`
}

func (b *BeaconBlock) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(&b.Slot, &b.ProposerIndex, &b.ParentRoot, &b.StateRoot, spec.Wrap(&b.Body))
}

func (b *BeaconBlock) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(&b.Slot, &b.ProposerIndex, &b.ParentRoot, &b.StateRoot, spec.Wrap(&b.Body))
}

func (b *BeaconBlock) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(&b.Slot, &b.ProposerIndex, &b.ParentRoot, &b.StateRoot, spec.Wrap(&b.Body))
}

func (a *BeaconBlock) FixedLength(*common.Spec) uint64 {
	return 0
}

func (b *BeaconBlock) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(b.Slot, b.ProposerIndex, b.ParentRoot, b.StateRoot, spec.Wrap(&b.Body))
}

func BeaconBlockType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("BeaconBlock", []FieldDef{
		{"slot", common.SlotType},
		{"proposer_index", common.ValidatorIndexType},
		{"parent_root", RootType},
		{"state_root", RootType},
		{"body", BeaconBlockBodyType(spec)},
	})
}

func SignedBeaconBlockType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("SignedBeaconBlock", []FieldDef{
		{"message", BeaconBlockType(spec)},
		{"signature", common.BLSSignatureType},
	})
}

func (block *BeaconBlock) Header(spec *common.Spec) *common.BeaconBlockHeader {
	return &common.BeaconBlockHeader{
		Slot:          block.Slot,
		ProposerIndex: block.ProposerIndex,
		ParentRoot:    block.ParentRoot,
		StateRoot:     block.StateRoot,
		BodyRoot:      block.Body.HashTreeRoot(spec, tree.GetHashFn()),
	}
}

type BeaconBlockBody struct {
	RandaoReveal common.BLSSignature `json:"randao_reveal" yaml:"randao_reveal"`
	Eth1Data     common.Eth1Data     `json:"eth1_data" yaml:"eth1_data"`
	Graffiti     common.Root         `json:"graffiti" yaml:"graffiti"`

	ProposerSlashings ProposerSlashings `json:"proposer_slashings" yaml:"proposer_slashings"`
	AttesterSlashings AttesterSlashings `json:"attester_slashings" yaml:"attester_slashings"`
	Attestations      Attestations      `json:"attestations" yaml:"attestations"`
	Deposits          Deposits          `json:"deposits" yaml:"deposits"`
	VoluntaryExits    VoluntaryExits    `json:"voluntary_exits" yaml:"voluntary_exits"`
}

func (b *BeaconBlockBody) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(
		&b.RandaoReveal, &b.Eth1Data,
		&b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
	)
}

func (b *BeaconBlockBody) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(
		&b.RandaoReveal, &b.Eth1Data,
		&b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
	)
}

func (b *BeaconBlockBody) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(
		&b.RandaoReveal, &b.Eth1Data,
		&b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
	)
}

func (a *BeaconBlockBody) FixedLength(*common.Spec) uint64 {
	return 0
}

func (b *BeaconBlockBody) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(
		b.RandaoReveal, &b.Eth1Data,
		b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
	)
}

func (b *BeaconBlockBody) CheckLimits(spec *common.Spec) error {
	if x := uint64(len(b.ProposerSlashings)); x > uint64(spec.MAX_PROPOSER_SLASHINGS) {
		return fmt.Errorf("too many proposer slashings: %d", x)
	}
	if x := uint64(len(b.AttesterSlashings)); x > uint64(spec.MAX_ATTESTER_SLASHINGS) {
		return fmt.Errorf("too many attester slashings: %d", x)
	}
	if x := uint64(len(b.Attestations)); x > uint64(spec.MAX_ATTESTATIONS) {
		return fmt.Errorf("too many attestations: %d", x)
	}
	if x := uint64(len(b.Deposits)); x > uint64(spec.MAX_DEPOSITS) {
		return fmt.Errorf("too many deposits: %d", x)
	}
	if x := uint64(len(b.VoluntaryExits)); x > uint64(spec.MAX_VOLUNTARY_EXITS) {
		return fmt.Errorf("too many voluntary exits: %d", x)
	}
	return nil
}

func BeaconBlockBodyType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("BeaconBlockBody", []FieldDef{
		{"randao_reveal", common.BLSSignatureType},
		{"eth1_data", common.Eth1DataType}, // Eth1 data vote
		{"graffiti", common.Bytes32Type},   // Arbitrary data
		// Operations
		{"proposer_slashings", BlockProposerSlashingsType(spec)},
		{"attester_slashings", BlockAttesterSlashingsType(spec)},
		{"attestations", BlockAttestationsType(spec)},
		{"deposits", BlockDepositsType(spec)},
		{"voluntary_exits", BlockVoluntaryExitsType(spec)},
	})
}
