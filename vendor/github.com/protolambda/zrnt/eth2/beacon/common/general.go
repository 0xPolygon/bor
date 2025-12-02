package common

import (
	"fmt"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type Root = tree.Root

type Bytes32 = Root

const Bytes32Type = RootType

type CommitteeIndex Uint64View

func (i *CommitteeIndex) Deserialize(dr *codec.DecodingReader) error {
	return (*Uint64View)(i).Deserialize(dr)
}

func (i CommitteeIndex) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint64(uint64(i))
}

func (CommitteeIndex) ByteLength() uint64 {
	return 8
}

func (CommitteeIndex) FixedLength() uint64 {
	return 8
}

func (i CommitteeIndex) HashTreeRoot(hFn tree.HashFn) Root {
	return Uint64View(i).HashTreeRoot(hFn)
}

func (e CommitteeIndex) MarshalJSON() ([]byte, error) {
	return Uint64View(e).MarshalJSON()
}

func (e *CommitteeIndex) UnmarshalJSON(b []byte) error {
	return ((*Uint64View)(e)).UnmarshalJSON(b)
}

func (e CommitteeIndex) String() string {
	return Uint64View(e).String()
}

const CommitteeIndexType = Uint64Type

func AsCommitteeIndex(v View, err error) (CommitteeIndex, error) {
	i, err := AsUint64(v, err)
	return CommitteeIndex(i), err
}

type Gwei Uint64View

func (g *Gwei) Deserialize(dr *codec.DecodingReader) error {
	return (*Uint64View)(g).Deserialize(dr)
}

func (i Gwei) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint64(uint64(i))
}

func (Gwei) ByteLength() uint64 {
	return 8
}

func (g Gwei) FixedLength() uint64 {
	return 8
}

func (g Gwei) HashTreeRoot(hFn tree.HashFn) Root {
	return Uint64View(g).HashTreeRoot(hFn)
}

func (e Gwei) MarshalJSON() ([]byte, error) {
	return Uint64View(e).MarshalJSON()
}

func (e *Gwei) UnmarshalJSON(b []byte) error {
	return ((*Uint64View)(e)).UnmarshalJSON(b)
}

func (e Gwei) String() string {
	return Uint64View(e).String()
}

const GweiType = Uint64Type

func AsGwei(v View, err error) (Gwei, error) {
	i, err := AsUint64(v, err)
	return Gwei(i), err
}

type Checkpoint struct {
	Epoch Epoch `json:"epoch" yaml:"epoch"`
	Root  Root  `json:"root" yaml:"root"`
}

func (c *Checkpoint) String() string {
	return c.Root.String() + ":" + c.Epoch.String()
}

func (c *Checkpoint) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&c.Epoch, &c.Root)
}

func (a *Checkpoint) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(a.Epoch, &a.Root)
}

func (a *Checkpoint) ByteLength() uint64 {
	return 8 + 32
}

func (g *Checkpoint) FixedLength() uint64 {
	return 8 + 32
}

func (c *Checkpoint) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(c.Epoch, c.Root)
}

func (c *Checkpoint) View() *CheckpointView {
	r := RootView(c.Root)
	res, _ := CheckpointType.FromFields(Uint64View(c.Epoch), &r)
	return &CheckpointView{res}
}

var CheckpointType = ContainerType("Checkpoint", []FieldDef{
	{"epoch", EpochType},
	{"root", RootType},
})

func (c *Phase0Preset) CheckPoint() *ContainerTypeDef {
	return ContainerType("Checkpoint", []FieldDef{
		{"epoch", EpochType},
		{"root", RootType},
	})
}

type CheckpointView struct {
	*ContainerView
}

func (v *CheckpointView) Set(ch *Checkpoint) error {
	return v.SetBacking(ch.View().Backing())
}

func (v *CheckpointView) Epoch() (Epoch, error) {
	return AsEpoch(v.Get(0))
}

func (v *CheckpointView) Root() (Root, error) {
	return AsRoot(v.Get(0))
}

func (v *CheckpointView) Raw() (Checkpoint, error) {
	epoch, err := AsEpoch(v.Get(0))
	if err != nil {
		return Checkpoint{}, err
	}
	root, err := AsRoot(v.Get(1))
	if err != nil {
		return Checkpoint{}, err
	}
	return Checkpoint{Epoch: epoch, Root: root}, nil
}

func AsCheckPoint(v View, err error) (*CheckpointView, error) {
	c, err := AsContainer(v, err)
	return &CheckpointView{c}, err
}

type NodeRef struct {
	Slot Slot
	// Block root, may be equal to parent root if empty
	Root Root
}

func (n NodeRef) String() string {
	return n.Root.String() + ":" + n.Slot.String()
}

type ExtendedNodeRef struct {
	NodeRef
	ParentRoot Root
}

func (n ExtendedNodeRef) String() string {
	return fmt.Sprintf("%s:%d (parent %s)", n.Root, n.Slot, n.ParentRoot)
}
