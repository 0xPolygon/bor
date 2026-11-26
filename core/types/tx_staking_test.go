package types

import (
	"bytes"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

func testStakingTx() *StakingTx {
	return &StakingTx{
		BlockNumber: 512,
		StatusRecords: []StakingStatusRecord{
			{ID: 1, Validators: []StakingValidatorStatus{{ValidatorID: 1, InSet: true}, {ValidatorID: 2, InSet: false, AcceptedNonce: 7}}},
			{ID: 2, Validators: []StakingValidatorStatus{{ValidatorID: 1_000_000, InSet: true, AcceptedNonce: 2}}},
		},
		Reports: []StakingReport{{
			ID: 1, StartBlock: 256, EndBlock: 511, Milestones: 16,
			Validators: []StakingParticipation{
				{ValidatorID: 1, MilestonesVoted: 16, EthereumStake: big.NewInt(3e18), PolygonStake: big.NewInt(5e17), InTransit: big.NewInt(0), VotingPower: 3},
				{ValidatorID: 2, MilestonesVoted: 12, EthereumStake: big.NewInt(1e18), PolygonStake: big.NewInt(0), InTransit: big.NewInt(25e16), VotingPower: 1},
			},
		}},
		StakeUpdate: true,
	}
}

func TestStakingTx_BinaryRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tx   *StakingTx
	}{
		{"full", testStakingTx()},
		{"stake update only", &StakingTx{BlockNumber: 256, StakeUpdate: true}},
		{"records only", &StakingTx{BlockNumber: 16, StatusRecords: testStakingTx().StatusRecords}},
		{"record with no validators", &StakingTx{BlockNumber: 16, StatusRecords: []StakingStatusRecord{{ID: 9}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx := NewTx(tt.tx)
			require.Equal(t, uint8(StakingTxType), tx.Type())
			require.True(t, tx.IsBorSystemTx())

			enc, err := tx.MarshalBinary()
			require.NoError(t, err)
			require.Equal(t, byte(StakingTxType), enc[0])

			var dec Transaction
			require.NoError(t, dec.UnmarshalBinary(enc))
			require.Equal(t, tx.Hash(), dec.Hash())

			reenc, err := dec.MarshalBinary()
			require.NoError(t, err)
			require.Equal(t, enc, reenc)

			// The hash is over the same bytes the transactions root commits to.
			require.Equal(t, prefixedRlpHash(StakingTxType, tt.tx), tx.Hash())

			// Inside a block body the transaction is an RLP string.
			var buf bytes.Buffer
			require.NoError(t, rlp.Encode(&buf, Transactions{tx}))

			var body Transactions
			require.NoError(t, rlp.DecodeBytes(buf.Bytes(), &body))
			require.Len(t, body, 1)
			require.Equal(t, tx.Hash(), body[0].Hash())
		})
	}
}

// The type byte is part of the block encoding, so a change to it is a fork. It
// avoids 0x7e and 0x7a to 0x7c, which are transaction types on other chains that
// shared tooling already decodes as theirs.
func TestStakingTx_TypeByte(t *testing.T) {
	require.Equal(t, 0x79, StakingTxType)

	enc, err := NewTx(&StakingTx{BlockNumber: 256, StakeUpdate: true}).MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, "79c6820100c0c001", common.Bytes2Hex(enc))
}

func TestStakingTx_HashSensitivity(t *testing.T) {
	t.Parallel()

	base := NewTx(testStakingTx()).Hash()
	require.Equal(t, base, NewTx(testStakingTx()).Hash())

	mutations := map[string]func(*StakingTx){
		"block number":      func(tx *StakingTx) { tx.BlockNumber++ },
		"stake update flag": func(tx *StakingTx) { tx.StakeUpdate = false },
		"status record ID":  func(tx *StakingTx) { tx.StatusRecords[0].ID++ },
		"in set":            func(tx *StakingTx) { tx.StatusRecords[0].Validators[0].InSet = false },
		"accepted nonce":    func(tx *StakingTx) { tx.StatusRecords[1].Validators[0].AcceptedNonce++ },
		"status order": func(tx *StakingTx) {
			tx.StatusRecords[0], tx.StatusRecords[1] = tx.StatusRecords[1], tx.StatusRecords[0]
		},
		"dropped record":    func(tx *StakingTx) { tx.StatusRecords = tx.StatusRecords[:1] },
		"report end block":  func(tx *StakingTx) { tx.Reports[0].EndBlock++ },
		"report milestones": func(tx *StakingTx) { tx.Reports[0].Milestones++ },
		"ethereum stake": func(tx *StakingTx) {
			tx.Reports[0].Validators[0].EthereumStake.Add(tx.Reports[0].Validators[0].EthereumStake, common.Big1)
		},
		"polygon stake":  func(tx *StakingTx) { tx.Reports[0].Validators[0].PolygonStake = big.NewInt(1) },
		"in transit":     func(tx *StakingTx) { tx.Reports[0].Validators[1].InTransit = big.NewInt(1) },
		"voting power":   func(tx *StakingTx) { tx.Reports[0].Validators[0].VotingPower++ },
		"dropped report": func(tx *StakingTx) { tx.Reports = nil },
	}

	seen := map[common.Hash]string{base: "base"}

	for name, mutate := range mutations {
		tx := testStakingTx()
		mutate(tx)

		hash := NewTx(tx).Hash()
		require.NotContains(t, seen, hash, "%s gives the same hash as %s", name, seen[hash])
		seen[hash] = name
	}
}

// A transaction with only a stake update call has nothing but the block number
// to tell it apart from the one 256 blocks earlier.
func TestStakingTx_HashUniquePerBlock(t *testing.T) {
	t.Parallel()

	first := NewTx(&StakingTx{BlockNumber: 256, StakeUpdate: true})
	second := NewTx(&StakingTx{BlockNumber: 512, StakeUpdate: true})
	require.NotEqual(t, first.Hash(), second.Hash())
}

func TestStakingTx_CopyIsDeep(t *testing.T) {
	t.Parallel()

	orig := testStakingTx()
	tx := NewTx(orig) // NewTx copies
	hash := tx.Hash()

	orig.StatusRecords[0].Validators[0].InSet = false
	orig.Reports[0].Validators[0].EthereumStake.SetUint64(1)
	orig.Reports[0].Validators = nil

	require.Equal(t, hash, NewTx(testStakingTx()).Hash())
	require.Equal(t, testStakingTx(), tx.GetStakingTx())
	require.Nil(t, NewTx(&StateSyncTx{}).GetStakingTx())
}

// A nil amount encodes as zero, so both must give one hash.
func TestStakingTx_NilAmountIsZero(t *testing.T) {
	t.Parallel()

	withNil := &StakingTx{Reports: []StakingReport{{ID: 1, Validators: []StakingParticipation{{ValidatorID: 1}}}}}
	withZero := &StakingTx{Reports: []StakingReport{{ID: 1, Validators: []StakingParticipation{{
		ValidatorID: 1, EthereumStake: new(big.Int), PolygonStake: new(big.Int), InTransit: new(big.Int),
	}}}}}

	require.Equal(t, NewTx(withZero).Hash(), NewTx(withNil).Hash())
}

func TestStakingTx_PublicFields(t *testing.T) {
	t.Parallel()

	tx := NewTx(testStakingTx())

	require.Equal(t, common.Address{}, *tx.To())
	require.Zero(t, tx.Gas())
	require.Zero(t, tx.Nonce())
	require.Zero(t, tx.Value().Sign())
	require.Empty(t, tx.Data())

	tip, err := tx.EffectiveGasTip(big.NewInt(1e9))
	require.NoError(t, err)
	require.Zero(t, tip.Sign())

	sender, err := Sender(LatestSignerForChainID(big.NewInt(137)), tx)
	require.NoError(t, err)
	require.Equal(t, common.Address{}, sender)
}

func TestStakingTx_JSON(t *testing.T) {
	t.Parallel()

	tx := NewTx(testStakingTx())

	enc, err := json.Marshal(tx)
	require.NoError(t, err)

	var dec Transaction
	require.NoError(t, json.Unmarshal(enc, &dec))
	require.Equal(t, uint8(StakingTxType), dec.Type())
}

func TestStakingTx_ReceiptRoundTrip(t *testing.T) {
	t.Parallel()

	receipt := &Receipt{
		Type:              StakingTxType,
		Status:            ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		Logs: []*Log{{
			Address: common.HexToAddress("0x2825f62686974624Af583e23dDd7C7Fe8F1B12ff"),
			Topics:  []common.Hash{common.HexToHash("0x01")},
			Data:    []byte{1, 2, 3},
		}},
	}
	receipt.Bloom = CreateBloom(receipt)

	enc, err := receipt.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, byte(StakingTxType), enc[0])

	var dec Receipt
	require.NoError(t, dec.UnmarshalBinary(enc))
	require.Equal(t, uint8(StakingTxType), dec.Type)
	require.Equal(t, receipt.CumulativeGasUsed, dec.CumulativeGasUsed)
	require.Len(t, dec.Logs, 1)
	require.Equal(t, receipt.Logs[0].Address, dec.Logs[0].Address)

	// The receipts root is derived through EncodeIndex.
	var buf bytes.Buffer
	Receipts{receipt}.EncodeIndex(0, &buf)
	require.Equal(t, enc, buf.Bytes())
}

// A transaction with no records keeps nil lists through a copy.
func TestStakingTx_CopyKeepsNil(t *testing.T) {
	t.Parallel()

	empty := NewTx(&StakingTx{BlockNumber: 256, StakeUpdate: true}).GetStakingTx()
	require.Nil(t, empty.StatusRecords)
	require.Nil(t, empty.Reports)

	noValidators := NewTx(&StakingTx{Reports: []StakingReport{{ID: 1}}}).GetStakingTx()
	require.Len(t, noValidators.Reports, 1)
	require.Nil(t, noValidators.Reports[0].Validators)

	var missing *StakingTx
	require.Nil(t, missing.copy())
}

func TestStakingTx_Accessors(t *testing.T) {
	t.Parallel()

	inner := testStakingTx()
	zero := big.NewInt(0)

	require.Equal(t, byte(StakingTxType), inner.txType())
	require.Nil(t, inner.chainID())
	require.Nil(t, inner.accessList())
	require.Equal(t, []byte{}, inner.data())
	require.Zero(t, inner.gas())
	require.Zero(t, inner.nonce())
	require.Equal(t, zero, inner.gasPrice())
	require.Equal(t, zero, inner.gasTipCap())
	require.Equal(t, zero, inner.gasFeeCap())
	require.Equal(t, zero, inner.value())
	require.Equal(t, &common.Address{}, inner.to())
	require.Equal(t, zero, inner.effectiveGasPrice(new(big.Int), big.NewInt(1e9)))

	v, r, s := inner.rawSignatureValues()
	require.Equal(t, zero, v)
	require.Equal(t, zero, r)
	require.Equal(t, zero, s)

	require.Panics(t, func() { inner.setSignatureValues(nil, nil, nil, nil) })
	require.Panics(t, func() { inner.sigHash(nil) })
}

func TestStakingTx_NilEncodeDecode(t *testing.T) {
	t.Parallel()

	var missing *StakingTx

	require.Error(t, missing.encode(new(bytes.Buffer)))
	require.Error(t, missing.decode([]byte{0xc0}))

	var tx StakingTx
	require.Error(t, tx.decode([]byte{0xff}), "malformed payload")
}

func TestStateSyncTxIndex(t *testing.T) {
	t.Parallel()

	user := NewTx(&LegacyTx{Nonce: 1})
	stateSync := NewTx(&StateSyncTx{})
	staking := NewTx(&StakingTx{BlockNumber: 1})

	tests := []struct {
		name string
		txs  []*Transaction
		want int
	}{
		{"no transactions", nil, -1},
		{"user transactions only", []*Transaction{user, user}, -1},
		{"state sync last", []*Transaction{user, stateSync}, 1},
		{"state sync alone", []*Transaction{stateSync}, 0},
		{"state sync before staking", []*Transaction{user, stateSync, staking}, 1},
		{"staking alone", []*Transaction{staking}, -1},
		{"staking after a user transaction", []*Transaction{user, staking}, -1},
		{"state sync not at the end", []*Transaction{stateSync, user}, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, StateSyncTxIndex(tt.txs))
		})
	}

	require.True(t, stateSync.IsBorSystemTx())
	require.True(t, staking.IsBorSystemTx())
	require.False(t, user.IsBorSystemTx())
}
