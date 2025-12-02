package common

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/conv"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

func PayloadTransactionsType(spec *Spec) ListTypeDef {
	return ListType(TransactionType(spec), uint64(spec.MAX_TRANSACTIONS_PER_PAYLOAD))
}

type PayloadTransactions []Transaction

func (txs *PayloadTransactions) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*txs)
		*txs = append(*txs, Transaction{})
		return spec.Wrap(&((*txs)[i]))
	}, 0, uint64(spec.MAX_TRANSACTIONS_PER_PAYLOAD))
}

func (txs PayloadTransactions) Serialize(spec *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return spec.Wrap(&txs[i])
	}, 0, uint64(len(txs)))
}

func (txs PayloadTransactions) ByteLength(spec *Spec) (out uint64) {
	for _, v := range txs {
		out += v.ByteLength(spec) + codec.OFFSET_SIZE
	}
	return
}

func (txs *PayloadTransactions) FixedLength(*Spec) uint64 {
	return 0
}

func (txs PayloadTransactions) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(txs))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return spec.Wrap(&txs[i])
		}
		return nil
	}, length, uint64(spec.MAX_TRANSACTIONS_PER_PAYLOAD))
}

func (txs PayloadTransactions) MarshalJSON() ([]byte, error) {
	if txs == nil {
		return json.Marshal([]Transaction{}) // encode as empty list, not null
	}
	return json.Marshal([]Transaction(txs))
}

func TransactionType(spec *Spec) *BasicListTypeDef {
	return BasicListType(Uint8Type, uint64(spec.MAX_BYTES_PER_TRANSACTION))
}

type Transaction []byte

func (otx *Transaction) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.ByteList((*[]byte)(otx), uint64(spec.MAX_BYTES_PER_TRANSACTION))
}

func (otx Transaction) Serialize(spec *Spec, w *codec.EncodingWriter) error {
	return w.Write(otx)
}

func (otx Transaction) ByteLength(spec *Spec) (out uint64) {
	return uint64(len(otx))
}

func (otx *Transaction) FixedLength(*Spec) uint64 {
	return 0
}

func (otx Transaction) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	return hFn.ByteListHTR(otx, uint64(spec.MAX_BYTES_PER_TRANSACTION))
}

func (otx Transaction) MarshalText() ([]byte, error) {
	return conv.BytesMarshalText(otx[:])
}

func (otx Transaction) String() string {
	return "0x" + hex.EncodeToString(otx[:])
}

func (otx *Transaction) UnmarshalText(text []byte) error {
	if otx == nil {
		return errors.New("cannot decode into nil opaque transaction")
	}
	return conv.DynamicBytesUnmarshalText((*[]byte)(otx), text[:])
}

func (otx Transaction) View(spec *Spec) (*TransactionView, error) {
	dec := codec.NewDecodingReader(bytes.NewReader(otx), uint64(len(otx)))
	return AsTransaction(TransactionType(spec).Deserialize(dec))
}

type TransactionView struct {
	*UnionView
}

func AsTransaction(v View, err error) (*TransactionView, error) {
	c, err := AsUnion(v, err)
	return &TransactionView{c}, err
}
