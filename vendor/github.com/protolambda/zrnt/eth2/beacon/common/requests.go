package common

import (
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type DepositRequest struct {
	Pubkey                BLSPubkey    `json:"pubkey" yaml:"pubkey"`
	WithdrawalCredentials Bytes32      `json:"withdrawal_credentials" yaml:"withdrawal_credentials"`
	Amount                Gwei         `json:"amount" yaml:"amount"`
	Signature             BLSSignature `json:"signature" yaml:"signature"`
	Index                 Uint64View   `json:"index" yaml:"index"`
}

var DepositRequestType = ContainerType("DepositRequest", []FieldDef{
	{"pubkey", BLSPubkeyType},
	{"withdrawal_credentials", Bytes32Type},
	{"amount", GweiType},
	{"signature", BLSSignatureType},
	{"index", Uint64Type},
})

func (a *DepositRequest) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Index)
}

func (a *DepositRequest) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Index)
}

func (a *DepositRequest) ByteLength() uint64 {
	return codec.ContainerLength(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Index)
}

func (a *DepositRequest) FixedLength() uint64 {
	return codec.ContainerLength(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Index)
}

func (a *DepositRequest) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Index)
}

type WithdrawalRequest struct {
	SourceAddress   Eth1Address `json:"source_address" yaml:"source_address"`
	ValidatorPubkey BLSPubkey   `json:"validator_pubkey" yaml:"validator_pubkey"`
	Amount          Gwei        `json:"amount" yaml:"amount"`
}

var WithdrawalRequestType = ContainerType("WithdrawalRequest", []FieldDef{
	{"source_address", Eth1AddressType},
	{"validator_pubkey", BLSPubkeyType},
	{"amount", GweiType},
})

func (a *WithdrawalRequest) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&a.SourceAddress, &a.ValidatorPubkey, &a.Amount)
}

func (a *WithdrawalRequest) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&a.SourceAddress, &a.ValidatorPubkey, &a.Amount)
}

func (a *WithdrawalRequest) ByteLength() uint64 {
	return codec.ContainerLength(&a.SourceAddress, &a.ValidatorPubkey, &a.Amount)
}

func (a *WithdrawalRequest) FixedLength() uint64 {
	return codec.ContainerLength(&a.SourceAddress, &a.ValidatorPubkey, &a.Amount)
}

func (a *WithdrawalRequest) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&a.SourceAddress, &a.ValidatorPubkey, &a.Amount)
}

type ConsolidationRequest struct {
	SourceAddress Eth1Address `json:"source_address" yaml:"source_address"`
	SourcePubkey  BLSPubkey   `json:"source_pubkey" yaml:"source_pubkey"`
	TargetPubkey  BLSPubkey   `json:"target_pubkey" yaml:"target_pubkey"`
}

var ConsolidationRequestType = ContainerType("ConsolidationRequest", []FieldDef{
	{"source_address", Eth1AddressType},
	{"source_pubkey", BLSPubkeyType},
	{"target_pubkey", BLSPubkeyType},
})

func (a *ConsolidationRequest) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&a.SourceAddress, &a.SourcePubkey, &a.TargetPubkey)
}

func (a *ConsolidationRequest) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&a.SourceAddress, &a.SourcePubkey, &a.TargetPubkey)
}

func (a *ConsolidationRequest) ByteLength() uint64 {
	return codec.ContainerLength(&a.SourceAddress, &a.SourcePubkey, &a.TargetPubkey)
}

func (a *ConsolidationRequest) FixedLength() uint64 {
	return codec.ContainerLength(&a.SourceAddress, &a.SourcePubkey, &a.TargetPubkey)
}

func (a *ConsolidationRequest) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&a.SourceAddress, &a.SourcePubkey, &a.TargetPubkey)
}

type DepositRequests []DepositRequest

func DepositRequestsType(spec *Spec) ListTypeDef {
	return ListType(DepositRequestType, uint64(spec.MAX_DEPOSIT_REQUESTS_PER_PAYLOAD))
}

func (li *DepositRequests) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*li)
		*li = append(*li, DepositRequest{})
		return &((*li)[i])
	}, DepositRequestType.TypeByteLength(), uint64(spec.MAX_DEPOSIT_REQUESTS_PER_PAYLOAD))
}

func (li DepositRequests) Serialize(_ *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &li[i]
	}, DepositRequestType.TypeByteLength(), uint64(len(li)))
}

func (li DepositRequests) ByteLength(_ *Spec) (out uint64) {
	return DepositRequestType.TypeByteLength() * uint64(len(li))
}

func (*DepositRequests) FixedLength(*Spec) uint64 {
	return 0
}

func (li DepositRequests) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.MAX_DEPOSIT_REQUESTS_PER_PAYLOAD))
}

type WithdrawalRequests []WithdrawalRequest

func WithdrawalRequestsType(spec *Spec) ListTypeDef {
	return ListType(WithdrawalRequestType, uint64(spec.MAX_WITHDRAWAL_REQUESTS_PER_PAYLOAD))
}

func (li *WithdrawalRequests) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*li)
		*li = append(*li, WithdrawalRequest{})
		return &((*li)[i])
	}, WithdrawalRequestType.TypeByteLength(), uint64(spec.MAX_WITHDRAWAL_REQUESTS_PER_PAYLOAD))
}

func (li WithdrawalRequests) Serialize(_ *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &li[i]
	}, WithdrawalRequestType.TypeByteLength(), uint64(len(li)))
}

func (li WithdrawalRequests) ByteLength(_ *Spec) (out uint64) {
	return WithdrawalRequestType.TypeByteLength() * uint64(len(li))
}

func (*WithdrawalRequests) FixedLength(*Spec) uint64 {
	return 0
}

func (li WithdrawalRequests) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.MAX_WITHDRAWAL_REQUESTS_PER_PAYLOAD))
}

type ConsolidationRequests []ConsolidationRequest

func ConsolidationRequestsType(spec *Spec) ListTypeDef {
	return ListType(ConsolidationRequestType, uint64(spec.MAX_CONSOLIDATION_REQUESTS_PER_PAYLOAD))
}

func (li *ConsolidationRequests) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*li)
		*li = append(*li, ConsolidationRequest{})
		return &((*li)[i])
	}, ConsolidationRequestType.TypeByteLength(), uint64(spec.MAX_CONSOLIDATION_REQUESTS_PER_PAYLOAD))
}

func (li ConsolidationRequests) Serialize(_ *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &li[i]
	}, ConsolidationRequestType.TypeByteLength(), uint64(len(li)))
}

func (li ConsolidationRequests) ByteLength(_ *Spec) (out uint64) {
	return ConsolidationRequestType.TypeByteLength() * uint64(len(li))
}

func (*ConsolidationRequests) FixedLength(*Spec) uint64 {
	return 0
}

func (li ConsolidationRequests) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.MAX_CONSOLIDATION_REQUESTS_PER_PAYLOAD))
}
