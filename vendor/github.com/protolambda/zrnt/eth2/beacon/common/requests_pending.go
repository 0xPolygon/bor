package common

import (
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type PendingDeposit struct {
	Pubkey                BLSPubkey    `json:"pubkey" yaml:"pubkey"`
	WithdrawalCredentials Bytes32      `json:"withdrawal_credentials" yaml:"withdrawal_credentials"`
	Amount                Gwei         `json:"amount" yaml:"amount"`
	Signature             BLSSignature `json:"signature" yaml:"signature"`
	Slot                  Slot         `json:"slot" yaml:"slot"`
}

var PendingDepositType = ContainerType("PendingDeposit", []FieldDef{
	{"pubkey", BLSPubkeyType},
	{"withdrawal_credentials", Bytes32Type},
	{"amount", GweiType},
	{"signature", BLSSignatureType},
	{"slot", SlotType},
})

func (a *PendingDeposit) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Slot)
}

func (a *PendingDeposit) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Slot)
}

func (a *PendingDeposit) ByteLength() uint64 {
	return codec.ContainerLength(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Slot)
}

func (a *PendingDeposit) FixedLength() uint64 {
	return codec.ContainerLength(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Slot)
}

func (a *PendingDeposit) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&a.Pubkey, &a.WithdrawalCredentials, &a.Amount, &a.Signature, &a.Slot)
}

type PendingPartialWithdrawal struct {
	ValidatorIndex    ValidatorIndex `json:"validator_index" yaml:"validator_index"`
	Amount            Gwei           `json:"amount" yaml:"amount"`
	WithdrawableEpoch Epoch          `json:"withdrawable_epoch" yaml:"withdrawable_epoch"`
}

var PendingPartialWithdrawalType = ContainerType("PendingPartialWithdrawal", []FieldDef{
	{"validator_index", ValidatorIndexType},
	{"amount", GweiType},
	{"withdrawable_epoch", EpochType},
})

func (a *PendingPartialWithdrawal) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&a.ValidatorIndex, &a.Amount, &a.WithdrawableEpoch)
}

func (a *PendingPartialWithdrawal) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&a.ValidatorIndex, &a.Amount, &a.WithdrawableEpoch)
}

func (a *PendingPartialWithdrawal) ByteLength() uint64 {
	return codec.ContainerLength(&a.ValidatorIndex, &a.Amount, &a.WithdrawableEpoch)
}

func (a *PendingPartialWithdrawal) FixedLength() uint64 {
	return codec.ContainerLength(&a.ValidatorIndex, &a.Amount, &a.WithdrawableEpoch)
}

func (a *PendingPartialWithdrawal) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&a.ValidatorIndex, &a.Amount, &a.WithdrawableEpoch)
}

type PendingConsolidation struct {
	SourceIndex ValidatorIndex `json:"source_index" yaml:"source_index"`
	TargetIndex ValidatorIndex `json:"target_index" yaml:"target_index"`
}

var PendingConsolidationType = ContainerType("PendingConsolidation", []FieldDef{
	{"source_index", ValidatorIndexType},
	{"target_index", ValidatorIndexType},
})

func (a *PendingConsolidation) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&a.SourceIndex, &a.TargetIndex)
}

func (a *PendingConsolidation) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&a.SourceIndex, &a.TargetIndex)
}

func (a *PendingConsolidation) ByteLength() uint64 {
	return codec.ContainerLength(&a.SourceIndex, &a.TargetIndex)
}

func (a *PendingConsolidation) FixedLength() uint64 {
	return codec.ContainerLength(&a.SourceIndex, &a.TargetIndex)
}

func (a *PendingConsolidation) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&a.SourceIndex, &a.TargetIndex)
}

type PendingDeposits []PendingDeposit

func PendingDepositsType(spec *Spec) ListTypeDef {
	return ListType(PendingDepositType, uint64(spec.PENDING_DEPOSITS_LIMIT))
}

func (li *PendingDeposits) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*li)
		*li = append(*li, PendingDeposit{})
		return &((*li)[i])
	}, PendingDepositType.TypeByteLength(), uint64(spec.PENDING_DEPOSITS_LIMIT))
}

func (li PendingDeposits) Serialize(_ *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &li[i]
	}, PendingDepositType.TypeByteLength(), uint64(len(li)))
}

func (li PendingDeposits) ByteLength(_ *Spec) (out uint64) {
	return PendingDepositType.TypeByteLength() * uint64(len(li))
}

func (*PendingDeposits) FixedLength(*Spec) uint64 {
	return 0
}

func (li PendingDeposits) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.PENDING_DEPOSITS_LIMIT))
}

type PendingPartialWithdrawals []PendingPartialWithdrawal

func PendingPartialWithdrawalsType(spec *Spec) ListTypeDef {
	return ListType(PendingPartialWithdrawalType, uint64(spec.PENDING_PARTIAL_WITHDRAWALS_LIMIT))
}

func (li *PendingPartialWithdrawals) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*li)
		*li = append(*li, PendingPartialWithdrawal{})
		return &((*li)[i])
	}, PendingPartialWithdrawalType.TypeByteLength(), uint64(spec.PENDING_PARTIAL_WITHDRAWALS_LIMIT))
}

func (li PendingPartialWithdrawals) Serialize(_ *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &li[i]
	}, PendingPartialWithdrawalType.TypeByteLength(), uint64(len(li)))
}

func (li PendingPartialWithdrawals) ByteLength(_ *Spec) (out uint64) {
	return PendingPartialWithdrawalType.TypeByteLength() * uint64(len(li))
}

func (*PendingPartialWithdrawals) FixedLength(*Spec) uint64 {
	return 0
}

func (li PendingPartialWithdrawals) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.PENDING_PARTIAL_WITHDRAWALS_LIMIT))
}

type PendingConsolidations []PendingConsolidation

func PendingConsolidationsType(spec *Spec) ListTypeDef {
	return ListType(PendingConsolidationType, uint64(spec.PENDING_CONSOLIDATIONS_LIMIT))
}

func (li *PendingConsolidations) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*li)
		*li = append(*li, PendingConsolidation{})
		return &((*li)[i])
	}, PendingConsolidationType.TypeByteLength(), uint64(spec.PENDING_CONSOLIDATIONS_LIMIT))
}

func (li PendingConsolidations) Serialize(_ *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &li[i]
	}, PendingConsolidationType.TypeByteLength(), uint64(len(li)))
}

func (li PendingConsolidations) ByteLength(_ *Spec) (out uint64) {
	return PendingConsolidationType.TypeByteLength() * uint64(len(li))
}

func (*PendingConsolidations) FixedLength(*Spec) uint64 {
	return 0
}

func (li PendingConsolidations) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.PENDING_CONSOLIDATIONS_LIMIT))
}
