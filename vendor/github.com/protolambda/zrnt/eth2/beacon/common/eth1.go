package common

import (
	"encoding/hex"
	"errors"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/conv"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

const Eth1AddressType = SmallByteVecMeta(20)

func AsEth1Address(v View, err error) (Eth1Address, error) {
	c, err := AsSmallByteVec(v, err)
	if err != nil {
		return Eth1Address{}, err
	}
	var out Eth1Address
	copy(out[:], c)
	return out, nil
}

type Eth1Address [20]byte

func (addr Eth1Address) View() SmallByteVecView {
	// overlay on a copy of the value
	return SmallByteVecView(addr[:])
}

func (addr *Eth1Address) Deserialize(dr *codec.DecodingReader) error {
	if addr == nil {
		return errors.New("cannot deserialize into nil eth1 address")
	}
	_, err := dr.Read(addr[:])
	return err
}

func (addr *Eth1Address) Serialize(w *codec.EncodingWriter) error {
	return w.Write(addr[:])
}

func (*Eth1Address) ByteLength() uint64 {
	return 20
}

func (*Eth1Address) FixedLength() uint64 {
	return 20
}

func (addr *Eth1Address) HashTreeRoot(hFn tree.HashFn) tree.Root {
	var out tree.Root
	copy(out[0:20], addr[:])
	return out
}

func (addr Eth1Address) MarshalText() ([]byte, error) {
	return conv.BytesMarshalText(addr[:])
}

func (addr Eth1Address) String() string {
	return "0x" + hex.EncodeToString(addr[:])
}

func (addr *Eth1Address) UnmarshalText(text []byte) error {
	if addr == nil {
		return errors.New("cannot decode into nil Eth1Address")
	}
	return conv.FixedBytesUnmarshalText(addr[:], text[:])
}

type Eth1Data struct {
	// Hash-tree-root of DepositData tree.
	DepositRoot  Root         `json:"deposit_root" yaml:"deposit_root"`
	DepositCount DepositIndex `json:"deposit_count" yaml:"deposit_count"`
	BlockHash    Root         `json:"block_hash" yaml:"block_hash"`
}

func (b *Eth1Data) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&b.DepositRoot, &b.DepositCount, &b.BlockHash)
}

func (a *Eth1Data) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(a.DepositRoot, a.DepositCount, a.BlockHash)
}

func (a *Eth1Data) ByteLength() uint64 {
	return Eth1DataType.TypeByteLength()
}

func (a *Eth1Data) FixedLength() uint64 {
	return Eth1DataType.TypeByteLength()
}

func (b *Eth1Data) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(b.DepositRoot, b.DepositCount, b.BlockHash)
}

func (dat *Eth1Data) View() *Eth1DataView {
	depRv := RootView(dat.DepositRoot)
	blockRv := RootView(dat.BlockHash)
	c, _ := Eth1DataType.FromFields(&depRv, Uint64View(dat.DepositCount), &blockRv)
	return &Eth1DataView{c}
}

var Eth1DataType = ContainerType("Eth1Data", []FieldDef{
	{"deposit_root", RootType},
	{"deposit_count", Uint64Type},
	{"block_hash", Bytes32Type},
})

type Eth1DataView struct{ *ContainerView }

func AsEth1Data(v View, err error) (*Eth1DataView, error) {
	c, err := AsContainer(v, err)
	return &Eth1DataView{c}, err
}

func (v *Eth1DataView) DepositRoot() (Root, error) {
	return AsRoot(v.Get(0))
}

func (v *Eth1DataView) SetDepositRoot(r Root) error {
	rv := RootView(r)
	return v.Set(0, &rv)
}

func (v *Eth1DataView) DepositCount() (DepositIndex, error) {
	return AsDepositIndex(v.Get(1))
}

func (v *Eth1DataView) BlockHash() (Root, error) {
	return AsRoot(v.Get(2))
}

func (v *Eth1DataView) Raw() (Eth1Data, error) {
	depRoot, err := v.DepositRoot()
	if err != nil {
		return Eth1Data{}, err
	}
	depCount, err := v.DepositCount()
	if err != nil {
		return Eth1Data{}, err
	}
	blockHash, err := v.BlockHash()
	if err != nil {
		return Eth1Data{}, err
	}
	return Eth1Data{
		DepositRoot:  depRoot,
		DepositCount: depCount,
		BlockHash:    blockHash,
	}, nil
}
