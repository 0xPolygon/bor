//go:build integration
// +build integration

package bor

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	borSpan "github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/consensus/bor/staking"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

var (
	stakingContract     = common.HexToAddress("0x2825f62686974624Af583e23dDd7C7Fe8F1B12ff")
	distributorContract = common.HexToAddress("0x42C7e1476115f1A4Bb65a4ccf8e27f65299c1486")
)

func loadStakingFixtureRecords(t *testing.T) ([]*staking.ValidatorStatusRecord, []*staking.ParticipationReport) {
	t.Helper()

	data, err := os.ReadFile("../../consensus/bor/staking/testdata/records.json")
	require.NoError(t, err)

	var fixture struct {
		ValidatorStatus []struct {
			Record *staking.ValidatorStatusRecord `json:"record"`
		} `json:"validator_status"`
		ParticipationReports []struct {
			Record *staking.ParticipationReport `json:"record"`
		} `json:"participation_reports"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))

	var (
		statuses []*staking.ValidatorStatusRecord
		reports  []*staking.ParticipationReport
	)

	for _, entry := range fixture.ValidatorStatus {
		statuses = append(statuses, entry.Record)
	}

	for _, entry := range fixture.ParticipationReports {
		reports = append(reports, entry.Record)
	}

	return statuses, reports
}

// TestStakingTx_BuiltAndImported builds, seals and imports a sprint-start
// block that carries both a state-sync transaction and a staking transaction,
// then keeps the chain going. Import re-derives both from the node's own
// Heimdall answers and checks the receipts root.
func TestStakingTx_BuiltAndImported(t *testing.T) {
	t.Parallel()

	const stateSyncConfirmationDelay = int64(128)

	updateGenesis := func(gen *core.Genesis) {
		gen.Config.Bor.StateSyncConfirmationDelay = map[string]uint64{"0": uint64(stateSyncConfirmationDelay)}
		gen.Config.Bor.Sprint = map[string]uint64{"0": sprintSize}
		gen.Config.Bor.MadhugiriBlock = big.NewInt(0)
		gen.Config.Bor.NativeStakingBlock = big.NewInt(0)
		gen.Config.Bor.StakingContract = stakingContract.Hex()
		gen.Config.Bor.DistributorContract = distributorContract.Hex()

		data, err := os.ReadFile("../../consensus/bor/testdata/staking-contracts-alloc.json")
		require.NoError(t, err)

		var alloc types.GenesisAlloc
		require.NoError(t, json.Unmarshal(data, &alloc))

		for address, account := range alloc {
			gen.Alloc[address] = account
		}
	}

	init := buildEthereumInstance(t, rawdb.NewMemoryDatabase(), updateGenesis)
	chain := init.ethereum.BlockChain()
	_bor := init.ethereum.Engine().(*bor.Bor)
	defer _bor.Close()

	block := init.genesis.ToBlock()

	span0 := createMockSpan(addr, chain.Config().ChainID.String())
	borValSet := borSpan.ConvertHeimdallValSetToBorValSet(span0.ValidatorSet)
	_bor.SetSpanner(getMockedSpanner(t, borValSet.Validators))

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	h := createMockHeimdall(ctrl, &span0, loadSpanFromFile(t))

	// The first sprint start is block sprintSize. Three 1s blocks and one 6s
	// sprint-end block put it 9s after genesis.
	to := int64(chain.GetHeaderByNumber(0).Time) + 9 - stateSyncConfirmationDelay

	sample := getSampleEventRecord(t)
	sample.Time = time.Unix(to-3, 0)
	events := generateFakeStateSyncEvents(sample, 2)

	statuses, reports := loadStakingFixtureRecords(t)

	h.EXPECT().StateSyncEvents(gomock.Any(), uint64(1), to).Return(events, nil).AnyTimes()
	h.EXPECT().StateSyncEvents(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	h.EXPECT().ValidatorStatusRecords(gomock.Any(), uint64(1), to).Return(statuses, nil).AnyTimes()
	h.EXPECT().ValidatorStatusRecords(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	h.EXPECT().ParticipationReports(gomock.Any(), uint64(1), to).Return(reports, nil).AnyTimes()
	h.EXPECT().ParticipationReports(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	h.EXPECT().GetLatestSpan(gomock.Any()).Return(nil, fmt.Errorf("span not found")).AnyTimes()
	_bor.SetHeimdallClient(h)

	for i := uint64(1); i <= sprintSize; i++ {
		block = buildNextBlock(t, _bor, chain, block, nil, init.genesis.Config.Bor, nil, borValSet.Validators, false, nil, nil)
		insertNewBlock(t, chain, block)
	}

	sprintStart := chain.GetBlockByNumber(sprintSize)
	require.NotNil(t, sprintStart)

	txs := sprintStart.Transactions()
	require.Len(t, txs, 2, "state-sync and staking transactions should be in the block body")
	require.Equal(t, uint8(types.StateSyncTxType), txs[0].Type())
	require.Equal(t, uint8(types.StakingTxType), txs[1].Type())

	stakingTx := txs[1].GetStakingTx()
	require.Equal(t, sprintSize, stakingTx.BlockNumber)
	require.Len(t, stakingTx.StatusRecords, len(statuses))
	require.Len(t, stakingTx.Reports, len(reports))
	require.False(t, stakingTx.StakeUpdate)

	receipts := chain.GetReceiptsByHash(sprintStart.Hash())
	require.Len(t, receipts, 2)
	require.Equal(t, uint8(types.StateSyncTxType), receipts[0].Type)
	require.Equal(t, txs[0].Hash(), receipts[0].TxHash)

	receipt := receipts[1]
	require.Equal(t, uint8(types.StakingTxType), receipt.Type)
	require.Equal(t, txs[1].Hash(), receipt.TxHash)
	require.Equal(t, uint(1), receipt.TransactionIndex)
	require.Len(t, receipt.Logs, len(statuses)+len(reports), "one log per record delivered")

	for _, l := range receipt.Logs {
		require.Contains(t, []common.Address{stakingContract, distributorContract}, l.Address)
		require.Equal(t, sprintStart.Hash(), l.BlockHash)
		require.Equal(t, txs[1].Hash(), l.TxHash)
		require.Equal(t, uint(1), l.TxIndex)
	}

	for _, l := range receipts[0].Logs {
		require.NotContains(t, []common.Address{stakingContract, distributorContract}, l.Address, "staking logs belong to the staking receipt")
	}

	// The next sprint has nothing new to deliver, so its start has no staking transaction.
	for i := sprintSize + 1; i <= 2*sprintSize; i++ {
		block = buildNextBlock(t, _bor, chain, block, nil, init.genesis.Config.Bor, nil, borValSet.Validators, false, nil, nil)
		insertNewBlock(t, chain, block)
	}

	for _, tx := range chain.GetBlockByNumber(2 * sprintSize).Transactions() {
		require.NotEqual(t, uint8(types.StakingTxType), tx.Type())
	}
}
