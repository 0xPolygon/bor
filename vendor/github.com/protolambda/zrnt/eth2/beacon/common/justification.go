package common

import (
	"fmt"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/conv"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type JustificationBits [1]byte

func (b *JustificationBits) Deserialize(dr *codec.DecodingReader) error {
	v, err := dr.ReadByte()
	if err != nil {
		return err
	}
	b[0] = v
	return nil
}

func (a JustificationBits) Serialize(w *codec.EncodingWriter) error {
	return w.WriteByte(a[0])
}

func (jb JustificationBits) FixedLength() uint64 {
	return 1
}

func (jb JustificationBits) ByteLength() uint64 {
	return 1
}

func (jb JustificationBits) HashTreeRoot(hFn tree.HashFn) Root {
	return Root{0: jb[0]}
}

func (jb *JustificationBits) BitLen() uint64 {
	return JUSTIFICATION_BITS_LENGTH
}

// Prepare bitfield for next epoch by shifting previous bits (truncating to bitfield length)
func (jb *JustificationBits) NextEpoch() {
	// shift and mask
	jb[0] = (jb[0] << 1) & 0x0f
}

func (jb *JustificationBits) IsJustified(epochsAgo ...Epoch) bool {
	for _, t := range epochsAgo {
		if jb[0]&(1<<t) == 0 {
			return false
		}
	}
	return true
}

func (jb JustificationBits) MarshalText() ([]byte, error) {
	return conv.BytesMarshalText(jb[:])
}

func (jb *JustificationBits) UnmarshalText(text []byte) error {
	return conv.FixedBytesUnmarshalText(jb[:], text)
}

func (jb *JustificationBits) String() string {
	return conv.BytesString(jb[:])
}

func (jb *JustificationBits) View() *JustificationBitsView {
	v, _ := JustificationBitsType.ViewFromBacking(&Root{0: jb[0]}, nil)
	return &JustificationBitsView{v.(*BitVectorView)}
}

var JustificationBitsType = BitVectorType(JUSTIFICATION_BITS_LENGTH)

type JustificationBitsView struct {
	*BitVectorView
}

func (v *JustificationBitsView) Raw() (JustificationBits, error) {
	b, err := v.SubtreeView.GetNode(0)
	if err != nil {
		return JustificationBits{}, err
	}
	r, ok := b.(*Root)
	if !ok {
		return JustificationBits{}, fmt.Errorf("justification bitvector bottom node is not a root, cannot get bits")
	}
	return JustificationBits{r[0]}, nil
}

func (v *JustificationBitsView) Set(bits JustificationBits) error {
	root := Root{0: bits[0]}
	return v.SetBacking(&root)
}

func AsJustificationBits(v View, err error) (*JustificationBitsView, error) {
	c, err := AsBitVector(v, err)
	return &JustificationBitsView{c}, err
}
