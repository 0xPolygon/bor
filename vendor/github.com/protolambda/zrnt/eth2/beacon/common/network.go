package common

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type Eth2Data struct {
	ForkDigest      ForkDigest `json:"fork_digest" yaml:"fork_digest"`
	NextForkVersion Version    `json:"next_fork_version" yaml:"next_fork_version"`
	NextForkEpoch   Epoch      `json:"next_fork_epoch" yaml:"next_fork_epoch"`
}

func (d *Eth2Data) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&d.ForkDigest, &d.NextForkVersion, &d.NextForkEpoch)
}

func (d *Eth2Data) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&d.ForkDigest, &d.NextForkVersion, &d.NextForkEpoch)
}

func (d Eth2Data) ByteLength() uint64 {
	return 4 + 4 + 8
}

func (*Eth2Data) FixedLength() uint64 {
	return 4 + 4 + 8
}

func (d *Eth2Data) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&d.ForkDigest, &d.NextForkVersion, &d.NextForkEpoch)
}

const ATTESTATION_SUBNET_COUNT = 64

const attnetByteLen = (ATTESTATION_SUBNET_COUNT + 7) / 8

type AttnetBits [attnetByteLen]byte

func (ab *AttnetBits) BitLen() uint64 {
	return ATTESTATION_SUBNET_COUNT
}
func (p *AttnetBits) Deserialize(dr *codec.DecodingReader) error {
	if p == nil {
		return errors.New("nil attnet bits")
	}
	_, err := dr.Read(p[:])
	return err
}

func (p AttnetBits) Serialize(w *codec.EncodingWriter) error {
	return w.Write(p[:])
}

func (p AttnetBits) ByteLength() uint64 {
	return attnetByteLen
}

func (AttnetBits) FixedLength() uint64 {
	return attnetByteLen
}

func (p AttnetBits) HashTreeRoot(_ tree.HashFn) (out Root) {
	copy(out[:], p[:])
	return
}

func (p AttnetBits) MarshalText() ([]byte, error) {
	return []byte("0x" + hex.EncodeToString(p[:])), nil
}

func (p AttnetBits) String() string {
	return "0x" + hex.EncodeToString(p[:])
}

func (p *AttnetBits) UnmarshalText(text []byte) error {
	if p == nil {
		return errors.New("cannot decode into nil AttnetBits")
	}
	if len(text) >= 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = text[2:]
	}
	if len(text) != attnetByteLen*2 {
		return fmt.Errorf("unexpected length string '%s'", string(text))
	}
	_, err := hex.Decode(p[:], text)
	return err
}

const syncnetByteLen = (SYNC_COMMITTEE_SUBNET_COUNT + 7) / 8

type SyncnetBits [syncnetByteLen]byte

func (ab *SyncnetBits) BitLen() uint64 {
	return SYNC_COMMITTEE_SUBNET_COUNT
}

func (p *SyncnetBits) Deserialize(dr *codec.DecodingReader) error {
	if p == nil {
		return errors.New("nil syncnet bits")
	}
	_, err := dr.Read(p[:])
	return err
}

func (p SyncnetBits) Serialize(w *codec.EncodingWriter) error {
	return w.Write(p[:])
}

func (p SyncnetBits) ByteLength() uint64 {
	return syncnetByteLen
}

func (SyncnetBits) FixedLength() uint64 {
	return syncnetByteLen
}

func (p SyncnetBits) HashTreeRoot(_ tree.HashFn) (out Root) {
	copy(out[:], p[:])
	return
}

func (p SyncnetBits) MarshalText() ([]byte, error) {
	return []byte("0x" + hex.EncodeToString(p[:])), nil
}

func (p SyncnetBits) String() string {
	return "0x" + hex.EncodeToString(p[:])
}

func (p *SyncnetBits) UnmarshalText(text []byte) error {
	if p == nil {
		return errors.New("cannot decode into nil SyncnetBits")
	}
	if len(text) >= 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = text[2:]
	}
	if len(text) != syncnetByteLen*2 {
		return fmt.Errorf("unexpected length string '%s'", string(text))
	}
	_, err := hex.Decode(p[:], text)
	return err
}

type SeqNr Uint64View

func (i *SeqNr) Deserialize(dr *codec.DecodingReader) error {
	return (*Uint64View)(i).Deserialize(dr)
}

func (i SeqNr) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint64(uint64(i))
}

func (SeqNr) ByteLength() uint64 {
	return 8
}

func (SeqNr) FixedLength() uint64 {
	return 8
}

func (i SeqNr) HashTreeRoot(hFn tree.HashFn) Root {
	return Uint64View(i).HashTreeRoot(hFn)
}

func (i SeqNr) MarshalJSON() ([]byte, error) {
	return Uint64View(i).MarshalJSON()
}

func (i *SeqNr) UnmarshalJSON(b []byte) error {
	return ((*Uint64View)(i)).UnmarshalJSON(b)
}

func (i SeqNr) String() string {
	return Uint64View(i).String()
}

type Ping SeqNr

func (i *Ping) Deserialize(dr *codec.DecodingReader) error {
	return (*Uint64View)(i).Deserialize(dr)
}

func (i Ping) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint64(uint64(i))
}

func (Ping) ByteLength() uint64 {
	return 8
}

func (Ping) FixedLength() uint64 {
	return 8
}

func (i Ping) HashTreeRoot(hFn tree.HashFn) Root {
	return Uint64View(i).HashTreeRoot(hFn)
}

func (i Ping) MarshalJSON() ([]byte, error) {
	return Uint64View(i).MarshalJSON()
}

func (i *Ping) UnmarshalJSON(b []byte) error {
	return ((*Uint64View)(i)).UnmarshalJSON(b)
}

func (i Ping) String() string {
	return Uint64View(i).String()
}

type Pong SeqNr

func (i *Pong) Deserialize(dr *codec.DecodingReader) error {
	return (*Uint64View)(i).Deserialize(dr)
}

func (i Pong) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint64(uint64(i))
}

func (Pong) ByteLength() uint64 {
	return 8
}

func (Pong) FixedLength() uint64 {
	return 8
}

func (i Pong) HashTreeRoot(hFn tree.HashFn) Root {
	return Uint64View(i).HashTreeRoot(hFn)
}

func (i Pong) MarshalJSON() ([]byte, error) {
	return Uint64View(i).MarshalJSON()
}

func (i *Pong) UnmarshalJSON(b []byte) error {
	return ((*Uint64View)(i)).UnmarshalJSON(b)
}

func (i Pong) String() string {
	return Uint64View(i).String()
}

type MetaData struct {
	SeqNumber SeqNr       `json:"seq_number" yaml:"seq_number"`
	Attnets   AttnetBits  `json:"attnets" yaml:"attnets"`
	Syncnets  SyncnetBits `json:"syncnets" yaml:"syncnets"`
}

func (m *MetaData) Data() map[string]interface{} {
	return map[string]interface{}{
		"seq_number": m.SeqNumber,
		"attnets":    hex.EncodeToString(m.Attnets[:]),
		"syncnets":   hex.EncodeToString(m.Syncnets[:]),
	}
}

func (d *MetaData) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&d.SeqNumber, &d.Attnets, &d.Syncnets)
}

func (d *MetaData) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&d.SeqNumber, &d.Attnets, &d.Syncnets)
}

const MetadataByteLen = 8 + attnetByteLen + syncnetByteLen

func (d MetaData) ByteLength() uint64 {
	return MetadataByteLen
}

func (*MetaData) FixedLength() uint64 {
	return MetadataByteLen
}

func (d *MetaData) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&d.SeqNumber, &d.Attnets, &d.Syncnets)
}

func (m *MetaData) String() string {
	return fmt.Sprintf("MetaData(seq: %d, attnet bits: %08b, syncnet bits: %08b)", m.SeqNumber, m.Attnets, m.Syncnets)
}

type Status struct {
	ForkDigest     ForkDigest `json:"fork_digest" yaml:"fork_digest"`
	FinalizedRoot  Root       `json:"finalized_root" yaml:"finalized_root"`
	FinalizedEpoch Epoch      `json:"finalized_epoch" yaml:"finalized_epoch"`
	HeadRoot       Root       `json:"head_root" yaml:"head_root"`
	HeadSlot       Slot       `json:"head_slot" yaml:"head_slot"`
}

func (s *Status) Data() map[string]interface{} {
	return map[string]interface{}{
		"fork_digest":     hex.EncodeToString(s.ForkDigest[:]),
		"finalized_root":  hex.EncodeToString(s.FinalizedRoot[:]),
		"finalized_epoch": s.FinalizedEpoch,
		"head_root":       hex.EncodeToString(s.HeadRoot[:]),
		"head_slot":       s.HeadSlot,
	}
}

func (d *Status) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&d.ForkDigest, &d.FinalizedRoot, &d.FinalizedEpoch, &d.HeadRoot, &d.HeadSlot)
}

func (d *Status) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&d.ForkDigest, &d.FinalizedRoot, &d.FinalizedEpoch, &d.HeadRoot, &d.HeadSlot)
}

const StatusByteLen = 4 + 32 + 8 + 32 + 8

func (d Status) ByteLength() uint64 {
	return StatusByteLen
}

func (*Status) FixedLength() uint64 {
	return StatusByteLen
}

func (d *Status) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&d.ForkDigest, &d.FinalizedRoot, &d.FinalizedEpoch, &d.HeadRoot, &d.HeadSlot)
}

func (s *Status) String() string {
	return fmt.Sprintf("Status(fork_digest: %s, finalized_root: %s, finalized_epoch: %d, head_root: %s, head_slot: %d)",
		s.ForkDigest.String(), s.FinalizedRoot.String(), s.FinalizedEpoch, s.HeadRoot.String(), s.HeadSlot)
}

type Goodbye Uint64View

func (i *Goodbye) Deserialize(dr *codec.DecodingReader) error {
	return (*Uint64View)(i).Deserialize(dr)
}

func (i Goodbye) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint64(uint64(i))
}

func (Goodbye) ByteLength() uint64 {
	return 8
}

func (Goodbye) FixedLength() uint64 {
	return 8
}

func (i Goodbye) HashTreeRoot(hFn tree.HashFn) Root {
	return Uint64View(i).HashTreeRoot(hFn)
}

func (i Goodbye) MarshalJSON() ([]byte, error) {
	return Uint64View(i).MarshalJSON()
}

func (i *Goodbye) UnmarshalJSON(b []byte) error {
	return ((*Uint64View)(i)).UnmarshalJSON(b)
}

func (i Goodbye) String() string {
	return Uint64View(i).String()
}
