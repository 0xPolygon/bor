package common

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

const VersionType = Bytes4Type

// 32 bits, not strictly an integer, hence represented as 4 bytes
// (bytes not necessarily corresponding to versions)
type Version [4]byte

func (v *Version) Deserialize(dr *codec.DecodingReader) error {
	if v == nil {
		return errors.New("nil version")
	}
	_, err := dr.Read(v[:])
	return err
}

func (a Version) Serialize(w *codec.EncodingWriter) error {
	return w.Write(a[:])
}

func (a Version) ByteLength() uint64 {
	return 4
}

func (Version) FixedLength() uint64 {
	return 4
}

func (p Version) HashTreeRoot(_ tree.HashFn) (out Root) {
	copy(out[:], p[:])
	return
}

func (p Version) MarshalText() ([]byte, error) {
	return []byte("0x" + hex.EncodeToString(p[:])), nil
}

func (p Version) String() string {
	return "0x" + hex.EncodeToString(p[:])
}

func (p *Version) UnmarshalText(text []byte) error {
	if p == nil {
		return errors.New("cannot decode into nil Version")
	}
	if len(text) >= 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = text[2:]
	}
	if len(text) != 8 {
		return fmt.Errorf("unexpected length string '%s'", string(text))
	}
	_, err := hex.Decode(p[:], text)
	return err
}

func (v Version) ToUint32() uint32 {
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func (v Version) View() SmallByteVecView {
	return v[:]
}

func AsVersion(v View, err error) (Version, error) {
	return AsBytes4(v, err)
}

const ForkDigestType = Bytes4Type

// A digest of the current fork data
type ForkDigest [4]byte

func (p *ForkDigest) Deserialize(dr *codec.DecodingReader) error {
	if p == nil {
		return errors.New("nil fork-digest")
	}
	_, err := dr.Read(p[:])
	return err
}

func (p ForkDigest) Serialize(w *codec.EncodingWriter) error {
	return w.Write(p[:])
}

func (p ForkDigest) ByteLength() uint64 {
	return 4
}

func (ForkDigest) FixedLength() uint64 {
	return 4
}

func (p ForkDigest) HashTreeRoot(_ tree.HashFn) (out Root) {
	copy(out[:], p[:])
	return
}

func (p ForkDigest) MarshalText() ([]byte, error) {
	return []byte("0x" + hex.EncodeToString(p[:])), nil
}

func (p ForkDigest) String() string {
	return "0x" + hex.EncodeToString(p[:])
}

func (p *ForkDigest) UnmarshalText(text []byte) error {
	if p == nil {
		return errors.New("cannot decode into nil ForkDigest")
	}
	if len(text) >= 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = text[2:]
	}
	if len(text) != 8 {
		return fmt.Errorf("unexpected length string '%s'", string(text))
	}
	_, err := hex.Decode(p[:], text)
	return err
}

var ForkDataType = ContainerType("ForkData", []FieldDef{
	{"current_version", VersionType},
	{"genesis_validators_root", RootType},
})

type ForkData struct {
	CurrentVersion        Version `json:"current_version" yaml:"current_version"`
	GenesisValidatorsRoot Root    `json:"genesis_validators_root" yaml:"genesis_validators_root"`
}

func (v *ForkData) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&v.CurrentVersion, &v.GenesisValidatorsRoot)
}

func (v *ForkData) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&v.CurrentVersion, &v.GenesisValidatorsRoot)
}

func (p ForkData) ByteLength() uint64 {
	return 4 + 32
}

func (*ForkData) FixedLength() uint64 {
	return 4 + 32
}

func (d *ForkData) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(d.CurrentVersion, d.GenesisValidatorsRoot)
}

func ComputeForkDataRoot(currentVersion Version, genesisValidatorsRoot Root) Root {
	data := ForkData{
		CurrentVersion:        currentVersion,
		GenesisValidatorsRoot: genesisValidatorsRoot,
	}
	return data.HashTreeRoot(tree.GetHashFn())
}

func ComputeForkDigest(currentVersion Version, genesisValidatorsRoot Root) ForkDigest {
	var digest ForkDigest
	dataRoot := ComputeForkDataRoot(currentVersion, genesisValidatorsRoot)
	copy(digest[:], dataRoot[:4])
	return digest
}

type Fork struct {
	PreviousVersion Version `json:"previous_version" yaml:"previous_version"`
	CurrentVersion  Version `json:"current_version" yaml:"current_version"`
	Epoch           Epoch   `json:"epoch" yaml:"epoch"`
}

func (b *Fork) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&b.PreviousVersion, &b.CurrentVersion, &b.Epoch)
}

func (a *Fork) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(a.PreviousVersion, a.CurrentVersion, a.Epoch)
}

func (a *Fork) ByteLength() uint64 {
	return ForkType.TypeByteLength()
}

func (a *Fork) FixedLength() uint64 {
	return ForkType.TypeByteLength()
}

func (a *Fork) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(a.PreviousVersion, a.CurrentVersion, a.Epoch)
}

func (f *Fork) View() *ForkView {
	res, _ := ForkType.FromFields(f.PreviousVersion.View(), f.CurrentVersion.View(), Uint64View(f.Epoch))
	return &ForkView{res}
}

// Return the signature domain (fork version concatenated with domain type) of a message.
func (f *Fork) GetDomain(dom BLSDomainType, genesisValRoot Root, messageEpoch Epoch) (BLSDomain, error) {
	var v Version
	if messageEpoch < f.Epoch {
		v = f.PreviousVersion
	} else {
		v = f.CurrentVersion
	}
	// combine fork version with domain type.
	return ComputeDomain(dom, v, genesisValRoot), nil
}

var ForkType = ContainerType("Fork", []FieldDef{
	{"previous_version", VersionType},
	{"current_version", VersionType},
	{"epoch", EpochType}, // Epoch of latest fork
})

type ForkView struct{ *ContainerView }

func (f *ForkView) PreviousVersion() (Version, error) {
	return AsVersion(f.Get(0))
}

func (f *ForkView) CurrentVersion() (Version, error) {
	return AsVersion(f.Get(1))
}

func (f *ForkView) Epoch() (Epoch, error) {
	return AsEpoch(f.Get(2))
}

func (f *ForkView) Raw() (Fork, error) {
	prev, err := f.PreviousVersion()
	if err != nil {
		return Fork{}, err
	}
	curr, err := f.CurrentVersion()
	if err != nil {
		return Fork{}, err
	}
	ep, err := f.Epoch()
	if err != nil {
		return Fork{}, err
	}
	return Fork{
		PreviousVersion: prev,
		CurrentVersion:  curr,
		Epoch:           ep,
	}, nil
}

func AsFork(v View, err error) (*ForkView, error) {
	c, err := AsContainer(v, err)
	return &ForkView{c}, err
}

// Return the signature domain (fork version concatenated with domain type) of a message.
func GetDomain(state BeaconState, dom BLSDomainType, messageEpoch Epoch) (BLSDomain, error) {
	fork, err := state.Fork()
	if err != nil {
		return BLSDomain{}, err
	}
	genesisValRoot, err := state.GenesisValidatorsRoot()
	if err != nil {
		return BLSDomain{}, err
	}
	return fork.GetDomain(dom, genesisValRoot, messageEpoch)
}

// A network message domain
type NetworkMessageDomain [4]byte

func (p *NetworkMessageDomain) Deserialize(dr *codec.DecodingReader) error {
	if p == nil {
		return errors.New("nil network-message-domain")
	}
	_, err := dr.Read(p[:])
	return err
}

func (p NetworkMessageDomain) Serialize(w *codec.EncodingWriter) error {
	return w.Write(p[:])
}

func (p NetworkMessageDomain) ByteLength() uint64 {
	return 4
}

func (NetworkMessageDomain) FixedLength() uint64 {
	return 4
}

func (p NetworkMessageDomain) HashTreeRoot(_ tree.HashFn) (out Root) {
	copy(out[:], p[:])
	return
}

func (p NetworkMessageDomain) MarshalText() ([]byte, error) {
	return []byte("0x" + hex.EncodeToString(p[:])), nil
}

func (p NetworkMessageDomain) String() string {
	return "0x" + hex.EncodeToString(p[:])
}

func (p *NetworkMessageDomain) UnmarshalText(text []byte) error {
	if p == nil {
		return errors.New("cannot decode into nil NetworkMessageDomain")
	}
	if len(text) >= 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = text[2:]
	}
	if len(text) != 8 {
		return fmt.Errorf("unexpected length string '%s'", string(text))
	}
	_, err := hex.Decode(p[:], text)
	return err
}
