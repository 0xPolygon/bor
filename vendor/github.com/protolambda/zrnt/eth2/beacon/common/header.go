package common

import (
	"context"
	"errors"
	"fmt"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type BeaconBlockHeader struct {
	Slot          Slot           `json:"slot" yaml:"slot"`
	ProposerIndex ValidatorIndex `json:"proposer_index" yaml:"proposer_index"`
	ParentRoot    Root           `json:"parent_root" yaml:"parent_root"`
	StateRoot     Root           `json:"state_root" yaml:"state_root"`
	BodyRoot      Root           `json:"body_root" yaml:"body_root"`
}

func (h *BeaconBlockHeader) View() *BeaconBlockHeaderView {
	pr := RootView(h.ParentRoot)
	sr := RootView(h.StateRoot)
	br := RootView(h.BodyRoot)
	c, _ := BeaconBlockHeaderType.FromFields(
		Uint64View(h.Slot),
		Uint64View(h.ProposerIndex),
		&pr,
		&sr,
		&br,
	)
	return &BeaconBlockHeaderView{c}
}

func (s *BeaconBlockHeader) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&s.Slot, &s.ProposerIndex, &s.ParentRoot, &s.StateRoot, &s.BodyRoot)
}

func (s *BeaconBlockHeader) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&s.Slot, &s.ProposerIndex, &s.ParentRoot, &s.StateRoot, &s.BodyRoot)
}

func (s *BeaconBlockHeader) ByteLength() uint64 {
	return BeaconBlockHeaderType.TypeByteLength()
}

func (b *BeaconBlockHeader) FixedLength() uint64 {
	return BeaconBlockHeaderType.TypeByteLength()
}

func (b *BeaconBlockHeader) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(b.Slot, b.ProposerIndex, b.ParentRoot, b.StateRoot, b.BodyRoot)
}

type SignedBeaconBlockHeader struct {
	Message   BeaconBlockHeader `json:"message" yaml:"message"`
	Signature BLSSignature      `json:"signature" yaml:"signature"`
}

func (s *SignedBeaconBlockHeader) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&s.Message, &s.Signature)
}

func (s *SignedBeaconBlockHeader) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&s.Message, &s.Signature)
}

func (s *SignedBeaconBlockHeader) ByteLength() uint64 {
	return SignedBeaconBlockHeaderType.TypeByteLength()
}

func (b *SignedBeaconBlockHeader) FixedLength() uint64 {
	return SignedBeaconBlockHeaderType.TypeByteLength()
}

func (s *SignedBeaconBlockHeader) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&s.Message, s.Signature)
}

var SignedBeaconBlockHeaderType = ContainerType("SignedBeaconBlockHeader", []FieldDef{
	{"message", BeaconBlockHeaderType},
	{"signature", BLSSignatureType},
})

var BeaconBlockHeaderType = ContainerType("BeaconBlockHeader", []FieldDef{
	{"slot", SlotType},
	{"proposer_index", ValidatorIndexType},
	{"parent_root", RootType},
	{"state_root", RootType},
	{"body_root", RootType},
})

type BeaconBlockHeaderView struct {
	*ContainerView
}

func AsBeaconBlockHeader(v View, err error) (*BeaconBlockHeaderView, error) {
	c, err := AsContainer(v, err)
	return &BeaconBlockHeaderView{c}, err
}

func (v *BeaconBlockHeaderView) Slot() (Slot, error) {
	return AsSlot(v.Get(0))
}

func (v *BeaconBlockHeaderView) ProposerIndex() (ValidatorIndex, error) {
	return AsValidatorIndex(v.Get(1))
}

func (v *BeaconBlockHeaderView) ParentRoot() (Root, error) {
	return AsRoot(v.Get(2))
}

func (v *BeaconBlockHeaderView) StateRoot() (Root, error) {
	return AsRoot(v.Get(3))
}

func (v *BeaconBlockHeaderView) SetStateRoot(root Root) error {
	rv := RootView(root)
	return v.Set(3, &rv)
}

func (v *BeaconBlockHeaderView) BodyRoot() (Root, error) {
	return AsRoot(v.Get(4))
}

func (v *BeaconBlockHeaderView) Raw() (*BeaconBlockHeader, error) {
	slot, err := v.Slot()
	if err != nil {
		return nil, err
	}
	proposer, err := v.ProposerIndex()
	if err != nil {
		return nil, err
	}
	parentRoot, err := v.ParentRoot()
	if err != nil {
		return nil, err
	}
	stateRoot, err := v.StateRoot()
	if err != nil {
		return nil, err
	}
	bodyRoot, err := v.BodyRoot()
	if err != nil {
		return nil, err
	}
	return &BeaconBlockHeader{
		Slot:          slot,
		ProposerIndex: proposer,
		ParentRoot:    parentRoot,
		StateRoot:     stateRoot,
		BodyRoot:      bodyRoot,
	}, nil
}

func ProcessHeader(ctx context.Context, spec *Spec, state BeaconState, header *BeaconBlockHeader, expectedProposer ValidatorIndex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	currentSlot, err := state.Slot()
	if err != nil {
		return err
	}
	// Verify that the slots match
	if header.Slot != currentSlot {
		return errors.New("slot of block does not match slot of state")
	}
	latestHeader, err := state.LatestBlockHeader()
	if err != nil {
		return err
	}
	if header.Slot <= latestHeader.Slot {
		return errors.New("bad block header")
	}
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	if isValid, err := vals.IsValidIndex(header.ProposerIndex); err != nil {
		return err
	} else if !isValid {
		return fmt.Errorf("beacon block header proposer index is out of range: %d", header.ProposerIndex)
	}
	if header.ProposerIndex != expectedProposer {
		return fmt.Errorf("beacon block header proposer index does not match expected index: got: %d, expected: %d", header.ProposerIndex, expectedProposer)
	}
	// Verify that the parent matches
	latestRoot := latestHeader.HashTreeRoot(tree.GetHashFn())
	if header.ParentRoot != latestRoot {
		return fmt.Errorf("previous block root %s does not match root %s from latest state block header", header.ParentRoot, latestRoot)
	}
	validators, err := state.Validators()
	if err != nil {
		return err
	}
	validator, err := validators.Validator(header.ProposerIndex)
	if err != nil {
		return err
	}
	// Verify proposer is not slashed
	if slashed, err := validator.Slashed(); err != nil {
		return err
	} else if slashed {
		return errors.New("cannot accept block header from slashed proposer")
	}

	// Store as the new latest block
	headerRaw := &BeaconBlockHeader{
		Slot:          header.Slot,
		ProposerIndex: header.ProposerIndex,
		ParentRoot:    header.ParentRoot,
		// state_root is zeroed and overwritten in the next `process_slot` call.
		// with BlockHeaderState.UpdateStateRoot(), once the post state is available.
		StateRoot: Root{},
		BodyRoot:  header.BodyRoot,
	}
	return state.SetLatestBlockHeader(headerRaw)
}
