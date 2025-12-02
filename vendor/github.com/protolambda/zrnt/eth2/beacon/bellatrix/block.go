package bellatrix

import (
	"fmt"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"

	"github.com/protolambda/zrnt/eth2/beacon/altair"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
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

	ProposerSlashings phase0.ProposerSlashings `json:"proposer_slashings" yaml:"proposer_slashings"`
	AttesterSlashings phase0.AttesterSlashings `json:"attester_slashings" yaml:"attester_slashings"`
	Attestations      phase0.Attestations      `json:"attestations" yaml:"attestations"`
	Deposits          phase0.Deposits          `json:"deposits" yaml:"deposits"`
	VoluntaryExits    phase0.VoluntaryExits    `json:"voluntary_exits" yaml:"voluntary_exits"`

	SyncAggregate altair.SyncAggregate `json:"sync_aggregate" yaml:"sync_aggregate"`

	ExecutionPayload ExecutionPayload `json:"execution_payload" yaml:"execution_payload"`
}

func (b *BeaconBlockBody) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(
		&b.RandaoReveal, &b.Eth1Data,
		&b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
		spec.Wrap(&b.SyncAggregate), spec.Wrap(&b.ExecutionPayload),
	)
}

func (b *BeaconBlockBody) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(
		&b.RandaoReveal, &b.Eth1Data,
		&b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
		spec.Wrap(&b.SyncAggregate), spec.Wrap(&b.ExecutionPayload),
	)
}

func (b *BeaconBlockBody) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(
		&b.RandaoReveal, &b.Eth1Data,
		&b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
		spec.Wrap(&b.SyncAggregate), spec.Wrap(&b.ExecutionPayload),
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
		spec.Wrap(&b.SyncAggregate), spec.Wrap(&b.ExecutionPayload),
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
	// TODO: also check sum of byte size, sanity check block size.
	if x := uint64(len(b.ExecutionPayload.Transactions)); x > uint64(spec.MAX_TRANSACTIONS_PER_PAYLOAD) {
		return fmt.Errorf("too many transactions: %d", x)
	}
	return nil
}

func (b *BeaconBlockBody) Shallow(spec *common.Spec) *BeaconBlockBodyShallow {
	return &BeaconBlockBodyShallow{
		RandaoReveal:         b.RandaoReveal,
		Eth1Data:             b.Eth1Data,
		Graffiti:             b.Graffiti,
		ProposerSlashings:    b.ProposerSlashings,
		AttesterSlashings:    b.AttesterSlashings,
		Attestations:         b.Attestations,
		Deposits:             b.Deposits,
		VoluntaryExits:       b.VoluntaryExits,
		SyncAggregate:        b.SyncAggregate,
		ExecutionPayloadRoot: b.ExecutionPayload.HashTreeRoot(spec, tree.GetHashFn()),
	}
}

func BeaconBlockBodyType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("BeaconBlockBody", []FieldDef{
		{"randao_reveal", common.BLSSignatureType},
		{"eth1_data", common.Eth1DataType}, // Eth1 data vote
		{"graffiti", common.Bytes32Type},   // Arbitrary data
		// Operations
		{"proposer_slashings", phase0.BlockProposerSlashingsType(spec)},
		{"attester_slashings", phase0.BlockAttesterSlashingsType(spec)},
		{"attestations", phase0.BlockAttestationsType(spec)},
		{"deposits", phase0.BlockDepositsType(spec)},
		{"voluntary_exits", phase0.BlockVoluntaryExitsType(spec)},
		{"sync_aggregate", altair.SyncAggregateType(spec)},
		// Bellatrix
		{"execution_payload", ExecutionPayloadType(spec)},
	})
}

type BeaconBlockBodyShallow struct {
	RandaoReveal common.BLSSignature `json:"randao_reveal" yaml:"randao_reveal"`
	Eth1Data     common.Eth1Data     `json:"eth1_data" yaml:"eth1_data"`
	Graffiti     common.Root         `json:"graffiti" yaml:"graffiti"`

	ProposerSlashings phase0.ProposerSlashings `json:"proposer_slashings" yaml:"proposer_slashings"`
	AttesterSlashings phase0.AttesterSlashings `json:"attester_slashings" yaml:"attester_slashings"`
	Attestations      phase0.Attestations      `json:"attestations" yaml:"attestations"`
	Deposits          phase0.Deposits          `json:"deposits" yaml:"deposits"`
	VoluntaryExits    phase0.VoluntaryExits    `json:"voluntary_exits" yaml:"voluntary_exits"`

	SyncAggregate altair.SyncAggregate `json:"sync_aggregate" yaml:"sync_aggregate"`

	ExecutionPayloadRoot common.Root `json:"execution_payload_root" yaml:"execution_payload_root"`
}

func (b *BeaconBlockBodyShallow) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(
		&b.RandaoReveal, &b.Eth1Data,
		&b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
		spec.Wrap(&b.SyncAggregate), &b.ExecutionPayloadRoot,
	)
}

func (b *BeaconBlockBodyShallow) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(
		&b.RandaoReveal, &b.Eth1Data,
		&b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
		spec.Wrap(&b.SyncAggregate), &b.ExecutionPayloadRoot,
	)
}

func (b *BeaconBlockBodyShallow) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(
		&b.RandaoReveal, &b.Eth1Data,
		&b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
		spec.Wrap(&b.SyncAggregate), &b.ExecutionPayloadRoot,
	)
}

func (a *BeaconBlockBodyShallow) FixedLength(*common.Spec) uint64 {
	return 0
}

func (b *BeaconBlockBodyShallow) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(
		b.RandaoReveal, &b.Eth1Data,
		b.Graffiti, spec.Wrap(&b.ProposerSlashings),
		spec.Wrap(&b.AttesterSlashings), spec.Wrap(&b.Attestations),
		spec.Wrap(&b.Deposits), spec.Wrap(&b.VoluntaryExits),
		spec.Wrap(&b.SyncAggregate), &b.ExecutionPayloadRoot,
	)
}

func (b *BeaconBlockBodyShallow) WithExecutionPayload(spec *common.Spec, payload ExecutionPayload) (*BeaconBlockBody, error) {
	payloadRoot := payload.HashTreeRoot(spec, tree.GetHashFn())
	if b.ExecutionPayloadRoot != payloadRoot {
		return nil, fmt.Errorf("payload does not match expected root: %s <> %s", b.ExecutionPayloadRoot, payloadRoot)
	}
	return &BeaconBlockBody{
		RandaoReveal:      b.RandaoReveal,
		Eth1Data:          b.Eth1Data,
		Graffiti:          b.Graffiti,
		ProposerSlashings: b.ProposerSlashings,
		AttesterSlashings: b.AttesterSlashings,
		Attestations:      b.Attestations,
		Deposits:          b.Deposits,
		VoluntaryExits:    b.VoluntaryExits,
		SyncAggregate:     b.SyncAggregate,
		ExecutionPayload:  payload,
	}, nil
}
