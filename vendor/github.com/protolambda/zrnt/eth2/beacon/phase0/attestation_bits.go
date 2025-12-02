package phase0

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

// AttestationBits is formatted as a serialized SSZ bitlist, including the delimit bit
type AttestationBits []byte

func (li AttestationBits) View(spec *common.Spec) *AttestationBitsView {
	v, _ := AttestationBitsType(spec).Deserialize(codec.NewDecodingReader(bytes.NewReader(li), uint64(len(li))))
	return &AttestationBitsView{v.(*BitListView)}
}

func (li *AttestationBits) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.BitList((*[]byte)(li), uint64(spec.MAX_VALIDATORS_PER_COMMITTEE))
}

func (a AttestationBits) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.BitList(a[:])
}

func (a AttestationBits) ByteLength(spec *common.Spec) uint64 {
	return uint64(len(a))
}

func (a *AttestationBits) FixedLength(*common.Spec) uint64 {
	return 0
}

func (li AttestationBits) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.BitListHTR(li, uint64(spec.MAX_VALIDATORS_PER_COMMITTEE))
}

func (cb AttestationBits) MarshalText() ([]byte, error) {
	return conv.BytesMarshalText(cb[:])
}

func (cb *AttestationBits) UnmarshalText(text []byte) error {
	return conv.DynamicBytesUnmarshalText((*[]byte)(cb), text)
}

func (cb AttestationBits) String() string {
	return conv.BytesString(cb[:])
}

func (cb AttestationBits) BitLen() uint64 {
	return bitfields.BitlistLen(cb)
}

func (cb AttestationBits) GetBit(i uint64) bool {
	return bitfields.GetBit(cb, i)
}

func (cb AttestationBits) SetBit(i uint64, v bool) {
	bitfields.SetBit(cb, i, v)
}

// Sets the bits to true that are true in other. (in place)
func (cb AttestationBits) Or(other AttestationBits) {
	for i := 0; i < len(cb); i++ {
		cb[i] |= other[i]
	}
}

// In-place filters a list of committees indices to only keep the bitfield participants.
// The result is not sorted. Returns the re-sliced filtered participants list.
//
// WARNING: unsafe to use, panics if committee size does not match.
func (cb AttestationBits) FilterParticipants(committee []common.ValidatorIndex) []common.ValidatorIndex {
	out := committee[:0]
	bitLen := cb.BitLen()
	if bitLen != uint64(len(committee)) {
		panic("committee mismatch, bitfield length does not match")
	}
	for i := uint64(0); i < bitLen; i++ {
		if bitfields.GetBit(cb, i) {
			out = append(out, committee[i])
		}
	}
	return out
}

// In-place filters a list of committees indices to only keep the bitfield NON-participants.
// The result is not sorted. Returns the re-sliced filtered non-participants list.
//
// WARNING: unsafe to use, panics if committee size does not match.
func (cb AttestationBits) FilterNonParticipants(committee []common.ValidatorIndex) []common.ValidatorIndex {
	out := committee[:0]
	bitLen := cb.BitLen()
	if bitLen != uint64(len(committee)) {
		panic("committee mismatch, bitfield length does not match")
	}
	for i := uint64(0); i < bitLen; i++ {
		if !bitfields.GetBit(cb, i) {
			out = append(out, committee[i])
		}
	}
	return out
}

// Returns true if other only has bits set to 1 that this bitfield also has set to 1
func (cb AttestationBits) Covers(other AttestationBits) (bool, error) {
	if a, b := cb.BitLen(), other.BitLen(); a != b {
		return false, fmt.Errorf("bitfield length mismatch: %d <> %d", a, b)
	}
	return bitfields.Covers(cb, other)
}

func (cb AttestationBits) OnesCount() uint64 {
	return bitfields.BitlistOnesCount(cb)
}

func (cb AttestationBits) SingleParticipant(committee []common.ValidatorIndex) (common.ValidatorIndex, error) {
	bitLen := cb.BitLen()
	if bitLen != uint64(len(committee)) {
		return 0, fmt.Errorf("committee mismatch, bitfield length %d does not match committee size %d", bitLen, len(committee))
	}
	var found *common.ValidatorIndex
	for i := uint64(0); i < bitLen; i++ {
		if cb.GetBit(i) {
			if found == nil {
				found = &committee[i]
			} else {
				return 0, fmt.Errorf("found at least two participants: %d and %d", *found, committee[i])
			}
		}
	}
	if found == nil {
		return 0, fmt.Errorf("found no participants")
	}
	return *found, nil
}

func (cb AttestationBits) Copy() AttestationBits {
	// append won't find capacity, and thus put contents into new array, and then returns typed slice of it.
	return append(AttestationBits(nil), cb...)
}

func AttestationBitsType(spec *common.Spec) *BitListTypeDef {
	return BitListType(uint64(spec.MAX_VALIDATORS_PER_COMMITTEE))
}

type AttestationBitsView struct {
	*BitListView
}

func AsAttestationBits(v View, err error) (*AttestationBitsView, error) {
	c, err := AsBitList(v, err)
	return &AttestationBitsView{c}, err
}

func (v *AttestationBitsView) Raw() (AttestationBits, error) {
	bitLength, err := v.Length()
	if err != nil {
		return nil, err
	}
	// rounded up, and then an extra bit for delimiting. ((bitLength + 7 + 1)/ 8)
	byteLength := (bitLength / 8) + 1
	var buf bytes.Buffer
	if err := v.Serialize(codec.NewEncodingWriter(&buf)); err != nil {
		return nil, err
	}
	out := AttestationBits(buf.Bytes())
	if uint64(len(out)) != byteLength {
		return nil, fmt.Errorf("failed to convert attestation tree bits view to raw bits")
	}
	return out, nil
}
