// Package registrytest builds a working reserved blockspace registry inside an
// in-memory state and returns a registryreader.Reader that talks to it. It is
// shared across module-level tests (core, core/txpool, miner) so the deploy +
// initialize + createClient sequence isn't duplicated three times.
package registrytest

import (
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	borabi "github.com/ethereum/go-ethereum/consensus/bor/abi"
	"github.com/ethereum/go-ethereum/consensus/bor/registryreader"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// writeABI exposes the contract write methods that tests need to drive
// (initialize, createClient). The shared bor/abi package only ships the read
// side because production code never invokes the write side from Go — admins
// mutate the registry via normal user transactions.
const writeABI = `[
	{"inputs":[{"name":"initialOwner","type":"address"},{"name":"maxTotalGas","type":"uint64"},{"name":"maxClientGas","type":"uint64"}],"name":"initialize","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"admin","type":"address"},{"name":"gasQuota","type":"uint64"},{"name":"metadata","type":"string"},{"name":"addresses","type":"address[]"}],"name":"createClient","outputs":[{"name":"clientId","type":"uint256"}],"stateMutability":"nonpayable","type":"function"}
]`

// Harness exposes a Reader bound to an in-memory state with the registry
// deployed and one client registered, plus the addresses tests assert on.
type Harness struct {
	Reader         registryreader.Reader
	ReservedAddr   common.Address
	UnreservedAddr common.Address
}

// NewHarness deploys the registry, initializes it under a fresh owner, and
// registers one client with one whitelisted address.
func NewHarness(t *testing.T) *Harness {
	t.Helper()

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)

	contractAddr := common.HexToAddress(params.DefaultReservedRegistryContract)
	owner := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	clientAdmin := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	reserved := common.HexToAddress("0x00000000000000000000000000000000000000cc")
	unreserved := common.HexToAddress("0x00000000000000000000000000000000000000dd")

	// Install runtime bytecode directly — the Solidity contract has no
	// constructor (see ReservedBlockspaceRegistry.sol).
	statedb.SetCode(contractAddr, common.FromHex(params.ReservedBlockspaceRegistryCode), tracing.CodeChangeGenesis)
	statedb.AddBalance(owner, uint256.NewInt(params.Ether), tracing.BalanceChangeUnspecified)

	writeAbi, err := abi.JSON(strings.NewReader(writeABI))
	require.NoError(t, err)

	initData, err := writeAbi.Pack("initialize", owner, uint64(200_000_000), uint64(100_000_000))
	require.NoError(t, err)
	callContract(t, statedb, owner, contractAddr, initData)

	createData, err := writeAbi.Pack("createClient", clientAdmin, uint64(10_000_000), "test", []common.Address{reserved})
	require.NoError(t, err)
	callContract(t, statedb, owner, contractAddr, createData)

	return &Harness{
		Reader: &evmReader{
			state:    statedb,
			contract: contractAddr,
			readerAB: borabi.ReservedBlockspaceRegistry(),
		},
		ReservedAddr:   reserved,
		UnreservedAddr: unreserved,
	}
}

func callContract(t *testing.T, statedb *state.StateDB, from, to common.Address, data []byte) {
	t.Helper()
	evm := newEVM(statedb, from)
	ret, _, err := evm.Call(from, to, data, 30_000_000, uint256.NewInt(0))
	if err != nil {
		t.Fatalf("registry call reverted: err=%v ret=0x%x", err, ret)
	}
}

func newEVM(statedb *state.StateDB, author common.Address) *vm.EVM {
	// Built without importing core/ — the harness is shared across module
	// tests including core's own tests, so we can't import core here without
	// creating an import cycle.
	blockCtx := vm.BlockContext{
		CanTransfer: canTransfer,
		Transfer:    transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		Coinbase:    author,
		BlockNumber: big.NewInt(1),
		Time:        1700000000,
		Difficulty:  big.NewInt(1),
		GasLimit:    30_000_000,
	}
	return vm.NewEVM(blockCtx, statedb, testChainConfig(), vm.Config{})
}

func canTransfer(db vm.StateDB, addr common.Address, amount *uint256.Int) bool {
	return db.GetBalance(addr).Cmp(amount) >= 0
}

func transfer(db vm.StateDB, sender, recipient common.Address, amount *uint256.Int) {
	db.SubBalance(sender, amount, tracing.BalanceChangeTransfer)
	db.AddBalance(recipient, amount, tracing.BalanceChangeTransfer)
}

// testChainConfig enables every Ethereum fork the registry bytecode could
// depend on. The contract uses PUSH0 (Shanghai) so ShanghaiBlock must be 0.
func testChainConfig() *params.ChainConfig {
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
		ShanghaiBlock:       big.NewInt(0),
	}
}

// evmReader satisfies registryreader.Reader by running the registry's view
// methods against a fixed in-memory state. Uses a snapshot/revert pair per call
// so view executions never leak state mutations into the harness DB.
type evmReader struct {
	state    *state.StateDB
	contract common.Address
	readerAB abi.ABI
}

func (r *evmReader) HasReservedRegistry() bool {
	return r != nil && r.contract != (common.Address{})
}

func (r *evmReader) IsReservedAddress(_ *state.StateDB, _ uint64, _ common.Hash, account common.Address) (bool, error) {
	values, err := r.call("isReservedAddress", account)
	if err != nil {
		return false, err
	}
	if len(values) != 1 {
		return false, fmt.Errorf("isReservedAddress returned %d values", len(values))
	}
	reserved, ok := values[0].(bool)
	if !ok {
		return false, fmt.Errorf("isReservedAddress returned %T", values[0])
	}
	return reserved, nil
}

func (r *evmReader) ReservedClientForAddress(_ *state.StateDB, _ uint64, _ common.Hash, account common.Address) (registryreader.ClientLookup, error) {
	values, err := r.call("getClientForAddress", account)
	if err != nil {
		return registryreader.ClientLookup{}, err
	}
	if len(values) != 4 {
		return registryreader.ClientLookup{}, fmt.Errorf("getClientForAddress returned %d values", len(values))
	}
	clientID, _ := values[0].(*big.Int)
	gasQuota, _ := values[1].(uint64)
	admin, _ := values[2].(common.Address)
	active, _ := values[3].(bool)
	if clientID == nil {
		return registryreader.ClientLookup{}, fmt.Errorf("getClientForAddress returned nil clientID")
	}
	return registryreader.ClientLookup{
		ClientID: new(big.Int).Set(clientID),
		GasQuota: gasQuota,
		Admin:    admin,
		Active:   active,
	}, nil
}

func (r *evmReader) call(method string, args ...interface{}) ([]interface{}, error) {
	data, err := r.readerAB.Pack(method, args...)
	if err != nil {
		return nil, err
	}
	// View methods don't mutate state, but the EVM still consumes
	// finalisation cycles; snapshot/revert keeps the harness DB pristine.
	snap := r.state.Snapshot()
	defer r.state.RevertToSnapshot(snap)

	caller := common.HexToAddress("0x00000000000000000000000000000000deadbeef")
	evm := newEVM(r.state, caller)
	ret, _, err := evm.Call(caller, r.contract, data, 30_000_000, uint256.NewInt(0))
	if err != nil {
		return nil, err
	}
	return r.readerAB.Unpack(method, ret)
}
