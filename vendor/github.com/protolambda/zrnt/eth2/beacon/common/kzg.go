package common

import (
	"encoding/hex"
	"errors"
	"fmt"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	"github.com/protolambda/ztyp/view"

	"github.com/protolambda/zrnt/eth2/util/hashing"
)

const KZGCommitmentSize = 48

type KZGCommitment [KZGCommitmentSize]byte

var KZGCommitmentType = view.BasicVectorType(view.ByteType, KZGCommitmentSize)

func (p *KZGCommitment) Deserialize(dr *codec.DecodingReader) error {
	if p == nil {
		return errors.New("nil pubkey")
	}
	_, err := dr.Read(p[:])
	return err
}

func (p *KZGCommitment) Serialize(w *codec.EncodingWriter) error {
	return w.Write(p[:])
}

func (KZGCommitment) ByteLength() uint64 {
	return KZGCommitmentSize
}

func (KZGCommitment) FixedLength() uint64 {
	return KZGCommitmentSize
}

func (p KZGCommitment) HashTreeRoot(hFn tree.HashFn) tree.Root {
	var a, b tree.Root
	copy(a[:], p[0:32])
	copy(b[:], p[32:48])
	return hFn(a, b)
}

func (p KZGCommitment) MarshalText() ([]byte, error) {
	return []byte("0x" + hex.EncodeToString(p[:])), nil
}

func (p KZGCommitment) String() string {
	return "0x" + hex.EncodeToString(p[:])
}

func (p *KZGCommitment) UnmarshalText(text []byte) error {
	if p == nil {
		return errors.New("cannot decode into nil KZGCommitment")
	}
	if len(text) >= 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = text[2:]
	}
	if len(text) != 2*KZGCommitmentSize {
		return fmt.Errorf("unexpected length string '%s'", string(text))
	}
	_, err := hex.Decode(p[:], text)
	return err
}

func (p *KZGCommitment) ToPubkey() (*blsu.Pubkey, error) {
	var pub blsu.Pubkey
	if err := pub.Deserialize((*[KZGCommitmentSize]byte)(p)); err != nil {
		return nil, err
	}
	return &pub, nil
}

func (p KZGCommitment) ToVersionedHash() (out Hash32) {
	out = hashing.Hash(p[:])
	out[0] = VERSIONED_HASH_VERSION_KZG
	return out
}
