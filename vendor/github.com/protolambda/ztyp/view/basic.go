package view

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/conv"
	. "github.com/protolambda/ztyp/tree"
	"strconv"
)

// A uint type, identified by its size in bytes.
type UintMeta uint64

func (td UintMeta) Default(_ BackingHook) View {
	switch td {
	case Uint8Type:
		return Uint8View(0)
	case Uint16Type:
		return Uint16View(0)
	case Uint32Type:
		return Uint32View(0)
	case Uint64Type:
		return Uint64View(0)
	case Uint128Type:
		return nil
	case Uint256Type:
		return Uint256View{}
	default:
		// unsupported uint type
		return nil
	}
}

func (td UintMeta) DefaultNode() Node {
	return &ZeroHashes[0]
}

func (td UintMeta) New() BasicView {
	switch td {
	case Uint8Type:
		return Uint8View(0)
	case Uint16Type:
		return Uint16View(0)
	case Uint32Type:
		return Uint32View(0)
	case Uint64Type:
		return Uint64View(0)
	case Uint128Type:
		return nil
	case Uint256Type:
		return Uint256View{}
	default:
		return nil
	}
}

var UnsupportedUintType = errors.New("unsupported uint type")

func (td UintMeta) ViewFromBacking(node Node, _ BackingHook) (View, error) {
	v, ok := node.(*Root)
	if !ok {
		return nil, fmt.Errorf("node %v must be a root to read a uint%d from it", node, td*8)
	}
	switch td {
	case Uint8Type:
		return Uint8View(v[0]), nil
	case Uint16Type:
		return Uint16View(binary.LittleEndian.Uint16(v[:2])), nil
	case Uint32Type:
		return Uint32View(binary.LittleEndian.Uint32(v[:4])), nil
	case Uint64Type:
		return Uint64View(binary.LittleEndian.Uint64(v[:8])), nil
	case Uint128Type:
		return nil, UnsupportedUintType
	case Uint256Type:
		var out Uint256View
		out.setBytes32(v[:])
		return out, nil
	default:
		return nil, UnsupportedUintType
	}
}

func (td UintMeta) BasicViewFromBacking(v *Root, i uint8) (BasicView, error) {
	if uint64(i) >= (32 / uint64(td)) {
		return nil, fmt.Errorf("cannot get subview %d for uint%d type", i, td)
	}
	switch td {
	case Uint8Type:
		return Uint8View(v[i]), nil
	case Uint16Type:
		return Uint16View(binary.LittleEndian.Uint16(v[2*i : 2*i+2])), nil
	case Uint32Type:
		return Uint32View(binary.LittleEndian.Uint32(v[4*i : 4*i+4])), nil
	case Uint64Type:
		return Uint64View(binary.LittleEndian.Uint64(v[8*i : 8*i+8])), nil
	case Uint128Type:
		return nil, UnsupportedUintType
	case Uint256Type:
		var out Uint256View
		out.setBytes32(v[:])
		return out, nil
	default:
		return nil, UnsupportedUintType
	}
}

func (td UintMeta) PackViews(views []BasicView) ([]Node, error) {
	// TODO can be optimized: switch on type, put contents directly into node bytes, and don't use temporary nodes
	perNode := uint8(32 / td)
	chunkCount := (uint64(len(views)) + uint64(perNode) - 1) / uint64(perNode)
	chunks := make([]Node, chunkCount, chunkCount)
	i := 0
	for chunk := uint64(0); chunk < chunkCount; chunk++ {
		root := &Root{}
		for j := uint8(0); j < perNode && i < len(views); j++ {
			root = views[i].BackingFromBase(root, j)
			i += 1
		}
		chunks[chunk] = root
	}
	return chunks, nil
}

func (td UintMeta) IsFixedByteLength() bool {
	return true
}

func (td UintMeta) TypeByteLength() uint64 {
	return uint64(td)
}

func (td UintMeta) MinByteLength() uint64 {
	return uint64(td)
}

func (td UintMeta) MaxByteLength() uint64 {
	return uint64(td)
}

func (td UintMeta) Deserialize(dr *codec.DecodingReader) (View, error) {
	switch td {
	case Uint8Type:
		v, err := dr.ReadByte()
		return Uint8View(v), err
	case Uint16Type:
		v, err := dr.ReadUint16()
		return Uint16View(v), err
	case Uint32Type:
		v, err := dr.ReadUint32()
		return Uint32View(v), err
	case Uint64Type:
		v, err := dr.ReadUint64()
		return Uint64View(v), err
	case Uint128Type:
		return nil, UnsupportedUintType
	case Uint256Type:
		var out Uint256View
		err := out.Deserialize(dr)
		return out, err
	default:
		return nil, UnsupportedUintType
	}
}

func (td UintMeta) String() string {
	return fmt.Sprintf("uint%d", td*8)
}

const (
	Uint8Type   UintMeta = 1
	Uint16Type  UintMeta = 2
	Uint32Type  UintMeta = 4
	Uint64Type  UintMeta = 8
	Uint128Type UintMeta = 16
	Uint256Type UintMeta = 32
)

var BasicViewNoSetBackingError = errors.New("basic views cannot set new backing")

var BadLengthError = errors.New("scope is wrong")

type Uint8View uint8

func AsUint8(v View, err error) (Uint8View, error) {
	if err != nil {
		return 0, err
	}
	n, ok := v.(Uint8View)
	if !ok {
		return 0, fmt.Errorf("not a uint8 view: %v", v)
	}
	return n, nil
}

func (v Uint8View) SetBacking(b Node) error {
	return BasicViewNoSetBackingError
}

func (v Uint8View) Backing() Node {
	out := &Root{}
	out[0] = uint8(v)
	return out
}

func (v Uint8View) BackingFromBase(base *Root, i uint8) *Root {
	if i >= 32 {
		return nil
	}
	newRoot := *base
	newRoot[i] = uint8(v)
	return &newRoot
}

func (v Uint8View) Copy() (View, error) {
	return v, nil
}

func (v Uint8View) ValueByteLength() (uint64, error) {
	return 1, nil
}

func (v Uint8View) ByteLength() uint64 {
	return 1
}

func (v Uint8View) FixedLength() uint64 {
	return 1
}

func (v Uint8View) Serialize(w *codec.EncodingWriter) error {
	return w.WriteByte(byte(v))
}

func (v Uint8View) Encode() ([]byte, error) {
	return []byte{byte(v)}, nil
}

func (v *Uint8View) Deserialize(r *codec.DecodingReader) error {
	b, err := r.ReadByte()
	if err != nil {
		return err
	}
	*v = Uint8View(b)
	return nil
}

func (v *Uint8View) Decode(x []byte) error {
	if len(x) != 1 {
		return BadLengthError
	}
	*v = Uint8View(x[0])
	return nil
}

func (v Uint8View) HashTreeRoot(h HashFn) Root {
	newRoot := Root{}
	newRoot[0] = uint8(v)
	return newRoot
}

func (v Uint8View) Type() TypeDef {
	return Uint8Type
}

func (v Uint8View) MarshalText() (out []byte, err error) {
	out = strconv.AppendUint(out, uint64(v), 10)
	return
}

func (v *Uint8View) UnmarshalText(b []byte) error {
	n, err := strconv.ParseUint(string(b), 0, 8)
	if err != nil {
		return err
	}
	*v = Uint8View(n)
	return nil
}

func (v Uint8View) MarshalJSON() ([]byte, error) {
	return conv.Uint8Marshal(uint8(v))
}

func (v *Uint8View) UnmarshalJSON(b []byte) error {
	return conv.Uint8Unmarshal((*uint8)(v), b)
}

func (v Uint8View) String() string {
	return strconv.FormatUint(uint64(v), 10)
}

// Alias to Uint8Type
const ByteType = Uint8Type

// Alias to Uint8View
type ByteView = Uint8View

func AsByte(v View, err error) (ByteView, error) {
	return AsUint8(v, err)
}

type Uint16View uint16

func AsUint16(v View, err error) (Uint16View, error) {
	if err != nil {
		return 0, err
	}
	n, ok := v.(Uint16View)
	if !ok {
		return 0, fmt.Errorf("not a uint16 view: %v", v)
	}
	return n, nil
}

func (v Uint16View) SetBacking(b Node) error {
	return BasicViewNoSetBackingError
}

func (v Uint16View) Backing() Node {
	out := &Root{}
	binary.LittleEndian.PutUint16(out[:2], uint16(v))
	return out
}

func (v Uint16View) BackingFromBase(base *Root, i uint8) *Root {
	if i >= 16 {
		return nil
	}
	newRoot := *base
	binary.LittleEndian.PutUint16(newRoot[i<<1:(i<<1)+2], uint16(v))
	return &newRoot
}

func (v Uint16View) Copy() (View, error) {
	return v, nil
}

func (v Uint16View) ValueByteLength() (uint64, error) {
	return 2, nil
}

func (v Uint16View) ByteLength() uint64 {
	return 2
}

func (v Uint16View) FixedLength() uint64 {
	return 2
}

func (v Uint16View) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint16(uint16(v))
}

func (v Uint16View) Encode() ([]byte, error) {
	var out [2]byte
	binary.LittleEndian.PutUint16(out[:], uint16(v))
	return out[:], nil
}

func (v *Uint16View) Deserialize(r *codec.DecodingReader) error {
	d, err := r.ReadUint16()
	if err != nil {
		return err
	}
	*v = Uint16View(d)
	return nil
}

func (v *Uint16View) Decode(x []byte) error {
	if len(x) != 2 {
		return BadLengthError
	}
	*v = Uint16View(binary.LittleEndian.Uint16(x[:]))
	return nil
}

func (v Uint16View) HashTreeRoot(h HashFn) Root {
	newRoot := Root{}
	binary.LittleEndian.PutUint16(newRoot[:], uint16(v))
	return newRoot
}

func (v Uint16View) Type() TypeDef {
	return Uint16Type
}

func (v Uint16View) MarshalText() (out []byte, err error) {
	out = strconv.AppendUint(out, uint64(v), 10)
	return
}

func (v *Uint16View) UnmarshalText(b []byte) error {
	n, err := strconv.ParseUint(string(b), 0, 16)
	if err != nil {
		return err
	}
	*v = Uint16View(n)
	return nil
}

func (v Uint16View) MarshalJSON() ([]byte, error) {
	return conv.Uint16Marshal(uint16(v))
}

func (v *Uint16View) UnmarshalJSON(b []byte) error {
	return conv.Uint16Unmarshal((*uint16)(v), b)
}

func (v Uint16View) String() string {
	return strconv.FormatUint(uint64(v), 10)
}

type Uint32View uint32

func AsUint32(v View, err error) (Uint32View, error) {
	if err != nil {
		return 0, err
	}
	n, ok := v.(Uint32View)
	if !ok {
		return 0, fmt.Errorf("not a uint32 view: %v", v)
	}
	return n, nil
}

func (v Uint32View) SetBacking(b Node) error {
	return BasicViewNoSetBackingError
}

func (v Uint32View) Backing() Node {
	out := &Root{}
	binary.LittleEndian.PutUint32(out[:4], uint32(v))
	return out
}

func (v Uint32View) BackingFromBase(base *Root, i uint8) *Root {
	if i >= 8 {
		return nil
	}
	newRoot := *base
	binary.LittleEndian.PutUint32(newRoot[i*4:i*4+4], uint32(v))
	return &newRoot
}

func (v Uint32View) Copy() (View, error) {
	return v, nil
}

func (v Uint32View) ValueByteLength() (uint64, error) {
	return 4, nil
}

func (v Uint32View) ByteLength() uint64 {
	return 4
}

func (v Uint32View) FixedLength() uint64 {
	return 4
}

func (v Uint32View) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint32(uint32(v))
}

func (v Uint32View) Encode() ([]byte, error) {
	var out [4]byte
	binary.LittleEndian.PutUint32(out[:], uint32(v))
	return out[:], nil
}

func (v *Uint32View) Deserialize(r *codec.DecodingReader) error {
	d, err := r.ReadUint32()
	if err != nil {
		return err
	}
	*v = Uint32View(d)
	return nil
}

func (v *Uint32View) Decode(x []byte) error {
	if len(x) != 4 {
		return BadLengthError
	}
	*v = Uint32View(binary.LittleEndian.Uint32(x[:]))
	return nil
}

func (v Uint32View) HashTreeRoot(h HashFn) Root {
	newRoot := Root{}
	binary.LittleEndian.PutUint32(newRoot[:], uint32(v))
	return newRoot
}

func (v Uint32View) Type() TypeDef {
	return Uint32Type
}

func (v Uint32View) MarshalText() (out []byte, err error) {
	out = strconv.AppendUint(out, uint64(v), 10)
	return
}

func (v *Uint32View) UnmarshalText(b []byte) error {
	n, err := strconv.ParseUint(string(b), 0, 32)
	if err != nil {
		return err
	}
	*v = Uint32View(n)
	return nil
}

func (v Uint32View) MarshalJSON() ([]byte, error) {
	return conv.Uint32Marshal(uint32(v))
}

func (v *Uint32View) UnmarshalJSON(b []byte) error {
	return conv.Uint32Unmarshal((*uint32)(v), b)
}

func (v Uint32View) String() string {
	return strconv.FormatUint(uint64(v), 10)
}

type Uint64View uint64

func AsUint64(v View, err error) (Uint64View, error) {
	if err != nil {
		return 0, err
	}
	n, ok := v.(Uint64View)
	if !ok {
		return 0, fmt.Errorf("not a uint64 view: %v", v)
	}
	return n, nil
}

func (v Uint64View) SetBacking(b Node) error {
	return BasicViewNoSetBackingError
}

func (v Uint64View) Backing() Node {
	out := &Root{}
	binary.LittleEndian.PutUint64(out[:8], uint64(v))
	return out
}

func (v Uint64View) BackingFromBase(base *Root, i uint8) *Root {
	if i >= 4 {
		return nil
	}
	newRoot := *base
	binary.LittleEndian.PutUint64(newRoot[i*8:i*8+8], uint64(v))
	return &newRoot
}

func (v Uint64View) Copy() (View, error) {
	return v, nil
}

func (v Uint64View) ValueByteLength() (uint64, error) {
	return 8, nil
}

func (v Uint64View) ByteLength() uint64 {
	return 8
}

func (v Uint64View) FixedLength() uint64 {
	return 8
}

func (v Uint64View) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint64(uint64(v))
}

func (v Uint64View) Encode() ([]byte, error) {
	var out [8]byte
	binary.LittleEndian.PutUint64(out[:], uint64(v))
	return out[:], nil
}

func (v *Uint64View) Deserialize(r *codec.DecodingReader) error {
	d, err := r.ReadUint64()
	if err != nil {
		return err
	}
	*v = Uint64View(d)
	return nil
}

func (v *Uint64View) Decode(x []byte) error {
	if len(x) != 8 {
		return BadLengthError
	}
	*v = Uint64View(binary.LittleEndian.Uint64(x[:]))
	return nil
}

func (v Uint64View) HashTreeRoot(h HashFn) Root {
	newRoot := Root{}
	binary.LittleEndian.PutUint64(newRoot[:], uint64(v))
	return newRoot
}

func (v Uint64View) Type() TypeDef {
	return Uint64Type
}

func (v Uint64View) MarshalText() (out []byte, err error) {
	out = strconv.AppendUint(out, uint64(v), 10)
	return
}

func (v *Uint64View) UnmarshalText(b []byte) error {
	n, err := strconv.ParseUint(string(b), 0, 64)
	if err != nil {
		return err
	}
	*v = Uint64View(n)
	return nil
}

func (v Uint64View) MarshalJSON() ([]byte, error) {
	return conv.Uint64Marshal(uint64(v))
}

func (v *Uint64View) UnmarshalJSON(b []byte) error {
	return conv.Uint64Unmarshal((*uint64)(v), b)
}

func (v Uint64View) String() string {
	return strconv.FormatUint(uint64(v), 10)
}

type BoolMeta uint8

func (td BoolMeta) SubViewFromBacking(v *Root, i uint8) BasicView {
	if i >= 32 {
		return nil
	}
	if v[i] > 1 {
		return nil
	}
	return BoolView(v[i] == 1)
}

func (td BoolMeta) BoolViewFromBitfieldBacking(v *Root, i uint8) (BoolView, error) {
	if i > 32 {
		return false, fmt.Errorf("out of range bit lookup in node: index: %d root: %x", i, v)
	}
	return (v[i>>3]>>(i&7))&1 == 1, nil
}

func (td BoolMeta) Default(_ BackingHook) View {
	return BoolView(false)
}

func (td BoolMeta) New() BoolView {
	return false
}

func (td BoolMeta) DefaultNode() Node {
	return &ZeroHashes[0]
}

func (td BoolMeta) ViewFromBacking(node Node, _ BackingHook) (View, error) {
	v, ok := node.(*Root)
	if !ok {
		return nil, fmt.Errorf("node %v must be a root to read a bool from it", node)
	}
	return BoolView(v[0] != 0), nil
}

func (td BoolMeta) IsFixedByteLength() bool {
	return true
}

func (td BoolMeta) TypeByteLength() uint64 {
	return 1
}

func (td BoolMeta) MinByteLength() uint64 {
	return 1
}

func (td BoolMeta) MaxByteLength() uint64 {
	return 1
}

func (td BoolMeta) Deserialize(dr *codec.DecodingReader) (View, error) {
	b, err := dr.ReadByte()
	if err != nil {
		return nil, err
	}
	if b > 1 {
		return nil, fmt.Errorf("invalid bool value: 0x%x", b)
	}
	return BoolView(b == 1), nil
}

func (td BoolMeta) String() string {
	return "bool"
}

const BoolType BoolMeta = 0

type BoolView bool

func AsBool(v View, err error) (BoolView, error) {
	if err != nil {
		return false, err
	}
	b, ok := v.(BoolView)
	if !ok {
		return false, fmt.Errorf("not a bool view: %v", v)
	}
	return b, nil
}

var trueRoot = &Root{1}

func (v BoolView) SetBacking(b Node) error {
	return BasicViewNoSetBackingError
}

func (v BoolView) Backing() Node {
	if v {
		return trueRoot
	} else {
		return &ZeroHashes[0]
	}
}

func (v BoolView) BackingFromBitfieldBase(base *Root, i uint8) *Root {
	newRoot := *base
	if v {
		newRoot[i>>3] |= 1 << (i & 7)
	} else {
		newRoot[i>>3] &^= 1 << (i & 7)
	}
	return &newRoot
}

func (v BoolView) byte() byte {
	if v {
		return 1
	} else {
		return 0
	}
}

func (v BoolView) BackingFromBase(base *Root, i uint8) *Root {
	if i >= 32 {
		return nil
	}
	newRoot := *base
	if v {
		newRoot[i] = 1
	} else {
		newRoot[i] = 0
	}
	return &newRoot
}

func (v BoolView) Copy() (View, error) {
	return v, nil
}

func (v BoolView) ByteLength() uint64 {
	return 1
}

func (v BoolView) ValueByteLength() (uint64, error) {
	return 1, nil
}

func (v BoolView) FixedLength() uint64 {
	return 1
}

func (v BoolView) Serialize(w *codec.EncodingWriter) error {
	return w.WriteByte(v.byte())
}

func (v BoolView) Encode() ([]byte, error) {
	return []byte{v.byte()}, nil
}

func (v *BoolView) Deserialize(r *codec.DecodingReader) error {
	d, err := r.ReadByte()
	if err != nil {
		return err
	}
	if d > 1 {
		return fmt.Errorf("invalid bool value: 0x%x", d)
	}
	*v = BoolView(d > 0)
	return nil
}

func (v *BoolView) Decode(x []byte) error {
	if len(x) != 1 {
		return BadLengthError
	}
	if x[0] > 1 {
		return fmt.Errorf("invalid bool value: 0x%x", x[0])
	}
	*v = BoolView(x[0] > 0)
	return nil
}

func (v BoolView) HashTreeRoot(h HashFn) Root {
	newRoot := Root{}
	newRoot[0] = v.byte()
	return newRoot
}

func (v BoolView) Type() TypeDef {
	return BoolType
}

func (v BoolView) String() string {
	if v {
		return "true"
	} else {
		return "false"
	}
}
