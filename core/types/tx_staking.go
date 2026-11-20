package types

import (
	"bytes"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

// StakingValidatorStatus is one validator's entry in a validator status record.
type StakingValidatorStatus struct {
	ValidatorID   uint64
	InSet         bool
	AcceptedNonce uint64
}

// StakingStatusRecord is a validator status record delivered to the staking contract.
type StakingStatusRecord struct {
	ID         uint64
	Validators []StakingValidatorStatus
}

// StakingParticipation is one validator's entry in a participation report.
// Stake amounts are in wei.
type StakingParticipation struct {
	ValidatorID     uint64
	MilestonesVoted uint64
	EthereumStake   *big.Int
	PolygonStake    *big.Int
	InTransit       *big.Int
	VotingPower     uint64
}

// StakingReport is a participation report delivered to the distributor contract.
type StakingReport struct {
	ID         uint64
	StartBlock uint64
	EndBlock   uint64
	Milestones uint64
	Validators []StakingParticipation
}

// StakingTx is a system transaction in Bor that carries the system calls made
// to the Polygon staking contracts in one block: the validator status records
// and participation reports delivered, in ID order, and whether the stake
// update call ran. It comes after the state-sync transaction, if the block has
// one, and is always the last transaction.
//
// Like StateSyncTx it has no signature or real sender. The public facing
// methods return the zero address for both; the calls themselves come from
// params.BorSystemAddress.
//
// BlockNumber makes the hash unique to the block, which a transaction with
// only a stake update call would otherwise not have.
type StakingTx struct {
	BlockNumber   uint64
	StatusRecords []StakingStatusRecord
	Reports       []StakingReport
	StakeUpdate   bool
}

func (tx *StakingTx) copy() TxData {
	if tx == nil {
		return nil
	}

	return &StakingTx{
		BlockNumber:   tx.BlockNumber,
		StatusRecords: copyStatusRecords(tx.StatusRecords),
		Reports:       copyReports(tx.Reports),
		StakeUpdate:   tx.StakeUpdate,
	}
}

func copyStatusRecords(records []StakingStatusRecord) []StakingStatusRecord {
	if records == nil {
		return nil
	}

	out := make([]StakingStatusRecord, len(records))
	for i, r := range records {
		out[i] = StakingStatusRecord{ID: r.ID, Validators: append([]StakingValidatorStatus(nil), r.Validators...)}
	}

	return out
}

func copyReports(reports []StakingReport) []StakingReport {
	if reports == nil {
		return nil
	}

	out := make([]StakingReport, len(reports))
	for i, r := range reports {
		out[i] = r
		out[i].Validators = copyParticipations(r.Validators)
	}

	return out
}

func copyParticipations(validators []StakingParticipation) []StakingParticipation {
	if validators == nil {
		return nil
	}

	out := make([]StakingParticipation, len(validators))
	for i, v := range validators {
		out[i] = v.copy()
	}

	return out
}

func (p StakingParticipation) copy() StakingParticipation {
	out := p
	out.EthereumStake = copyAmount(p.EthereumStake)
	out.PolygonStake = copyAmount(p.PolygonStake)
	out.InTransit = copyAmount(p.InTransit)

	return out
}

// copyAmount turns nil into zero, which is what a nil amount encodes as.
func copyAmount(amount *big.Int) *big.Int {
	if amount == nil {
		return new(big.Int)
	}

	return new(big.Int).Set(amount)
}

// accessors for innerTx.
func (tx *StakingTx) txType() byte           { return StakingTxType }
func (tx *StakingTx) chainID() *big.Int      { return nil }
func (tx *StakingTx) accessList() AccessList { return nil }
func (tx *StakingTx) data() []byte           { return []byte{} }
func (tx *StakingTx) gas() uint64            { return 0 }
func (tx *StakingTx) gasPrice() *big.Int     { return big.NewInt(0) }
func (tx *StakingTx) gasTipCap() *big.Int    { return big.NewInt(0) }
func (tx *StakingTx) gasFeeCap() *big.Int    { return big.NewInt(0) }
func (tx *StakingTx) value() *big.Int        { return big.NewInt(0) }
func (tx *StakingTx) nonce() uint64          { return 0 }

// to returns the zero address representing the public facing RPC value. The
// calls go to the staking and distributor contracts.
func (tx *StakingTx) to() *common.Address { return &common.Address{} }

func (tx *StakingTx) effectiveGasPrice(dst *big.Int, baseFee *big.Int) *big.Int {
	return big.NewInt(0)
}

func (tx *StakingTx) rawSignatureValues() (v, r, s *big.Int) {
	return big.NewInt(0), big.NewInt(0), big.NewInt(0)
}

func (tx *StakingTx) setSignatureValues(chainID, v, r, s *big.Int) {
	panic("setSignatureValues called on StakingTx")
}

func (tx *StakingTx) encode(buf *bytes.Buffer) error {
	if tx == nil {
		return errors.New("nil StakingTx")
	}

	return rlp.Encode(buf, tx)
}

func (tx *StakingTx) decode(b []byte) error {
	if tx == nil {
		return errors.New("nil StakingTx")
	}

	return rlp.DecodeBytes(b, tx)
}

func (tx *StakingTx) sigHash(chainID *big.Int) common.Hash {
	panic("sigHash called on StakingTx")
}
