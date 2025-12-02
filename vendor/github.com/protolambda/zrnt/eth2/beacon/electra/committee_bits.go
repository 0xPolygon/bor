package electra

import (
	"bytes"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/bitfields"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/conv"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

// CommitteeBits is formatted as a serialized SSZ bitvector,
// with trailing zero bits if length does not align with byte length.
type CommitteeBits []byte

func (li CommitteeBits) View(spec *common.Spec) *CommitteeBitsView {
	v, _ := CommitteeBitsType(spec).Deserialize(codec.NewDecodingReader(bytes.NewReader(li), uint64(len(li))))
	return &CommitteeBitsView{v.(*BitVectorView)}
}

func (li *CommitteeBits) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.BitVector((*[]byte)(li), uint64(spec.MAX_COMMITTEES_PER_SLOT))
}

func (li CommitteeBits) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.BitVector(li[:])
}

func (li CommitteeBits) ByteLength(spec *common.Spec) uint64 {
	return (uint64(spec.MAX_COMMITTEES_PER_SLOT) + 7) / 8
}

func (li *CommitteeBits) FixedLength(spec *common.Spec) uint64 {
	return (uint64(spec.MAX_COMMITTEES_PER_SLOT) + 7) / 8
}

func (li CommitteeBits) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	if li == nil {
		// By default, we should initialize the full bits.
		// We can't do that in the struct case dynamically based on preset, but we can at least output the correct HTR.
		return CommitteeBitsType(spec).New().HashTreeRoot(hFn)
	}
	return hFn.BitVectorHTR(li)
}

func (li CommitteeBits) MarshalText() ([]byte, error) {
	return conv.BytesMarshalText(li[:])
}

func (li *CommitteeBits) UnmarshalText(text []byte) error {
	return conv.DynamicBytesUnmarshalText((*[]byte)(li), text)
}

func (li CommitteeBits) String() string {
	return conv.BytesString(li[:])
}

func (li CommitteeBits) GetBit(i uint64) bool {
	return bitfields.GetBit(li, i)
}

func (li CommitteeBits) SetBit(i uint64, v bool) {
	bitfields.SetBit(li, i, v)
}

type CommitteeBitsView struct {
	*BitVectorView
}

func AsCommitteeBits(v View, err error) (*CommitteeBitsView, error) {
	c, err := AsBitVector(v, err)
	return &CommitteeBitsView{c}, err
}

func (v *CommitteeBitsView) Raw(spec *common.Spec) (CommitteeBits, error) {
	byteLen := int((spec.MAX_COMMITTEES_PER_SLOT + 7) / 8)
	var buf bytes.Buffer
	buf.Grow(byteLen)
	if err := v.Serialize(codec.NewEncodingWriter(&buf)); err != nil {
		return nil, err
	}
	out := CommitteeBits(buf.Bytes())
	if len(out) != byteLen {
		return nil, fmt.Errorf("failed to convert sync committee tree bits view to raw bits")
	}
	return out, nil
}

func CommitteeBitsType(spec *common.Spec) *BitVectorTypeDef {
	return BitVectorType(uint64(spec.MAX_COMMITTEES_PER_SLOT))
}
