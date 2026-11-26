package bor

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/contract"
	"github.com/ethereum/go-ethereum/consensus/bor/staking"
	"github.com/ethereum/go-ethereum/consensus/bor/statefull"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

var (
	stakingContractAddr     = common.HexToAddress("0x2825f62686974624Af583e23dDd7C7Fe8F1B12ff")
	distributorContractAddr = common.HexToAddress("0x42C7e1476115f1A4Bb65a4ccf8e27f65299c1486")
)

// stakingHeimdall serves staking records the way Heimdall's queries do, and
// records each request.
type stakingHeimdall struct {
	mockHeimdallClient

	statuses []*staking.ValidatorStatusRecord
	reports  []*staking.ParticipationReport
	err      error

	mu    sync.Mutex
	calls []stakingCall
}

type stakingCall struct {
	list   string
	fromID uint64
	to     int64
}

func (h *stakingHeimdall) record(list string, fromID uint64, to int64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.calls = append(h.calls, stakingCall{list, fromID, to})
}

func (h *stakingHeimdall) taken() []stakingCall {
	h.mu.Lock()
	defer h.mu.Unlock()

	calls := h.calls
	h.calls = nil

	return calls
}

func (h *stakingHeimdall) ValidatorStatusRecords(_ context.Context, fromID uint64, to int64) ([]*staking.ValidatorStatusRecord, error) {
	h.record("status", fromID, to)

	if h.err != nil {
		return nil, h.err
	}

	var page []*staking.ValidatorStatusRecord

	for _, r := range h.statuses {
		if r.ID >= fromID {
			page = append(page, r)
		}
	}

	return page, nil
}

func (h *stakingHeimdall) ParticipationReports(_ context.Context, fromID uint64, to int64) ([]*staking.ParticipationReport, error) {
	h.record("report", fromID, to)

	if h.err != nil {
		return nil, h.err
	}

	var page []*staking.ParticipationReport

	for _, r := range h.reports {
		if r.ID >= fromID {
			page = append(page, r)
		}
	}

	return page, nil
}

func loadStakingRecords(t *testing.T) ([]*staking.ValidatorStatusRecord, []*staking.ParticipationReport) {
	t.Helper()

	data, err := os.ReadFile("staking/testdata/records.json")
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

// newStakingChain builds a chain whose genesis holds the staking contracts
// (unless withContracts is false) and whose NativeStaking fork is at fork.
func newStakingChain(t *testing.T, sprint, fork uint64, withContracts bool, heimdall *stakingHeimdall) (*core.BlockChain, *Bor, *types.Header, *state.StateDB) {
	t.Helper()

	addr1 := common.HexToAddress("0x1")
	borCfg := borConfigWithDelays(sprint)
	borCfg.RioBlock = big.NewInt(0)
	borCfg.AustinBlock = big.NewInt(0)
	borCfg.IndoreBlock = big.NewInt(0)
	borCfg.MadhugiriBlock = big.NewInt(0)
	borCfg.StateSyncConfirmationDelay = map[string]uint64{"0": 128}
	borCfg.NativeStakingBlock = new(big.Int).SetUint64(fork)
	borCfg.StakingContract = stakingContractAddr.Hex()
	borCfg.DistributorContract = distributorContractAddr.Hex()
	cfg := newAllForksChainConfig(borCfg)

	withAlloc := func(g *core.Genesis) {
		if !withContracts {
			return
		}

		data, err := os.ReadFile("testdata/staking-contracts-alloc.json")
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &g.Alloc))
	}

	sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
	chain, b := newChainAndBorForTestWithConfig(t, sp, cfg, true, addr1, uint64(time.Now().Unix())-200, withAlloc)
	t.Cleanup(chain.Stop)

	b.GenesisContractsClient = &mockGenesisContractForCommitStatesIndore{lastStateID: 0, gasUsed: 100}
	b.StakingContractsClient = contract.NewStakingContractsClient(cfg)
	b.SetHeimdallClient(heimdall)

	genesis := chain.HeaderChain().GetHeaderByNumber(0)
	require.NotNil(t, genesis)

	statedb, err := chain.StateAt(genesis.Root)
	require.NoError(t, err)

	return chain, b, genesis, statedb
}

// stakingFinalizers are the three finalize paths. The validating one needs
// the block body its producer built, so it first assembles the block on a copy
// of the state.
func stakingFinalizers(heimdall *stakingHeimdall) map[string]finalizeFunc {
	finalizers := uburuFinalizers()
	finalizers["Finalize"] = func(b *Bor, chain *core.BlockChain, header *types.Header, statedb *state.StateDB) error {
		block, _, _, err := b.FinalizeAndAssemble(chain, types.CopyHeader(header), statedb.Copy(), &types.Body{}, nil)
		if err != nil {
			return err
		}

		heimdall.taken()

		receipts, err := b.Finalize(chain.HeaderChain(), header, statedb, block.Body(), nil)
		if err != nil {
			return err
		}

		if len(receipts) != len(block.Transactions()) {
			return errors.New("receipt count differs from transaction count")
		}

		return nil
	}

	return finalizers
}

func lastStakingIDs(t *testing.T, b *Bor, chain *core.BlockChain, header *types.Header, statedb *state.StateDB) (uint64, uint64) {
	t.Helper()

	cx := statefull.ChainContext{Chain: chain.HeaderChain(), Bor: b}

	statusID, err := b.StakingContractsClient.LastStatusID(statedb, header, cx, vm.Config{})
	require.NoError(t, err)

	reportID, err := b.StakingContractsClient.LastReportID(statedb, header, cx, vm.Config{})
	require.NoError(t, err)

	return statusID, reportID
}

// TestFinalize_StakingRecordDelivery checks, on every finalize path, that
// records are delivered from the fork block's first sprint start on, after
// state syncs and in ID order, and not before the fork or between sprints.
func TestValidateStakingConfig(t *testing.T) {
	t.Parallel()

	const address = "0x2825f62686974624Af583e23dDd7C7Fe8F1B12ff"

	tests := []struct {
		name    string
		cfg     *params.BorConfig
		wantErr string
	}{
		{name: "no bor config"},
		{name: "fork unset, no contracts", cfg: &params.BorConfig{}},
		{name: "fork unset, bad contracts ignored", cfg: &params.BorConfig{StakingContract: "x", DistributorContract: "y"}},
		{name: "fork set, both contracts", cfg: &params.BorConfig{NativeStakingBlock: big.NewInt(0), StakingContract: address, DistributorContract: address}},
		{name: "fork set, no staking contract", cfg: &params.BorConfig{NativeStakingBlock: big.NewInt(0), DistributorContract: address}, wantErr: "stakingContract"},
		{name: "fork set, bad staking contract", cfg: &params.BorConfig{NativeStakingBlock: big.NewInt(0), StakingContract: "0x12", DistributorContract: address}, wantErr: "stakingContract"},
		{name: "fork set, no distributor contract", cfg: &params.BorConfig{NativeStakingBlock: big.NewInt(0), StakingContract: address}, wantErr: "distributorContract"},
		{name: "fork set, bad distributor contract", cfg: &params.BorConfig{NativeStakingBlock: big.NewInt(0), StakingContract: address, DistributorContract: "zz"}, wantErr: "distributorContract"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateStakingConfig(tt.cfg)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestFinalize_StakingRecordDelivery(t *testing.T) {
	t.Parallel()

	const fork = 32 // a sprint start with sprint 16

	for _, name := range []string{"Finalize", "FinalizeForPipeline", "FinalizeAndAssemble"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			statuses, reports := loadStakingRecords(t)
			heimdall := &stakingHeimdall{statuses: statuses, reports: reports}
			finalize := stakingFinalizers(heimdall)[name]
			chain, b, genesis, statedb := newStakingChain(t, 16, fork, true, heimdall)

			tests := []struct {
				number     uint64
				wantCalls  []stakingCall
				wantStatus uint64
				wantReport uint64
			}{
				{number: 16},       // sprint start before the fork
				{number: fork - 1}, // not a sprint start
				{
					number: fork,
					// The second request of each list finds nothing new and ends the delivery.
					wantCalls:  []stakingCall{{"status", 1, 0}, {"status", 3, 0}, {"report", 1, 0}, {"report", 2, 0}},
					wantStatus: 2,
					wantReport: 1,
				},
				{number: fork + 1, wantStatus: 2, wantReport: 1}, // not a sprint start
				{
					number:     fork + 16,
					wantCalls:  []stakingCall{{"status", 3, 0}, {"report", 2, 0}},
					wantStatus: 2,
					wantReport: 1,
				},
			}

			for _, tt := range tests {
				header := uburuHeader(genesis, b, tt.number)
				require.NoError(t, finalize(b, chain, header, statedb), "block %d", tt.number)

				to := int64(header.Time) - 128
				for i := range tt.wantCalls {
					tt.wantCalls[i].to = to
				}

				require.Equal(t, tt.wantCalls, heimdall.taken(), "block %d", tt.number)

				statusID, reportID := lastStakingIDs(t, b, chain, header, statedb)
				require.Equal(t, tt.wantStatus, statusID, "block %d", tt.number)
				require.Equal(t, tt.wantReport, reportID, "block %d", tt.number)
			}
		})
	}
}

func TestFinalize_StakingRecordFetchErrorFailsBlock(t *testing.T) {
	t.Parallel()

	const fork = 32

	for _, name := range []string{"Finalize", "FinalizeForPipeline", "FinalizeAndAssemble"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			errUnavailable := errors.New("heimdall unavailable")
			heimdall := &stakingHeimdall{err: errUnavailable}
			finalize := stakingFinalizers(heimdall)[name]
			chain, b, genesis, statedb := newStakingChain(t, 16, fork, true, heimdall)

			header := uburuHeader(genesis, b, fork)
			require.ErrorIs(t, finalize(b, chain, header, statedb), errUnavailable)

			// Before the fork the queries are not made, so the same failure is harmless.
			require.NoError(t, finalize(b, chain, uburuHeader(genesis, b, 16), statedb))
		})
	}
}

func TestFinalize_StakingContractsMissingFailsBlock(t *testing.T) {
	t.Parallel()

	const fork = 32

	for _, name := range []string{"Finalize", "FinalizeForPipeline", "FinalizeAndAssemble"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			statuses, reports := loadStakingRecords(t)
			heimdall := &stakingHeimdall{statuses: statuses, reports: reports}
			finalize := stakingFinalizers(heimdall)[name]
			chain, b, genesis, statedb := newStakingChain(t, 16, fork, false, heimdall)

			err := finalize(b, chain, uburuHeader(genesis, b, fork), statedb)
			require.ErrorContains(t, err, "reading last validator status ID")
			require.Empty(t, heimdall.taken())
		})
	}
}

func TestDeliverRecords(t *testing.T) {
	t.Parallel()

	errFetch := errors.New("fetch failed")
	errApply := errors.New("apply failed")

	tests := []struct {
		name        string
		lastID      uint64
		pages       map[uint64][]uint64 // fromID -> record IDs returned
		fetchErrAt  uint64
		applyErrAt  uint64
		wantApplied []uint64
		wantErr     error
	}{
		{name: "nothing new", lastID: 4, pages: map[uint64][]uint64{}},
		{name: "one page then empty", lastID: 0, pages: map[uint64][]uint64{1: {1, 2, 3}}, wantApplied: []uint64{1, 2, 3}},
		{name: "several pages", lastID: 2, pages: map[uint64][]uint64{3: {3, 4}, 5: {5}}, wantApplied: []uint64{3, 4, 5}},
		{name: "records at or below the last ID skipped", lastID: 2, pages: map[uint64][]uint64{3: {1, 2, 3}}, wantApplied: []uint64{3}},
		{name: "gap ends the delivery", lastID: 0, pages: map[uint64][]uint64{1: {1, 3, 4}}, wantApplied: []uint64{1}},
		{name: "gap at the start delivers nothing", lastID: 0, pages: map[uint64][]uint64{1: {2}}},
		{name: "fetch error on the first page", lastID: 0, fetchErrAt: 1, wantErr: errFetch},
		{name: "fetch error on a later page", lastID: 0, pages: map[uint64][]uint64{1: {1}}, fetchErrAt: 2, wantApplied: []uint64{1}, wantErr: errFetch},
		{name: "apply error", lastID: 0, pages: map[uint64][]uint64{1: {1, 2}}, applyErrAt: 2, wantApplied: []uint64{1}, wantErr: errApply},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var applied []uint64

			delivered, err := deliverRecords(tt.lastID,
				func(fromID uint64) ([]uint64, error) {
					if fromID == tt.fetchErrAt {
						return nil, errFetch
					}

					return tt.pages[fromID], nil
				},
				func(id uint64) uint64 { return id },
				func(id uint64) error {
					if id == tt.applyErrAt {
						return errApply
					}

					applied = append(applied, id)

					return nil
				},
			)

			require.ErrorIs(t, err, tt.wantErr)
			require.Equal(t, tt.wantApplied, applied)
			require.Equal(t, len(tt.wantApplied), delivered)
		})
	}
}

var stakeUpdateTopic = crypto.Keccak256Hash([]byte("StakeUpdate(uint256,uint256,uint256,uint256)"))

func stakingTestEVM(cfg *params.ChainConfig, statedb *state.StateDB) *vm.EVM {
	return vm.NewEVM(vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		BlockNumber: big.NewInt(1),
		Difficulty:  big.NewInt(1),
		GasLimit:    30_000_000,
	}, statedb, cfg, vm.Config{})
}

// registerValidator sends register(bytes) with a self-stake as a normal
// call, which marks the new validator's stake as changed.
func registerValidator(t *testing.T, cfg *params.ChainConfig, statedb *state.StateDB, seed byte) {
	t.Helper()

	owner := common.BytesToAddress([]byte{0xa0, seed})
	stake := uint256.NewInt(1e18)
	statedb.AddBalance(owner, stake, tracing.BalanceChangeUnspecified)

	pubkey := make([]byte, 64)
	pubkey[0] = seed

	data := crypto.Keccak256([]byte("register(bytes)"))[:4]
	data = append(data, common.BigToHash(big.NewInt(32)).Bytes()...)
	data = append(data, common.BigToHash(big.NewInt(64)).Bytes()...)
	data = append(data, pubkey...)

	_, _, err := stakingTestEVM(cfg, statedb).Call(owner, stakingContractAddr, data, 1_000_000, stake)
	require.NoError(t, err)
}

// pendingStakeUpdates returns how many validators have stake that changed
// since their last stake update.
func pendingStakeUpdates(t *testing.T, cfg *params.ChainConfig, statedb *state.StateDB) uint64 {
	t.Helper()

	data := crypto.Keccak256([]byte("pendingStakeUpdates()"))[:4]
	ret, _, err := stakingTestEVM(cfg, statedb).StaticCall(common.Address{}, stakingContractAddr, data, 1_000_000)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ret), 64)

	return new(big.Int).SetBytes(ret[32:64]).Uint64()
}

func stakeUpdateLogs(statedb *state.StateDB) int {
	count := 0

	for _, l := range statedb.Logs() {
		if l.Address == stakingContractAddr && len(l.Topics) > 0 && l.Topics[0] == stakeUpdateTopic {
			count++
		}
	}

	return count
}

// TestFinalize_StakeUpdateCall checks, on every finalize path, that the stake
// update call runs on multiples of 256 from the fork block on, whether or not
// the block starts a sprint.
func TestFinalize_StakeUpdateCall(t *testing.T) {
	t.Parallel()

	const fork = 512

	// 256 divides by 16 but not by 100, so the second run has no sprint start
	// on any stake update block.
	for _, sprint := range []uint64{16, 100} {
		for _, name := range []string{"Finalize", "FinalizeForPipeline", "FinalizeAndAssemble"} {
			t.Run(name+"/sprint"+new(big.Int).SetUint64(sprint).String(), func(t *testing.T) {
				t.Parallel()

				heimdall := &stakingHeimdall{}
				finalize := stakingFinalizers(heimdall)[name]
				chain, b, genesis, statedb := newStakingChain(t, sprint, fork, true, heimdall)
				cfg := b.chainConfig

				registerValidator(t, cfg, statedb, 1)
				require.Equal(t, uint64(1), pendingStakeUpdates(t, cfg, statedb))

				tests := []struct {
					number      uint64
					register    bool
					wantPending uint64
					wantLogs    int
				}{
					{number: 256, wantPending: 1},                      // a multiple of 256 before the fork
					{number: fork - 1, wantPending: 1},                 // N-1
					{number: fork, wantLogs: 1},                        // N
					{number: fork + 1, register: true, wantPending: 1}, // N+1, with new stake
					{number: fork + 255, wantPending: 1},
					{number: fork + 256, wantLogs: 1},
					{number: fork + 512}, // nothing changed: the call logs nothing
				}

				logs := 0

				for _, tt := range tests {
					if tt.register {
						registerValidator(t, cfg, statedb, 2)
					}

					require.NoError(t, finalize(b, chain, uburuHeader(genesis, b, tt.number), statedb), "block %d", tt.number)

					logs += tt.wantLogs
					require.Equal(t, tt.wantPending, pendingStakeUpdates(t, cfg, statedb), "block %d", tt.number)
					require.Equal(t, logs, stakeUpdateLogs(statedb), "block %d", tt.number)
				}
			})
		}
	}
}

// produceStakingBlock assembles block number on a fresh state, through
// FinalizeAndAssemble or through the pipelined pair.
func produceStakingBlock(t *testing.T, b *Bor, chain *core.BlockChain, genesis *types.Header, number uint64, pipelined bool, prepare userTxFunc) (*types.Block, []*types.Receipt) {
	t.Helper()

	statedb, err := chain.StateAt(genesis.Root)
	require.NoError(t, err)

	body, userReceipts := prepare(statedb)
	header := uburuHeader(genesis, b, number)

	if !pipelined {
		block, receipts, _, err := b.FinalizeAndAssemble(chain, header, statedb, body, userReceipts)
		require.NoError(t, err)

		return block, receipts
	}

	work, err := b.FinalizeForPipeline(chain.HeaderChain(), header, statedb, body, userReceipts)
	require.NoError(t, err)

	root := statedb.IntermediateRoot(true)
	block, receipts, err := b.AssembleBlock(chain, header, statedb, body, userReceipts, root, work)
	require.NoError(t, err)

	return block, receipts
}

// userTxFunc stands in for the block's user transactions: it changes the
// state and returns the body and receipts those transactions would leave.
type userTxFunc func(*state.StateDB) (*types.Body, []*types.Receipt)

// registerAsUserTx registers a validator as the block's only user
// transaction, so its logs are in that transaction's receipt.
func registerAsUserTx(t *testing.T, cfg *params.ChainConfig) userTxFunc {
	t.Helper()

	return func(statedb *state.StateDB) (*types.Body, []*types.Receipt) {
		registerValidator(t, cfg, statedb, 1)

		tx := types.NewTx(&types.LegacyTx{Nonce: 1, Gas: 21000})
		receipt := &types.Receipt{Status: types.ReceiptStatusSuccessful, CumulativeGasUsed: 21000, TxHash: tx.Hash(), Logs: statedb.Logs()}
		receipt.Bloom = types.CreateBloom(receipt)

		return &types.Body{Transactions: types.Transactions{tx}}, []*types.Receipt{receipt}
	}
}

func noUserTx(*state.StateDB) (*types.Body, []*types.Receipt) {
	return &types.Body{}, nil
}

// TestStakingTx_ProduceAndValidate builds blocks on both producing paths and
// has a validating node, with its own state and its own Heimdall answers,
// rebuild the system transactions and receipts.
func TestStakingTx_ProduceAndValidate(t *testing.T) {
	t.Parallel()

	statuses, reports := loadStakingRecords(t)
	stateSync := []*clerk.EventRecordWithTime{{
		EventRecord: clerk.EventRecord{ID: 1, Contract: common.HexToAddress("0x1001"), Data: []byte{0x01}, ChainID: "1"},
		Time:        time.Unix(1, 0),
	}}

	tests := []struct {
		name          string
		sprint        uint64
		fork          uint64
		number        uint64
		stateSync     bool
		wantStateSync bool
		wantStatuses  int
		wantReports   int
		wantUpdate    bool
		wantLogs      int // in the staking receipt
	}{
		{
			name: "state sync, records and stake update", sprint: 16, fork: 512, number: 512, stateSync: true,
			wantStateSync: true, wantStatuses: 2, wantReports: 1, wantUpdate: true,
			wantLogs: 4, // two status records, one report, one StakeUpdate
		},
		{
			name: "records only", sprint: 16, fork: 16, number: 16,
			wantStatuses: 2, wantReports: 1, wantLogs: 3,
		},
		{
			name: "stake update only, off a sprint start", sprint: 100, fork: 512, number: 768,
			wantUpdate: true, wantLogs: 1,
		},
		{
			name: "stake update with nothing to log", sprint: 100, fork: 512, number: 1024,
			wantUpdate: true,
		},
	}

	for _, tt := range tests {
		for _, pipelined := range []bool{false, true} {
			name := tt.name
			if pipelined {
				name += "/pipelined"
			}

			t.Run(name, func(t *testing.T) {
				t.Parallel()

				heimdall := &stakingHeimdall{statuses: statuses, reports: reports}
				if tt.stateSync {
					heimdall.events = stateSync
				}

				chain, b, genesis, _ := newStakingChain(t, tt.sprint, tt.fork, true, heimdall)

				prepare, userTxs := userTxFunc(noUserTx), 0
				if tt.wantLogs > tt.wantStatuses+tt.wantReports {
					prepare, userTxs = registerAsUserTx(t, b.chainConfig), 1
				}

				block, receipts := produceStakingBlock(t, b, chain, genesis, tt.number, pipelined, prepare)

				// Both producing paths build the same block.
				other, _ := produceStakingBlock(t, b, chain, genesis, tt.number, !pipelined, prepare)
				require.Equal(t, types.DeriveSha(block.Transactions(), trie.NewStackTrie(nil)), types.DeriveSha(other.Transactions(), trie.NewStackTrie(nil)))
				require.Equal(t, block.ReceiptHash(), other.ReceiptHash())

				txs := block.Transactions()
				wantTxs := userTxs + 1
				if tt.wantStateSync {
					wantTxs++
				}

				require.Len(t, txs, wantTxs)
				require.Len(t, receipts, wantTxs)

				if tt.wantStateSync {
					i := userTxs
					require.Equal(t, uint8(types.StateSyncTxType), txs[i].Type())
					require.Equal(t, i, types.StateSyncTxIndex(txs))
					require.Equal(t, uint8(types.StateSyncTxType), receipts[i].Type)
					require.Equal(t, txs[i].Hash(), receipts[i].TxHash)
					require.Equal(t, uint(i), receipts[i].TransactionIndex)
					require.Empty(t, receipts[i].Logs, "staking logs must not land in the state-sync receipt")
				} else {
					require.Equal(t, -1, types.StateSyncTxIndex(txs))
				}

				stakingTx := txs[len(txs)-1].GetStakingTx()
				require.NotNil(t, stakingTx)
				require.Equal(t, tt.number, stakingTx.BlockNumber)
				require.Len(t, stakingTx.StatusRecords, tt.wantStatuses)
				require.Len(t, stakingTx.Reports, tt.wantReports)
				require.Equal(t, tt.wantUpdate, stakingTx.StakeUpdate)

				receipt := receipts[len(receipts)-1]
				require.Equal(t, uint8(types.StakingTxType), receipt.Type)
				require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
				require.Equal(t, txs[len(txs)-1].Hash(), receipt.TxHash)
				require.Equal(t, uint(len(txs)-1), receipt.TransactionIndex)
				require.Zero(t, receipt.GasUsed)
				require.Len(t, receipt.Logs, tt.wantLogs)

				for _, l := range receipt.Logs {
					require.Contains(t, []common.Address{stakingContractAddr, distributorContractAddr}, l.Address)
					require.Equal(t, uint(len(txs)-1), l.TxIndex)
				}

				if tt.wantUpdate && tt.wantLogs > 0 {
					last := receipt.Logs[len(receipt.Logs)-1]
					require.Equal(t, stakeUpdateTopic, last.Topics[0], "the stake update call comes last")
				}

				validate := func(body *types.Body) ([]*types.Receipt, error) {
					statedb, err := chain.StateAt(genesis.Root)
					require.NoError(t, err)

					_, userReceipts := prepare(statedb)

					return b.Finalize(chain.HeaderChain(), types.CopyHeader(block.Header()), statedb, body, userReceipts)
				}

				validated, err := validate(block.Body())
				require.NoError(t, err)
				require.Equal(t, block.ReceiptHash(), types.DeriveSha(types.Receipts(validated), trie.NewStackTrie(nil)))

				t.Run("missing staking transaction", func(t *testing.T) {
					_, err := validate(&types.Body{Transactions: txs[:len(txs)-1]})
					require.ErrorIs(t, err, core.ErrStakingTxMismatch)
				})

				t.Run("staking transaction for another block", func(t *testing.T) {
					tampered := *stakingTx
					tampered.BlockNumber++

					body := &types.Body{Transactions: append(append(types.Transactions{}, txs[:len(txs)-1]...), types.NewTx(&tampered))}
					_, err := validate(body)
					require.ErrorIs(t, err, core.ErrStakingTxMismatch)
				})

				t.Run("staking transaction with a changed record", func(t *testing.T) {
					if tt.wantStatuses == 0 {
						t.Skip("no records in this block")
					}

					tampered := *txs[len(txs)-1].GetStakingTx()
					tampered.StatusRecords[0].Validators[0].InSet = !tampered.StatusRecords[0].Validators[0].InSet

					body := &types.Body{Transactions: append(append(types.Transactions{}, txs[:len(txs)-1]...), types.NewTx(&tampered))}
					_, err := validate(body)
					require.ErrorIs(t, err, core.ErrStakingTxMismatch)
				})

				if tt.wantStateSync {
					n := len(txs)

					t.Run("system transactions out of order", func(t *testing.T) {
						swapped := append(append(types.Transactions{}, txs[:n-2]...), txs[n-1], txs[n-2])
						_, err := validate(&types.Body{Transactions: swapped})
						require.ErrorIs(t, err, core.ErrStakingTxMismatch)
					})

					t.Run("missing state-sync transaction", func(t *testing.T) {
						without := append(append(types.Transactions{}, txs[:n-2]...), txs[n-1])
						_, err := validate(&types.Body{Transactions: without})
						require.ErrorIs(t, err, core.ErrStateSyncMismatch)
					})
				}
			})
		}
	}
}

// Before the fork, or on a block with nothing due, a staking transaction in
// the body gets no receipt, which the block processors reject as a count
// mismatch.
func TestStakingTx_UnexpectedInBody(t *testing.T) {
	t.Parallel()

	const fork = 512

	tests := []struct {
		name   string
		number uint64
	}{
		{"before the fork, on a multiple of 256", 256},
		{"after the fork, nothing due", fork + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chain, b, genesis, statedb := newStakingChain(t, 100, fork, true, &stakingHeimdall{})
			body := &types.Body{Transactions: types.Transactions{types.NewTx(&types.StakingTx{BlockNumber: tt.number, StakeUpdate: true})}}

			receipts, err := b.Finalize(chain.HeaderChain(), uburuHeader(genesis, b, tt.number), statedb, body, nil)
			require.NoError(t, err)
			require.Empty(t, receipts)
		})
	}
}

// failingStakingContract returns an error from the one call named by failOn
// and otherwise behaves like a contract with nothing delivered.
type failingStakingContract struct {
	failOn string
	err    error
}

func (f failingStakingContract) fail(call string) error {
	if f.failOn == call {
		return f.err
	}

	return nil
}

func (f failingStakingContract) LastStatusID(vm.StateDB, *types.Header, statefull.ChainContext, vm.Config) (uint64, error) {
	return 0, f.fail("LastStatusID")
}

func (f failingStakingContract) LastReportID(vm.StateDB, *types.Header, statefull.ChainContext, vm.Config) (uint64, error) {
	return 0, f.fail("LastReportID")
}

func (f failingStakingContract) ApplyValidatorStatus(*staking.ValidatorStatusRecord, vm.StateDB, *types.Header, statefull.ChainContext, vm.Config) (uint64, error) {
	return 0, f.fail("ApplyValidatorStatus")
}

func (f failingStakingContract) ApplyParticipationReport(*staking.ParticipationReport, vm.StateDB, *types.Header, statefull.ChainContext, vm.Config) (uint64, error) {
	return 0, f.fail("ApplyParticipationReport")
}

func (f failingStakingContract) LogStakeUpdates(vm.StateDB, *types.Header, statefull.ChainContext, vm.Config) (uint64, error) {
	return 0, f.fail("LogStakeUpdates")
}

// TestCommitStaking_ContractErrors checks that an error from any contract
// call fails the staking step, and that without one the step reports what it
// delivered.
func TestCommitStaking_ContractErrors(t *testing.T) {
	t.Parallel()

	const number = 512 // a sprint start and a multiple of 256

	errCall := errors.New("call failed")
	statuses, reports := loadStakingRecords(t)

	tests := []struct {
		failOn  string
		wantErr string
	}{
		{"LastStatusID", "reading last validator status ID"},
		{"ApplyValidatorStatus", "delivering validator status records after id 0"},
		{"LastReportID", "reading last participation report ID"},
		{"ApplyParticipationReport", "delivering participation reports after id 0"},
		{"LogStakeUpdates", "logging stake updates"},
	}

	for _, tt := range tests {
		t.Run(tt.failOn, func(t *testing.T) {
			t.Parallel()

			heimdall := &stakingHeimdall{statuses: statuses, reports: reports}
			chain, b, genesis, statedb := newStakingChain(t, 16, 0, true, heimdall)
			b.StakingContractsClient = failingStakingContract{failOn: tt.failOn, err: errCall}

			work, err := b.commitStaking(statedb, uburuHeader(genesis, b, number), statefull.ChainContext{Chain: chain.HeaderChain(), Bor: b})
			require.ErrorIs(t, err, errCall)
			require.ErrorContains(t, err, tt.wantErr)
			require.Nil(t, work)
		})
	}

	t.Run("no error", func(t *testing.T) {
		t.Parallel()

		heimdall := &stakingHeimdall{statuses: statuses, reports: reports}
		chain, b, genesis, statedb := newStakingChain(t, 16, 0, true, heimdall)
		b.StakingContractsClient = failingStakingContract{}

		work, err := b.commitStaking(statedb, uburuHeader(genesis, b, number), statefull.ChainContext{Chain: chain.HeaderChain(), Bor: b})
		require.NoError(t, err)
		require.NotNil(t, work)
		require.Equal(t, uint64(number), work.tx.BlockNumber)
		require.Len(t, work.tx.StatusRecords, len(statuses))
		require.Len(t, work.tx.Reports, len(reports))
		require.True(t, work.tx.StakeUpdate)
		require.Zero(t, work.logStart)
	})
}

func TestSystemWork(t *testing.T) {
	t.Parallel()

	stateSync := []*types.StateSyncData{{ID: 1}}
	staking := &stakingWork{tx: &types.StakingTx{BlockNumber: 1}}

	require.Nil(t, newSystemWork(nil, nil))
	require.Nil(t, newSystemWork([]*types.StateSyncData{}, nil))

	var none *SystemWork
	require.Nil(t, none.stateSyncData())
	require.Nil(t, none.stakingWork())

	onlyStateSync := newSystemWork(stateSync, nil)
	require.NotNil(t, onlyStateSync)
	require.Equal(t, stateSync, onlyStateSync.stateSyncData())
	require.Nil(t, onlyStateSync.stakingWork())

	onlyStaking := newSystemWork(nil, staking)
	require.NotNil(t, onlyStaking)
	require.Empty(t, onlyStaking.stateSyncData())
	require.Same(t, staking, onlyStaking.stakingWork())

	both := newSystemWork(stateSync, staking)
	require.Equal(t, stateSync, both.stateSyncData())
	require.Same(t, staking, both.stakingWork())
}

// A gap ends the delivery at once: asking Heimdall again would return the
// same gap.
func TestDeliverRecords_FetchCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		pages       map[uint64][]uint64
		wantFetches []uint64
	}{
		{"nothing new", map[uint64][]uint64{}, []uint64{1}},
		{"one page, then an empty one", map[uint64][]uint64{1: {1, 2}}, []uint64{1, 3}},
		{"gap after some records", map[uint64][]uint64{1: {1, 3}}, []uint64{1}},
		{"gap at the start", map[uint64][]uint64{1: {2}}, []uint64{1}},
		{"only records already delivered", map[uint64][]uint64{1: {0}}, []uint64{1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var fetches []uint64

			_, err := deliverRecords(0,
				func(fromID uint64) ([]uint64, error) {
					fetches = append(fetches, fromID)
					return tt.pages[fromID], nil
				},
				func(id uint64) uint64 { return id },
				func(uint64) error { return nil },
			)
			require.NoError(t, err)
			require.Equal(t, tt.wantFetches, fetches)
		})
	}
}

func TestTxRecords(t *testing.T) {
	t.Parallel()

	status := statusTxRecord(&staking.ValidatorStatusRecord{
		ID:         4,
		RecordTime: time.Unix(1, 0),
		Validators: []staking.ValidatorStatus{{ValidatorID: 1, InSet: true, AcceptedNonce: 9}, {ValidatorID: 2}},
	})
	require.Equal(t, types.StakingStatusRecord{
		ID:         4,
		Validators: []types.StakingValidatorStatus{{ValidatorID: 1, InSet: true, AcceptedNonce: 9}, {ValidatorID: 2}},
	}, status)

	report := reportTxRecord(&staking.ParticipationReport{
		ID: 5, RecordTime: time.Unix(1, 0), StartBlock: 10, EndBlock: 20, Milestones: 3,
		Validators: []staking.Participation{{
			ValidatorID: 1, MilestonesVoted: 2, EthereumStake: big.NewInt(7), PolygonStake: big.NewInt(8), InTransit: big.NewInt(9), VotingPower: 15,
		}},
	})
	require.Equal(t, types.StakingReport{
		ID: 5, StartBlock: 10, EndBlock: 20, Milestones: 3,
		Validators: []types.StakingParticipation{{
			ValidatorID: 1, MilestonesVoted: 2, EthereumStake: big.NewInt(7), PolygonStake: big.NewInt(8), InTransit: big.NewInt(9), VotingPower: 15,
		}},
	}, report)
}
