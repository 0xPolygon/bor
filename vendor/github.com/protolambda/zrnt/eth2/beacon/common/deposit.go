package common

import (
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"

	. "github.com/protolambda/ztyp/view"
)

var DepositDataType = ContainerType("DepositData", []FieldDef{
	{"pubkey", BLSPubkeyType},
	{"withdrawal_credentials", Bytes32Type},
	{"amount", GweiType},
	{"signature", BLSSignatureType},
})

type DepositData struct {
	Pubkey                BLSPubkey `json:"pubkey" yaml:"pubkey"`
	WithdrawalCredentials Root      `json:"withdrawal_credentials" yaml:"withdrawal_credentials"`
	Amount                Gwei      `json:"amount" yaml:"amount"`
	// Signing over DepositMessage
	Signature BLSSignature `json:"signature" yaml:"signature"`
}

func (d *DepositData) ToMessage() *DepositMessage {
	return &DepositMessage{
		Pubkey:                d.Pubkey,
		WithdrawalCredentials: d.WithdrawalCredentials,
		Amount:                d.Amount,
	}
}

func (d *DepositData) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&d.Pubkey, &d.WithdrawalCredentials, &d.Amount, &d.Signature)
}

func (d *DepositData) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&d.Pubkey, &d.WithdrawalCredentials, &d.Amount, &d.Signature)
}

func (a *DepositData) ByteLength() uint64 {
	return DepositDataType.TypeByteLength()
}

func (a *DepositData) FixedLength() uint64 {
	return DepositDataType.TypeByteLength()
}

// hash-tree-root including the signature
func (d *DepositData) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(d.Pubkey, d.WithdrawalCredentials, d.Amount, d.Signature)
}

// hash-tree-root excluding the signature
func (d *DepositData) MessageRoot() Root {
	return d.ToMessage().HashTreeRoot(tree.GetHashFn())
}

var DepositMessageType = ContainerType("DepositMessage", []FieldDef{
	{"pubkey", BLSPubkeyType},
	{"withdrawal_credentials", Bytes32Type},
	{"amount", GweiType},
})

type DepositMessage struct {
	Pubkey                BLSPubkey `json:"pubkey" yaml:"pubkey"`
	WithdrawalCredentials Root      `json:"withdrawal_credentials" yaml:"withdrawal_credentials"`
	Amount                Gwei      `json:"amount" yaml:"amount"`
}

func (d *DepositMessage) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&d.Pubkey, &d.WithdrawalCredentials, &d.Amount)
}

func (d *DepositMessage) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&d.Pubkey, &d.WithdrawalCredentials, &d.Amount)
}

func (a *DepositMessage) ByteLength() uint64 {
	return 48 + 32 + 8
}

func (a *DepositMessage) FixedLength() uint64 {
	return 48 + 32 + 8
}

func (b *DepositMessage) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(b.Pubkey, b.WithdrawalCredentials, b.Amount)
}

var DepositProofType = VectorType(Bytes32Type, DEPOSIT_CONTRACT_TREE_DEPTH+1)

// DepositProof contains the proof for the merkle-path to deposit root, including list mix-in.
type DepositProof [DEPOSIT_CONTRACT_TREE_DEPTH + 1]Root

func (d *DepositProof) Deserialize(dr *codec.DecodingReader) error {
	return dr.Vector(func(i uint64) codec.Deserializable {
		return &d[i]
	}, RootType.TypeByteLength(), DepositProofType.Length())
}

func (d *DepositProof) Serialize(w *codec.EncodingWriter) error {
	return w.Vector(func(i uint64) codec.Serializable {
		return &d[i]
	}, RootType.TypeByteLength(), DepositProofType.Length())
}

func (a *DepositProof) ByteLength() uint64 {
	return DepositProofType.TypeByteLength()
}

func (a *DepositProof) FixedLength() uint64 {
	return DepositProofType.TypeByteLength()
}

func (b *DepositProof) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.ChunksHTR(func(i uint64) tree.Root {
		return b[i]
	}, uint64(len(b)), uint64(len(b)))
}

type Deposit struct {
	Proof DepositProof `json:"proof" yaml:"proof"`
	Data  DepositData  `json:"data" yaml:"data"`
}

func (d *Deposit) Deserialize(dr *codec.DecodingReader) error {
	return dr.Container(&d.Proof, &d.Data)
}

func (d *Deposit) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&d.Proof, &d.Data)
}

func (a *Deposit) ByteLength() uint64 {
	return Eth1DataType.TypeByteLength()
}

func (a *Deposit) FixedLength() uint64 {
	return DepositType.TypeByteLength()
}

func (b *Deposit) HashTreeRoot(hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(&b.Proof, &b.Data)
}

var DepositType = ContainerType("Deposit", []FieldDef{
	{"proof", DepositProofType}, // Merkle path to deposit data list root
	{"data", DepositDataType},
})
