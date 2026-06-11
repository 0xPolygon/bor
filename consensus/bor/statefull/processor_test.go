package statefull

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// Tiny stub bytecodes used by the EVM-driven tests below. We don't need real
// state-receiver logic — `ApplyStateSyncEvents` doesn't inspect return values, so
// any contract that the EVM can dispatch to is enough to exercise the dispatch +
// gas accumulation paths.
var (
	// successStub: PUSH1 0x00, PUSH1 0x00, RETURN — returns empty data, no revert.
	successStub = []byte{0x60, 0x00, 0x60, 0x00, 0xf3}

	// revertStub: PUSH1 0x00, PUSH1 0x00, REVERT — reverts with empty data.
	revertStub = []byte{0x60, 0x00, 0x60, 0x00, 0xfd}

	stateReceiverAddr = common.HexToAddress("0x0000000000000000000000000000000000001001")
)

func TestGetSystemMessage_FieldsArePinned(t *testing.T) {
	to := common.HexToAddress("0x0000000000000000000000000000000000001001")
	data := []byte("calldata-payload")

	msg := GetSystemMessage(to, data)

	// From must be BorSystemAddress — system contracts check msg.sender against this
	// (see contract-interaction-security.md). Any drift breaks consensus.
	require.Equal(t, params.BorSystemAddress, msg.From())
	require.Equal(t, &to, msg.To())
	require.Equal(t, params.MaxTxGas, msg.Gas())
	require.Equal(t, big.NewInt(0), msg.GasPrice())
	require.Equal(t, big.NewInt(0), msg.Value())
	require.Equal(t, data, msg.Data())
}

// TestApplyStateSyncEvents_NonStateSyncTx_NoOp checks if `ApplyStateSyncEvents` bypasses
// legacy transaction sent for tracing.
func TestApplyStateSyncEvents_NonStateSyncTx_NoOp(t *testing.T) {
	// A legacy tx has nil StateSyncData. Function must short-circuit without
	// touching the EVM — important because callers may pass tx context for
	// non-state-sync txs at the boundary of the dispatch helpers.
	evm := newTestEVM(t, nil)
	legacy := types.NewTx(&types.LegacyTx{Nonce: 0, GasPrice: new(big.Int), Gas: 21000})

	res, err := ApplyStateSyncEvents(t.Context(), evm, legacy, &core.Message{}, stateReceiverAddr)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, uint64(0), res.UsedGas)
	require.Nil(t, res.ReturnData)
}

// TestApplyStateSyncEvents_EmptyEvents_NoOp checks if `ApplyStateSyncEvents` bypasses
// state-sync transaction with no events (not possible on production but a valid input).
func TestApplyStateSyncEvents_EmptyEvents_NoOp(t *testing.T) {
	// StateSyncTx with zero events is a valid wire form (empty block of bridge
	// events). It must return early — no SetTxContext, no system call.
	evm := newTestEVM(t, nil)
	tx := types.NewTx(&types.StateSyncTx{StateSyncData: nil})

	res, err := ApplyStateSyncEvents(t.Context(), evm, tx, &core.Message{}, stateReceiverAddr)
	require.NoError(t, err)
	require.Equal(t, uint64(0), res.UsedGas)
}

func TestApplyStateSyncEvents_HappyPath(t *testing.T) {
	// Deploys a "success" stub at the state receiver address and confirms:
	//   1. Each event is dispatched (totalGasUsed accumulates).
	//   2. No error is returned.
	// The stub doesn't care about calldata shape — the test exercises the
	// encode → ABI pack → call path, not the contract behaviour.
	evm := newTestEVM(t, map[common.Address][]byte{stateReceiverAddr: successStub})

	events := []*types.StateSyncData{
		{ID: 1, Contract: common.HexToAddress("0x2001"), Data: []byte("event-1"), TxHash: common.HexToHash("0x01")},
		{ID: 2, Contract: common.HexToAddress("0x2002"), Data: []byte("event-2"), TxHash: common.HexToHash("0x02")},
		{ID: 3, Contract: common.HexToAddress("0x2003"), Data: []byte("event-3"), TxHash: common.HexToHash("0x03")},
	}
	tx := types.NewTx(&types.StateSyncTx{StateSyncData: events})

	res, err := ApplyStateSyncEvents(t.Context(), evm, tx, newSystemMessage(), stateReceiverAddr)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NoError(t, res.Err)
	require.Greater(t, res.UsedGas, uint64(0), "expected non-zero gas across 3 events")
}

func TestApplyStateSyncEvents_RevertDoesNotPropagate(t *testing.T) {
	// Production semantics (see consensus/bor/statefull/processor.go:196): a reverted
	// commitState call must not fail the surrounding tx. Gas of the reverted call still
	// counts toward totalGasUsed.
	evm := newTestEVM(t, map[common.Address][]byte{stateReceiverAddr: revertStub})

	tx := types.NewTx(&types.StateSyncTx{
		StateSyncData: []*types.StateSyncData{
			{ID: 1, Contract: common.HexToAddress("0x2001"), Data: []byte("e"), TxHash: common.HexToHash("0x01")},
		},
	})

	res, err := ApplyStateSyncEvents(t.Context(), evm, tx, newSystemMessage(), stateReceiverAddr)
	require.NoError(t, err, "revert must not bubble up to ApplyStateSyncEvents")
	require.NoError(t, res.Err)
	require.Greater(t, res.UsedGas, uint64(0), "reverted call still consumes gas")
}

// TestApplyStateSyncEvents_ContextCancelled verifies that the loop aborts when ctx is
// already cancelled, before processing any event.
func TestApplyStateSyncEvents_ContextCancelled(t *testing.T) {
	evm := newTestEVM(t, map[common.Address][]byte{stateReceiverAddr: successStub})
	tx := types.NewTx(&types.StateSyncTx{
		StateSyncData: []*types.StateSyncData{
			{ID: 1, Contract: common.HexToAddress("0x2001")},
			{ID: 2, Contract: common.HexToAddress("0x2002")},
		},
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := ApplyStateSyncEvents(ctx, evm, tx, newSystemMessage(), stateReceiverAddr)
	require.ErrorIs(t, err, context.Canceled)
}

// TestApplyBorMessage_PropagatesRevertInResult validates that `ApplyBorMessage` populates
// any EVM failure error in result.Err field.
func TestApplyBorMessage_PropagatesRevertInResult(t *testing.T) {
	// Contract guarantee: ApplyBorMessage returns nil error even on EVM revert,
	// but result.Err carries the EVM failure.
	evm := newTestEVM(t, map[common.Address][]byte{stateReceiverAddr: revertStub})
	msg := GetSystemMessage(stateReceiverAddr, []byte{})

	result, err := ApplyBorMessage(evm, msg)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Error(t, result.Err, "EVM revert must surface in result.Err")
	require.Greater(t, result.UsedGas, uint64(0))
}

func TestApplyBorMessage_SuccessPath(t *testing.T) {
	evm := newTestEVM(t, map[common.Address][]byte{stateReceiverAddr: successStub})
	msg := GetSystemMessage(stateReceiverAddr, []byte{})

	result, err := ApplyBorMessage(evm, msg)
	require.NoError(t, err)
	require.NoError(t, result.Err)
	require.Greater(t, result.UsedGas, uint64(0))
}

func TestApplyMessage_SuccessPath(t *testing.T) {
	// ApplyMessage builds its own EVM internally, so we need to provide a chain
	// context and header. The state-receiver-shaped test bypasses the validator
	// contract branch — log.Error on non-zero `success` for non-validator addresses
	// is acceptable in unit tests (logged, not returned).
	cfg := newTestChainConfig()
	statedb := newStateDBWithCode(t, map[common.Address][]byte{stateReceiverAddr: successStub})
	header := newTestHeader()

	msg := GetSystemMessage(stateReceiverAddr, []byte{})
	gasUsed, err := ApplyMessage(context.Background(), msg, statedb, header, cfg, &stubChainContext{cfg: cfg}, vm.Config{})
	require.NoError(t, err)
	require.Greater(t, gasUsed, uint64(0))
}

// newSystemMessage produces a minimal core.Message suitable for SetTxContext.
func newSystemMessage() *core.Message {
	return &core.Message{
		From:     params.BorSystemAddress,
		GasPrice: big.NewInt(0),
	}
}

func newTestChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:             big.NewInt(137),
		HomesteadBlock:      big.NewInt(0),
		EIP150Block:         big.NewInt(0),
		EIP155Block:         big.NewInt(0),
		EIP158Block:         big.NewInt(0),
		ByzantiumBlock:      big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
		PetersburgBlock:     big.NewInt(0),
		IstanbulBlock:       big.NewInt(0),
		MuirGlacierBlock:    big.NewInt(0),
		BerlinBlock:         big.NewInt(0),
		LondonBlock:         big.NewInt(0),
		Bor: &params.BorConfig{
			Period:                map[string]uint64{"0": 2},
			Sprint:                map[string]uint64{"0": 16},
			ValidatorContract:     "0x0000000000000000000000000000000000001000",
			StateReceiverContract: "0x0000000000000000000000000000000000001001",
			MadhugiriBlock:        big.NewInt(0),
		},
	}
}

func newTestHeader() *types.Header {
	return &types.Header{
		Number:     big.NewInt(1),
		Time:       1700000000,
		GasLimit:   30_000_000,
		Coinbase:   common.HexToAddress("0xbeef"),
		Difficulty: big.NewInt(1),
	}
}

// newStateDBWithCode returns a fresh in-memory StateDB with the given account
// code allocations. The accounts are funded with 1 ETH so EVM call value
// transfers (always zero in our tests, but still) don't bounce.
func newStateDBWithCode(t *testing.T, alloc map[common.Address][]byte) *state.StateDB {
	t.Helper()
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	for addr, code := range alloc {
		statedb.SetCode(addr, code, tracing.CodeChangeGenesis)
		statedb.AddBalance(addr, uint256.NewInt(params.Ether), tracing.BalanceChangeUnspecified)
	}
	statedb.AddBalance(params.BorSystemAddress, uint256.NewInt(params.Ether), tracing.BalanceChangeUnspecified)
	return statedb
}

// newTestEVM builds an EVM rooted at a fresh state DB with the given account
// code allocations. Block context uses an explicit author so the (nil) chain
// context is never consulted — keeping unit tests free of full-chain wiring.
func newTestEVM(t *testing.T, alloc map[common.Address][]byte) *vm.EVM {
	t.Helper()
	cfg := newTestChainConfig()
	statedb := newStateDBWithCode(t, alloc)
	header := newTestHeader()

	author := header.Coinbase
	blockCtx := core.NewEVMBlockContext(header, &stubChainContext{cfg: cfg}, &author)
	return vm.NewEVM(blockCtx, statedb, cfg, vm.Config{})
}

// stubChainContext satisfies core.ChainContext but never serves real headers.
// Suitable for system calls that don't touch BLOCKHASH — the author is always
// provided to NewEVMBlockContext, so Engine() is never invoked.
type stubChainContext struct {
	cfg *params.ChainConfig
}

func (s *stubChainContext) Config() *params.ChainConfig                 { return s.cfg }
func (s *stubChainContext) CurrentHeader() *types.Header                { return nil }
func (s *stubChainContext) GetHeader(common.Hash, uint64) *types.Header { return nil }
func (s *stubChainContext) GetHeaderByNumber(uint64) *types.Header      { return nil }
func (s *stubChainContext) GetHeaderByHash(common.Hash) *types.Header   { return nil }
func (s *stubChainContext) GetTd(common.Hash, uint64) *big.Int          { return nil }
func (s *stubChainContext) Engine() consensus.Engine                    { return nil }
