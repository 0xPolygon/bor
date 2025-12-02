package common

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type WithdrawalPrefix [1]byte

func (p WithdrawalPrefix) MarshalText() ([]byte, error) {
	return []byte("0x" + hex.EncodeToString(p[:])), nil
}

func (p WithdrawalPrefix) String() string {
	return "0x" + hex.EncodeToString(p[:])
}

func (p *WithdrawalPrefix) UnmarshalText(text []byte) error {
	if p == nil {
		return errors.New("cannot decode into nil WithdrawalPrefix")
	}
	if len(text) >= 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = text[2:]
	}
	if len(text) != 2 {
		return fmt.Errorf("unexpected length string '%s'", string(text))
	}
	_, err := hex.Decode(p[:], text)
	return err
}

const WithdrawalIndexType = Uint64Type

type WithdrawalIndex Uint64View

func AsWithdrawalIndex(v View, err error) (WithdrawalIndex, error) {
	i, err := AsUint64(v, err)
	return WithdrawalIndex(i), err
}

func (a *WithdrawalIndex) Deserialize(dr *codec.DecodingReader) error {
	return (*Uint64View)(a).Deserialize(dr)
}

func (i WithdrawalIndex) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint64(uint64(i))
}

func (WithdrawalIndex) ByteLength() uint64 {
	return 8
}

func (WithdrawalIndex) FixedLength() uint64 {
	return 8
}

func (t WithdrawalIndex) HashTreeRoot(hFn tree.HashFn) Root {
	return Uint64View(t).HashTreeRoot(hFn)
}

func (e WithdrawalIndex) MarshalJSON() ([]byte, error) {
	return Uint64View(e).MarshalJSON()
}

func (e *WithdrawalIndex) UnmarshalJSON(b []byte) error {
	return ((*Uint64View)(e)).UnmarshalJSON(b)
}

func (e WithdrawalIndex) String() string {
	return Uint64View(e).String()
}

var WithdrawalType = ContainerType("Withdrawal", []FieldDef{
	{"index", WithdrawalIndexType},
	{"validator_index", ValidatorIndexType},
	{"address", Eth1AddressType},
	{"amount", GweiType},
})

type WithdrawalView struct {
	*ContainerView
}

func (v *WithdrawalView) Raw() (*Withdrawal, error) {
	values, err := v.FieldValues()
	if err != nil {
		return nil, err
	}
	if len(values) != 4 {
		return nil, fmt.Errorf("unexpected number of withdrawal fields: %d", len(values))
	}
	index, err := AsWithdrawalIndex(values[0], err)
	validatorIndex, err := AsValidatorIndex(values[1], err)
	address, err := AsEth1Address(values[2], err)
	amount, err := AsGwei(values[3], err)
	if err != nil {
		return nil, err
	}
	return &Withdrawal{
		Index:          index,
		ValidatorIndex: validatorIndex,
		Address:        address,
		Amount:         amount,
	}, nil
}

func (v *WithdrawalView) Index() (WithdrawalIndex, error) {
	return AsWithdrawalIndex(v.Get(0))
}

func (v *WithdrawalView) ValidatorIndex() (ValidatorIndex, error) {
	return AsValidatorIndex(v.Get(1))
}

func (v *WithdrawalView) Address() (Eth1Address, error) {
	return AsEth1Address(v.Get(2))
}

func (v *WithdrawalView) Amount() (Gwei, error) {
	return AsGwei(v.Get(3))
}

func AsWithdrawal(v View, err error) (*WithdrawalView, error) {
	c, err := AsContainer(v, err)
	return &WithdrawalView{c}, err
}

type Withdrawal struct {
	Index          WithdrawalIndex `json:"index" yaml:"index"`
	ValidatorIndex ValidatorIndex  `json:"validator_index" yaml:"validator_index"`
	Address        Eth1Address     `json:"address" yaml:"address"`
	Amount         Gwei            `json:"amount" yaml:"amount"`
}

func (s *Withdrawal) View() *WithdrawalView {
	i, vi, ad, am := s.Index, s.ValidatorIndex, s.Address, s.Amount
	v, err := AsWithdrawal(WithdrawalType.FromFields(Uint64View(i), Uint64View(vi), ad.View(), Uint64View(am)))
	if err != nil {
		panic(err)
	}
	return v
}

func (s *Withdrawal) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&s.Index, &s.ValidatorIndex, &s.Address, &s.Amount)
}

func (s *Withdrawal) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&s.Index, &s.ValidatorIndex, &s.Address, &s.Amount)
}

func (s *Withdrawal) ByteLength() uint64 {
	return Uint64Type.TypeByteLength()*3 + Eth1AddressType.TypeByteLength()
}

func (s *Withdrawal) FixedLength() uint64 {
	return Uint64Type.TypeByteLength()*3 + Eth1AddressType.TypeByteLength()
}

func (s *Withdrawal) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&s.Index, &s.ValidatorIndex, &s.Address, &s.Amount)
}

func WithdrawalsType(spec *Spec) ListTypeDef {
	return ListType(WithdrawalType, uint64(spec.MAX_WITHDRAWALS_PER_PAYLOAD))
}

type Withdrawals []Withdrawal

func (ws *Withdrawals) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*ws)
		*ws = append(*ws, Withdrawal{})
		return &((*ws)[i])
	}, WithdrawalType.TypeByteLength(), uint64(spec.MAX_WITHDRAWALS_PER_PAYLOAD))
}

func (ws Withdrawals) Serialize(spec *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &ws[i]
	}, WithdrawalType.TypeByteLength(), uint64(len(ws)))
}

func (ws Withdrawals) ByteLength(spec *Spec) (out uint64) {
	return WithdrawalType.TypeByteLength() * uint64(len(ws))
}

func (ws *Withdrawals) FixedLength(*Spec) uint64 {
	return 0
}

func (ws Withdrawals) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(ws))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &ws[i]
		}
		return nil
	}, length, uint64(spec.MAX_WITHDRAWALS_PER_PAYLOAD))
}

func (ws Withdrawals) MarshalJSON() ([]byte, error) {
	if ws == nil {
		return json.Marshal([]Withdrawal{}) // encode as empty list, not null
	}
	return json.Marshal([]Withdrawal(ws))
}

var BLSToExecutionChangeType = ContainerType("BLSToExecutionChange", []FieldDef{
	{"validator_index", ValidatorIndexType},
	{"from_bls_pubkey", BLSPubkeyType},
	{"to_execution_address", Eth1AddressType},
})

type BLSToExecutionChangeView struct {
	*ContainerView
}

func (v *BLSToExecutionChangeView) Raw() (*BLSToExecutionChange, error) {
	values, err := v.FieldValues()
	if err != nil {
		return nil, err
	}
	if len(values) != 3 {
		return nil, fmt.Errorf("unexpected number of bls to execution change fields: %d", len(values))
	}
	validatorIndex, err := AsValidatorIndex(values[0], err)
	fromBLSPubKey, err := AsBLSPubkey(values[1], err)
	toExecAddress, err := AsEth1Address(values[2], err)
	if err != nil {
		return nil, err
	}
	return &BLSToExecutionChange{
		ValidatorIndex:     validatorIndex,
		FromBLSPubKey:      fromBLSPubKey,
		ToExecutionAddress: toExecAddress,
	}, nil
}

func (v *BLSToExecutionChangeView) ValidatorIndex() (ValidatorIndex, error) {
	return AsValidatorIndex(v.Get(0))
}

func (v *BLSToExecutionChangeView) FromBLSPubKey() (BLSPubkey, error) {
	return AsBLSPubkey(v.Get(1))
}

func (v *BLSToExecutionChangeView) ToExecutionAddress() (Eth1Address, error) {
	return AsEth1Address(v.Get(2))
}

func AsBLSToExecutionChange(v View, err error) (*BLSToExecutionChangeView, error) {
	c, err := AsContainer(v, err)
	return &BLSToExecutionChangeView{c}, err
}

type BLSToExecutionChange struct {
	ValidatorIndex     ValidatorIndex `json:"validator_index" yaml:"validator_index"`
	FromBLSPubKey      BLSPubkey      `json:"from_bls_pubkey" yaml:"from_bls_pubkey"`
	ToExecutionAddress Eth1Address    `json:"to_execution_address" yaml:"to_execution_address"`
}

func (s *BLSToExecutionChange) View() *BLSToExecutionChangeView {
	vi, pk, ea := s.ValidatorIndex, s.FromBLSPubKey, s.ToExecutionAddress
	v, err := AsBLSToExecutionChange(BLSToExecutionChangeType.FromFields(Uint64View(vi), ViewPubkey(&pk), ea.View()))
	if err != nil {
		panic(err)
	}
	return v
}

func (s *BLSToExecutionChange) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&s.ValidatorIndex, &s.FromBLSPubKey, &s.ToExecutionAddress)
}

func (s *BLSToExecutionChange) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&s.ValidatorIndex, &s.FromBLSPubKey, &s.ToExecutionAddress)
}

func (s *BLSToExecutionChange) ByteLength() uint64 {
	return 8 + 48 + 20
}

func (s *BLSToExecutionChange) FixedLength() uint64 {
	return 8 + 48 + 20
}

func (s *BLSToExecutionChange) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&s.ValidatorIndex, &s.FromBLSPubKey, &s.ToExecutionAddress)
}

var SignedBLSToExecutionChangeType = ContainerType("SignedBLSToExecutionChange", []FieldDef{
	{"message", BLSToExecutionChangeType},
	{"signature", BLSSignatureType},
})

type SignedBLSToExecutionChangeView struct {
	*ContainerView
}

func (v *SignedBLSToExecutionChangeView) Raw() (*SignedBLSToExecutionChange, error) {
	values, err := v.FieldValues()
	if err != nil {
		return nil, err
	}
	if len(values) != 2 {
		return nil, fmt.Errorf("unexpected number of signed bls to execution change fields: %d", len(values))
	}
	blsToExecView, err := AsBLSToExecutionChange(values[0], err)
	signature, err := AsBLSSignature(values[1], err)
	if err != nil {
		return nil, err
	}
	blsToExec, err := blsToExecView.Raw()
	if err != nil {
		return nil, err
	}
	return &SignedBLSToExecutionChange{
		BLSToExecutionChange: *blsToExec,
		Signature:            signature,
	}, nil
}

func (v *SignedBLSToExecutionChangeView) BLSToExecutionChange() (*BLSToExecutionChangeView, error) {
	return AsBLSToExecutionChange(v.Get(0))
}

func (v *SignedBLSToExecutionChangeView) Signature() (BLSSignature, error) {
	return AsBLSSignature(v.Get(1))
}

func AsSignedBLSToExecutionChange(v View, err error) (*SignedBLSToExecutionChangeView, error) {
	c, err := AsContainer(v, err)
	return &SignedBLSToExecutionChangeView{c}, err
}

type SignedBLSToExecutionChange struct {
	BLSToExecutionChange BLSToExecutionChange `json:"message" yaml:"message"`
	Signature            BLSSignature         `json:"signature" yaml:"signature"`
}

func (s *SignedBLSToExecutionChange) View() *SignedBLSToExecutionChangeView {
	v, err := AsSignedBLSToExecutionChange(SignedBLSToExecutionChangeType.FromFields(s.BLSToExecutionChange.View(), ViewSignature(&s.Signature)))
	if err != nil {
		panic(err)
	}
	return v
}

func (s *SignedBLSToExecutionChange) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&s.BLSToExecutionChange, &s.Signature)
}

func (s *SignedBLSToExecutionChange) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&s.BLSToExecutionChange, &s.Signature)
}

func (s *SignedBLSToExecutionChange) ByteLength() uint64 {
	return codec.ContainerLength(&s.BLSToExecutionChange, &s.Signature)
}

func (s *SignedBLSToExecutionChange) FixedLength() uint64 {
	return codec.ContainerLength(&s.BLSToExecutionChange, &s.Signature)
}

func (s *SignedBLSToExecutionChange) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&s.BLSToExecutionChange, &s.Signature)
}

func BlockSignedBLSToExecutionChangesType(spec *Spec) ListTypeDef {
	return ListType(SignedBLSToExecutionChangeType, uint64(spec.MAX_BLS_TO_EXECUTION_CHANGES))
}

type SignedBLSToExecutionChanges []SignedBLSToExecutionChange

func (li *SignedBLSToExecutionChanges) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*li)
		*li = append(*li, SignedBLSToExecutionChange{})
		return &((*li)[i])
	}, SignedBLSToExecutionChangeType.TypeByteLength(), uint64(spec.MAX_BLS_TO_EXECUTION_CHANGES))
}

func (li SignedBLSToExecutionChanges) Serialize(_ *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &li[i]
	}, SignedBLSToExecutionChangeType.TypeByteLength(), uint64(len(li)))
}

func (li SignedBLSToExecutionChanges) ByteLength(_ *Spec) (out uint64) {
	return SignedBLSToExecutionChangeType.TypeByteLength() * uint64(len(li))
}

func (*SignedBLSToExecutionChanges) FixedLength(*Spec) uint64 {
	return 0
}

func (li SignedBLSToExecutionChanges) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.MAX_BLS_TO_EXECUTION_CHANGES))
}

func (li SignedBLSToExecutionChanges) MarshalJSON() ([]byte, error) {
	if li == nil {
		return json.Marshal([]SignedBLSToExecutionChange{}) // encode as empty list, not null
	}
	return json.Marshal([]SignedBLSToExecutionChange(li))
}
