package view

import (
	"encoding/binary"
	"fmt"
	"github.com/holiman/uint256"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/conv"
	. "github.com/protolambda/ztyp/tree"
	"math/big"
	"strconv"
)

type Uint256View uint256.Int

func AsUint256(v View, err error) (Uint256View, error) {
	if err != nil {
		return Uint256View{}, err
	}
	n, ok := v.(Uint256View)
	if !ok {
		return Uint256View{}, fmt.Errorf("not a uint256 view: %v", v)
	}
	return n, nil
}

func (v Uint256View) SetBacking(b Node) error {
	return BasicViewNoSetBackingError
}

// Bytes32 returns little endian encoding
func (v Uint256View) Bytes32() (out [32]byte) {
	// uint64 3 is most significant internally in uint256.Int
	binary.LittleEndian.PutUint64(out[0:8], v[0])
	binary.LittleEndian.PutUint64(out[8:16], v[1])
	binary.LittleEndian.PutUint64(out[16:24], v[2])
	binary.LittleEndian.PutUint64(out[24:32], v[3])
	return
}

// SetBytes32 sets view from little endian encoding
func (v *Uint256View) SetBytes32(data [32]byte) {
	v.setBytes32(data[:])
}

func (v *Uint256View) setBytes32(data []byte) {
	v[0] = binary.LittleEndian.Uint64(data[0:8])
	v[1] = binary.LittleEndian.Uint64(data[8:16])
	v[2] = binary.LittleEndian.Uint64(data[16:24])
	v[3] = binary.LittleEndian.Uint64(data[24:32])
}

// Bytes returns little endian encoding (always 32 bytes)
func (v Uint256View) Bytes() []byte {
	out := v.Bytes32()
	return out[:]
}

func (v Uint256View) Backing() Node {
	out := Root(v.Bytes32())
	return &out
}

func (v Uint256View) BackingFromBase(base *Root, i uint8) *Root {
	if i != 0 { // must always be aligned, we overwrite full 32 bytes here, nothing to pack along in same root.
		return nil
	}
	newRoot := Root(v.Bytes32())
	return &newRoot
}

func (v Uint256View) Copy() (View, error) {
	return v, nil
}

func (v Uint256View) ValueByteLength() (uint64, error) {
	return 32, nil
}

func (v Uint256View) ByteLength() uint64 {
	return 32
}

func (v Uint256View) FixedLength() uint64 {
	return 32
}

func (v Uint256View) Serialize(w *codec.EncodingWriter) error {
	return w.Write(v.Bytes())
}

func (v Uint256View) Encode() ([]byte, error) {
	return v.Bytes(), nil
}

func (v *Uint256View) Deserialize(r *codec.DecodingReader) error {
	var data [32]byte
	_, err := r.Read(data[:])
	if err != nil {
		return err
	}
	v.SetBytes32(data)
	return nil
}

func (v *Uint256View) Decode(x []byte) error {
	if len(x) != 32 {
		return BadLengthError
	}
	v.setBytes32(x)
	return nil
}

func (v Uint256View) HashTreeRoot(h HashFn) Root {
	return v.Bytes32()
}

func (v Uint256View) Type() TypeDef {
	return Uint256Type
}

func (v Uint256View) MarshalText() (out []byte, err error) {
	return []byte(fmt.Sprintf("%d", (*uint256.Int)(&v))), nil
}

func (v *Uint256View) SetFromBig(x *big.Int) (overflow bool) {
	if x.Sign() < 0 {
		return true
	}
	return (*uint256.Int)(v).SetFromBig(x)
}

func (v *Uint256View) UnmarshalText(b []byte) error {
	x := new(big.Int)
	err := x.UnmarshalText(b)
	if err != nil {
		return fmt.Errorf("failed to unmarshal Uint256View: %w", err)
	}
	if v.SetFromBig(x) {
		return strconv.ErrRange
	}
	return nil
}

func (v Uint256View) MarshalJSON() ([]byte, error) {
	return conv.Uint256Marshal((*uint256.Int)(&v))
}

func (v *Uint256View) UnmarshalJSON(b []byte) error {
	return conv.Uint256Unmarshal((*uint256.Int)(v), b)
}

func (v Uint256View) String() string {
	return fmt.Sprintf("%d", (*uint256.Int)(&v))
}

func MustUint256(v string) Uint256View {
	var out Uint256View
	err := out.UnmarshalText([]byte(v))
	if err != nil {
		panic(err)
	}
	return out
}
