package contract

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/staking"
	"github.com/ethereum/go-ethereum/consensus/bor/statefull"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

var (
	testStakingAddr     = common.HexToAddress("0x2825f62686974624Af583e23dDd7C7Fe8F1B12ff")
	testDistributorAddr = common.HexToAddress("0x42C7e1476115f1A4Bb65a4ccf8e27f65299c1486")
)

// headerReader is the part of a chain the block context reads.
type headerReader struct{ cfg *params.ChainConfig }

func (r headerReader) Config() *params.ChainConfig                 { return r.cfg }
func (r headerReader) CurrentHeader() *types.Header                { return nil }
func (r headerReader) GetHeader(common.Hash, uint64) *types.Header { return nil }
func (r headerReader) GetHeaderByNumber(uint64) *types.Header      { return nil }
func (r headerReader) GetHeaderByHash(common.Hash) *types.Header   { return nil }
func (r headerReader) GetTd(common.Hash, uint64) *big.Int          { return nil }

type stakingEnv struct {
	client  *StakingContractsClient
	statedb *state.StateDB
	header  *types.Header
	chain   statefull.ChainContext
}

// newStakingEnv builds a state holding the staking contracts, unless
// withContracts is false, and a client for them.
func newStakingEnv(t *testing.T, withContracts bool) *stakingEnv {
	t.Helper()

	cfg := &params.ChainConfig{
		ChainID:             big.NewInt(1),
		HomesteadBlock:      big.NewInt(0),
		EIP150Block:         big.NewInt(0),
		EIP155Block:         big.NewInt(0),
		EIP158Block:         big.NewInt(0),
		ByzantiumBlock:      big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
		PetersburgBlock:     big.NewInt(0),
		IstanbulBlock:       big.NewInt(0),
		BerlinBlock:         big.NewInt(0),
		LondonBlock:         big.NewInt(0),
		ShanghaiBlock:       big.NewInt(0),
		CancunBlock:         big.NewInt(0),
		Bor: &params.BorConfig{
			NativeStakingBlock:  big.NewInt(0),
			StakingContract:     testStakingAddr.Hex(),
			DistributorContract: testDistributorAddr.Hex(),
		},
	}

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)

	if withContracts {
		data, err := os.ReadFile("../testdata/staking-contracts-alloc.json")
		require.NoError(t, err)

		var alloc types.GenesisAlloc
		require.NoError(t, json.Unmarshal(data, &alloc))

		for address, account := range alloc {
			statedb.SetCode(address, account.Code, tracing.CodeChangeUnspecified)

			for slot, value := range account.Storage {
				statedb.SetState(address, slot, value)
			}
		}
	}

	return &stakingEnv{
		client:  NewStakingContractsClient(cfg),
		statedb: statedb,
		header:  &types.Header{Number: big.NewInt(16), Difficulty: big.NewInt(1), GasLimit: 30_000_000, Time: 1_000},
		chain:   statefull.ChainContext{Chain: headerReader{cfg}},
	}
}

func (e *stakingEnv) lastIDs(t *testing.T) (uint64, uint64) {
	t.Helper()

	statusID, err := e.client.LastStatusID(e.statedb, e.header, e.chain, vm.Config{})
	require.NoError(t, err)

	reportID, err := e.client.LastReportID(e.statedb, e.header, e.chain, vm.Config{})
	require.NoError(t, err)

	return statusID, reportID
}

func TestStakingContractsClient_DeliversRecordsInOrder(t *testing.T) {
	t.Parallel()

	env := newStakingEnv(t, true)
	fixture := loadStakingFixture(t)

	statusID, reportID := env.lastIDs(t)
	require.Zero(t, statusID)
	require.Zero(t, reportID)

	for i, entry := range fixture.ValidatorStatus {
		gasUsed, err := env.client.ApplyValidatorStatus(&entry.Record, env.statedb, env.header, env.chain, vm.Config{})
		require.NoError(t, err)
		require.Positive(t, gasUsed)

		statusID, reportID = env.lastIDs(t)
		require.Equal(t, uint64(i+1), statusID)
		require.Zero(t, reportID, "status records go to the staking contract only")
	}

	for i, entry := range fixture.ParticipationReports {
		gasUsed, err := env.client.ApplyParticipationReport(&entry.Record, env.statedb, env.header, env.chain, vm.Config{})
		require.NoError(t, err)
		require.Positive(t, gasUsed)

		statusID, reportID = env.lastIDs(t)
		require.Equal(t, uint64(len(fixture.ValidatorStatus)), statusID)
		require.Equal(t, uint64(i+1), reportID)
	}
}

// A record that is not the next one reverts in the contract. Like a state
// sync, that is not an error for the block, and nothing is delivered.
func TestStakingContractsClient_RevertIsNotAnError(t *testing.T) {
	t.Parallel()

	env := newStakingEnv(t, true)

	_, err := env.client.ApplyValidatorStatus(&staking.ValidatorStatusRecord{ID: 5}, env.statedb, env.header, env.chain, vm.Config{})
	require.NoError(t, err)

	_, err = env.client.ApplyParticipationReport(&staking.ParticipationReport{ID: 5}, env.statedb, env.header, env.chain, vm.Config{})
	require.NoError(t, err)

	statusID, reportID := env.lastIDs(t)
	require.Zero(t, statusID)
	require.Zero(t, reportID)
}

// The staking calls return no data. A call counts as failed only when the EVM
// says so, and a failed call still uses gas and leaves the state unchanged.
func TestStakingContractsClient_ExecuteReportsTheCallError(t *testing.T) {
	t.Parallel()

	env := newStakingEnv(t, true)

	next, err := PackValidatorStatus(&staking.ValidatorStatusRecord{ID: 1})
	require.NoError(t, err)

	gasUsed, err := env.client.execute(testStakingAddr, next, env.statedb, env.header, env.chain, vm.Config{})
	require.NoError(t, err)
	require.NotZero(t, gasUsed)

	skipped, err := PackValidatorStatus(&staking.ValidatorStatusRecord{ID: 5})
	require.NoError(t, err)

	gasUsed, err = env.client.execute(testStakingAddr, skipped, env.statedb, env.header, env.chain, vm.Config{})
	require.ErrorIs(t, err, vm.ErrExecutionReverted)
	require.NotZero(t, gasUsed)
	require.Less(t, gasUsed, params.MaxTxGas)

	statusID, _ := env.lastIDs(t)
	require.Equal(t, uint64(1), statusID)
}

func TestStakingContractsClient_PackErrors(t *testing.T) {
	t.Parallel()

	env := newStakingEnv(t, true)
	report := &staking.ParticipationReport{ID: 1, Validators: []staking.Participation{{ValidatorID: 7}}}

	_, err := env.client.ApplyParticipationReport(report, env.statedb, env.header, env.chain, vm.Config{})
	require.ErrorContains(t, err, "packing participation report 1")
}

func TestStakingContractsClient_LastIDErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		code    []byte // installed at both addresses; nil leaves them without code
		wantErr string
	}{
		{name: "no code", wantErr: "unpacking"},
		{name: "returns nothing", code: common.FromHex("0x00"), wantErr: "unpacking"},
		{name: "reverts", code: common.FromHex("0x60006000fd"), wantErr: "calling"},
		{
			// PUSH9 2^64, MSTORE at 0, RETURN 32 bytes.
			name:    "ID above 64 bits",
			code:    common.FromHex("0x6801000000000000000060005260206000f3"),
			wantErr: errLastIDOverflow.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := newStakingEnv(t, false)
			if tt.code != nil {
				env.statedb.SetCode(testStakingAddr, tt.code, tracing.CodeChangeUnspecified)
				env.statedb.SetCode(testDistributorAddr, tt.code, tracing.CodeChangeUnspecified)
			}

			_, err := env.client.LastStatusID(env.statedb, env.header, env.chain, vm.Config{})
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorContains(t, err, lastStatusIDMethod)

			_, err = env.client.LastReportID(env.statedb, env.header, env.chain, vm.Config{})
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorContains(t, err, lastReportIDMethod)
		})
	}
}

func TestStakingContractsClient_LastIDFitsUint64(t *testing.T) {
	t.Parallel()

	env := newStakingEnv(t, false)
	// PUSH8 2^64-1, MSTORE at 0, RETURN 32 bytes.
	env.statedb.SetCode(testStakingAddr, common.FromHex("0x67ffffffffffffffff60005260206000f3"), tracing.CodeChangeUnspecified)

	lastID, err := env.client.LastStatusID(env.statedb, env.header, env.chain, vm.Config{})
	require.NoError(t, err)
	require.Equal(t, uint64(1<<64-1), lastID)
}

func TestStakingContractsClient_LogStakeUpdates(t *testing.T) {
	t.Parallel()

	env := newStakingEnv(t, true)

	// Registering a validator marks its stake as changed.
	owner := common.HexToAddress("0xa001")
	stake := uint256.NewInt(1e18)
	env.statedb.AddBalance(owner, stake, tracing.BalanceChangeUnspecified)

	register := append(crypto.Keccak256([]byte("register(bytes)"))[:4], common.LeftPadBytes([]byte{32}, 32)...)
	register = append(register, common.LeftPadBytes([]byte{64}, 32)...)
	register = append(register, make([]byte, 64)...)

	evm := vm.NewEVM(vm.BlockContext{
		CanTransfer: func(db vm.StateDB, from common.Address, amount *uint256.Int) bool {
			return db.GetBalance(from).Cmp(amount) >= 0
		},
		Transfer: func(db vm.StateDB, from, to common.Address, amount *uint256.Int) {
			db.SubBalance(from, amount, tracing.BalanceChangeTransfer)
			db.AddBalance(to, amount, tracing.BalanceChangeTransfer)
		},
		BlockNumber: big.NewInt(1),
		Difficulty:  big.NewInt(1),
		GasLimit:    30_000_000,
	}, env.statedb, env.client.chainConfig, vm.Config{})

	_, _, err := evm.Call(owner, testStakingAddr, register, 1_000_000, stake)
	require.NoError(t, err)

	before := len(env.statedb.Logs())

	gasUsed, err := env.client.LogStakeUpdates(env.statedb, env.header, env.chain, vm.Config{})
	require.NoError(t, err)
	require.Positive(t, gasUsed)
	require.Len(t, env.statedb.Logs(), before+1, "one StakeUpdate for the one validator whose stake changed")

	// Nothing changed since, so a second call logs nothing.
	_, err = env.client.LogStakeUpdates(env.statedb, env.header, env.chain, vm.Config{})
	require.NoError(t, err)
	require.Len(t, env.statedb.Logs(), before+1)
}
