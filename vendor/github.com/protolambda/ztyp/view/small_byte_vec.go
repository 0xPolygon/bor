package view

import (
	"errors"
	"fmt"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/conv"
	. "github.com/protolambda/ztyp/tree"
)

// To represent views of < 32 bytes efficiently as just a slice of those bytes.
// Values above 32 are invalid.
// For 32, using Root ([32]byte as underlying type) is better.
type SmallByteVecMeta uint8

func (td SmallByteVecMeta) Default(_ BackingHook) View {
	return make(SmallByteVecView, td, td)
}

func (td SmallByteVecMeta) DefaultNode() Node {
	return &ZeroHashes[0]
}

func (td SmallByteVecMeta) New() SmallByteVecView {
	return make(SmallByteVecView, td, td)
}

func (td SmallByteVecMeta) ViewFromBacking(node Node, _ BackingHook) (View, error) {
	r, ok := node.(*Root)
	if !ok {
		return nil, fmt.Errorf("backing must be a root")
	}
	if td > 32 {
		return nil, fmt.Errorf("SmallByteVecMeta can only be used for values 0...32")
	}
	v := make(SmallByteVecView, td, td)
	copy(v, r[:])
	return v, nil
}

func (td SmallByteVecMeta) IsFixedByteLength() bool {
	return true
}

func (td SmallByteVecMeta) TypeByteLength() uint64 {
	return uint64(td)
}

func (td SmallByteVecMeta) MinByteLength() uint64 {
	return uint64(td)
}

func (td SmallByteVecMeta) MaxByteLength() uint64 {
	return uint64(td)
}

func (td SmallByteVecMeta) Deserialize(dr *codec.DecodingReader) (View, error) {
	v := make(SmallByteVecView, td, td)
	_, err := dr.Read(v)
	return v, err
}

func (td SmallByteVecMeta) String() string {
	return fmt.Sprintf("Vector[byte, %d]", td)
}

type SmallByteVecView []byte

func AsSmallByteVec(v View, err error) (SmallByteVecView, error) {
	if err != nil {
		return nil, err
	}
	data, ok := v.(SmallByteVecView)
	if !ok {
		return nil, fmt.Errorf("not a small byte vec view: %v", v)
	}
	return data, nil
}

func (v SmallByteVecView) SetBacking(b Node) error {
	return errors.New("cannot set backing of SmallByteVecView")
}

func (v SmallByteVecView) Backing() Node {
	out := &Root{}
	copy(out[:], v)
	return out
}

func (v SmallByteVecView) Copy() (View, error) {
	return v, nil
}

func (v SmallByteVecView) ValueByteLength() (uint64, error) {
	return uint64(len(v)), nil
}

func (v SmallByteVecView) Serialize(w *codec.EncodingWriter) error {
	return w.Write(v)
}

func (v SmallByteVecView) HashTreeRoot(h HashFn) Root {
	newRoot := Root{}
	copy(newRoot[:], v)
	return newRoot
}

func (v SmallByteVecView) Type() TypeDef {
	return SmallByteVecMeta(len(v))
}

func (v SmallByteVecView) MarshalText() ([]byte, error) {
	return conv.BytesMarshalText(v)
}

func (v SmallByteVecView) UnmarshalText(text []byte) error {
	return conv.FixedBytesUnmarshalText(v, text)
}

func (v SmallByteVecView) String() string {
	return conv.BytesString(v)
}

const Bytes4Type SmallByteVecMeta = 4
const Bytes8Type SmallByteVecMeta = 8
const Bytes16Type SmallByteVecMeta = 16

// Go could use generics...

func AsBytes4(v View, err error) ([4]byte, error) {
	const byteLen = 4
	data, err := AsSmallByteVec(v, err)
	if err != nil {
		return [byteLen]byte{}, err
	}
	if len(data) != byteLen {
		return [byteLen]byte{}, fmt.Errorf("expected %d byte long small byte vec, got %d byte long", byteLen, len(data))
	}
	var out [byteLen]byte
	copy(out[:], data)
	return out, nil
}

func AsBytes8(v View, err error) ([8]byte, error) {
	const byteLen = 8
	data, err := AsSmallByteVec(v, err)
	if err != nil {
		return [byteLen]byte{}, err
	}
	if len(data) != byteLen {
		return [byteLen]byte{}, fmt.Errorf("expected %d byte long small byte vec, got %d byte long", byteLen, len(data))
	}
	var out [byteLen]byte
	copy(out[:], data)
	return out, nil
}

func AsBytes16(v View, err error) ([16]byte, error) {
	const byteLen = 16
	data, err := AsSmallByteVec(v, err)
	if err != nil {
		return [byteLen]byte{}, err
	}
	if len(data) != byteLen {
		return [byteLen]byte{}, fmt.Errorf("expected %d byte long small byte vec, got %d byte long", byteLen, len(data))
	}
	var out [byteLen]byte
	copy(out[:], data)
	return out, nil
}
