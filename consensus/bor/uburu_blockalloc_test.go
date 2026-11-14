package bor

import (
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/contract"
	"github.com/ethereum/go-ethereum/consensus/bor/statefull"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

var (
	stateReceiverAddr = common.HexToAddress("0x0000000000000000000000000000000000001001")

	// keccak256 of the fixtures, from the genesis-contracts build.
	preUburuStateReceiverHash = common.HexToHash("0x8347355457cf3921b2f8358ef5a79b3935328ae40d6519238f2487525bade90d")
	uburuStateReceiverHash    = common.HexToHash("0xf838690d33083ef341ea6e1b75bf6f66ca0bd521d557a907ab26deafdb7c6b72")
)

func readCodeFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return strings.TrimSpace(string(raw))
}

// stateReceiverSlots fills the six storage slots the pre-Uburu StateReceiver
// layout uses; slot 0 is lastStateId.
func stateReceiverSlots() map[common.Hash]common.Hash {
	slots := make(map[common.Hash]common.Hash, 6)
	for i := int64(0); i < 6; i++ {
		slots[common.BigToHash(big.NewInt(i))] = common.BigToHash(big.NewInt(0x1000 + i))
	}
	return slots
}

func lastStateIDAt(t *testing.T, cfg *params.ChainConfig, statedb *state.StateDB, number uint64) *big.Int {
	t.Helper()
	id, err := callLastStateID(cfg, statedb, number)
	require.NoError(t, err)
	return id
}

func callLastStateID(cfg *params.ChainConfig, statedb *state.StateDB, number uint64) (*big.Int, error) {
	blockCtx := vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		BlockNumber: new(big.Int).SetUint64(number),
		Difficulty:  big.NewInt(1),
		GasLimit:    30_000_000,
	}
	evm := vm.NewEVM(blockCtx, statedb, cfg, vm.Config{})
	ret, _, err := evm.StaticCall(common.Address{}, stateReceiverAddr, common.FromHex("0x5407ca67"), vm.NewGasBudget(100_000, 0))
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(ret), nil
}

type finalizeFunc func(b *Bor, chain *core.BlockChain, header *types.Header, statedb *state.StateDB) error

func uburuFinalizers() map[string]finalizeFunc {
	return map[string]finalizeFunc{
		"Finalize": func(b *Bor, chain *core.BlockChain, header *types.Header, statedb *state.StateDB) error {
			_, err := b.Finalize(chain.HeaderChain(), header, statedb, &types.Body{}, nil)
			return err
		},
		"FinalizeForPipeline": func(b *Bor, chain *core.BlockChain, header *types.Header, statedb *state.StateDB) error {
			_, err := b.FinalizeForPipeline(chain.HeaderChain(), header, statedb, &types.Body{}, nil)
			return err
		},
		"FinalizeAndAssemble": func(b *Bor, chain *core.BlockChain, header *types.Header, statedb *state.StateDB) error {
			_, _, _, err := b.FinalizeAndAssemble(chain, header, statedb, &types.Body{}, nil)
			return err
		},
	}
}

// TestFinalize_UburuStateReceiverSwap checks the StateReceiver code swap at
// N-1 / N / N+1 on every finalize path, on and off a sprint start.
func TestFinalize_UburuStateReceiverSwap(t *testing.T) {
	t.Parallel()

	preCode := readCodeFixture(t, "statereceiver-mainnet-pre-uburu.hex")
	uburuCode := readCodeFixture(t, "statereceiver-mainnet-uburu-oracle0.hex")
	require.Equal(t, preUburuStateReceiverHash, crypto.Keccak256Hash(common.FromHex(preCode)))
	require.Equal(t, uburuStateReceiverHash, crypto.Keccak256Hash(common.FromHex(uburuCode)))

	// The pre-Uburu fixture is the code mainnet runs today.
	mainnetCode := params.BorMainnetChainConfig.Bor.BlockAlloc["62278656"].(map[string]interface{})["0000000000000000000000000000000000001001"].(map[string]interface{})["code"]
	require.Equal(t, mainnetCode, preCode)

	// sprint is 16: 20 is mid-sprint, 32 is a sprint start.
	forkBlocks := []uint64{20, 32}

	for name, finalize := range uburuFinalizers() {
		for _, fork := range forkBlocks {
			t.Run(name+"/"+new(big.Int).SetUint64(fork).String(), func(t *testing.T) {
				t.Parallel()
				testUburuSwap(t, finalize, fork, preCode, uburuCode)
			})
		}
	}
}

func newUburuChain(t *testing.T, fork uint64, preCode, uburuCode string, extra types.GenesisAlloc) (*core.BlockChain, *Bor, *types.Header, *state.StateDB) {
	t.Helper()

	addr1 := common.HexToAddress("0x1")
	borCfg := borConfigWithDelays(16)
	borCfg.RioBlock = big.NewInt(0)
	borCfg.AustinBlock = big.NewInt(0)
	borCfg.IndoreBlock = big.NewInt(0)
	borCfg.StateSyncConfirmationDelay = map[string]uint64{"0": 0}
	borCfg.UburuBlock = new(big.Int).SetUint64(fork)
	borCfg.StateReceiverContract = stateReceiverAddr.Hex()
	borCfg.BlockAlloc = map[string]interface{}{
		borCfg.UburuBlock.String(): map[string]interface{}{
			stateReceiverAddr.Hex(): map[string]interface{}{
				"balance": "0x0",
				"code":    uburuCode,
			},
		},
	}
	cfg := newAllForksChainConfig(borCfg)

	withStateReceiver := func(g *core.Genesis) {
		g.Alloc = types.GenesisAlloc{
			stateReceiverAddr: {Balance: big.NewInt(0), Code: common.FromHex(preCode), Storage: stateReceiverSlots()},
		}
		for addr, account := range extra {
			g.Alloc[addr] = account
		}
	}
	sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
	chain, b := newChainAndBorForTestWithConfig(t, sp, cfg, true, addr1, uint64(time.Now().Unix())-200, withStateReceiver)
	t.Cleanup(chain.Stop)

	genesis := chain.HeaderChain().GetHeaderByNumber(0)
	require.NotNil(t, genesis)
	statedb, err := chain.StateAt(genesis.Root)
	require.NoError(t, err)
	return chain, b, genesis, statedb
}

func uburuHeader(genesis *types.Header, b *Bor, number uint64) *types.Header {
	return &types.Header{
		Number:     new(big.Int).SetUint64(number),
		ParentHash: genesis.Hash(),
		Time:       genesis.Time + number*b.config.Period["0"],
		GasLimit:   genesis.GasLimit,
		Difficulty: big.NewInt(1),
	}
}

func testUburuSwap(t *testing.T, finalize finalizeFunc, fork uint64, preCode, uburuCode string) {
	t.Helper()

	chain, b, genesis, statedb := newUburuChain(t, fork, preCode, uburuCode, nil)
	cfg := b.chainConfig
	slots := stateReceiverSlots()

	cases := []struct {
		number   uint64
		wantHash common.Hash
	}{
		{fork - 1, preUburuStateReceiverHash},
		{fork, uburuStateReceiverHash},
		{fork + 1, uburuStateReceiverHash},
	}
	for _, tc := range cases {
		header := uburuHeader(genesis, b, tc.number)
		require.NoError(t, finalize(b, chain, header, statedb), "block %d", tc.number)

		require.Equal(t, tc.wantHash == uburuStateReceiverHash, b.config.IsUburu(header.Number), "block %d", tc.number)
		require.Equal(t, tc.wantHash, statedb.GetCodeHash(stateReceiverAddr), "block %d", tc.number)
		for slot, want := range slots {
			require.Equal(t, want, statedb.GetState(stateReceiverAddr, slot), "block %d slot %s", tc.number, slot)
		}
		require.Equal(t, slots[common.Hash{}].Big(), lastStateIDAt(t, cfg, statedb, tc.number), "block %d", tc.number)
	}
}

var (
	revertingReceiver = common.HexToAddress("0xa001")
	stoppingReceiver  = common.HexToAddress("0xa002")
	burningReceiver   = common.HexToAddress("0xa003")
)

// failedStateSyncHash returns the failedStateSyncHashes[stateID] slot (mapping
// at slot 6) of the post-Uburu StateReceiver.
func failedStateSyncHash(statedb *state.StateDB, stateID uint64) common.Hash {
	key := crypto.Keccak256Hash(common.BigToHash(new(big.Int).SetUint64(stateID)).Bytes(), common.BigToHash(big.NewInt(6)).Bytes())
	return statedb.GetState(stateReceiverAddr, key)
}

func encodeReceiverData(t *testing.T, receiver common.Address, data []byte) common.Hash {
	t.Helper()
	addressT, err := abi.NewType("address", "", nil)
	require.NoError(t, err)
	bytesT, err := abi.NewType("bytes", "", nil)
	require.NoError(t, err)
	packed, err := abi.Arguments{{Type: addressT}, {Type: bytesT}}.Pack(receiver, data)
	require.NoError(t, err)
	return crypto.Keccak256Hash(packed)
}

// TestUburuCommitStateAfterSwap runs Bor's commitState system call against the
// swapped StateReceiver on the pipelined path: the block after the fork reads
// the new code only through the FlatDiff overlay, not from disk.
func TestUburuCommitStateAfterSwap(t *testing.T) {
	t.Parallel()

	const fork = 20
	receivers := types.GenesisAlloc{
		revertingReceiver: {Balance: big.NewInt(0), Code: common.FromHex("0x60006000fd")},
		stoppingReceiver:  {Balance: big.NewInt(0), Code: common.FromHex("0x00")},
		burningReceiver:   {Balance: big.NewInt(0), Code: common.FromHex("0x5b600056")},
	}
	chain, b, genesis, statedb := newUburuChain(t, fork,
		readCodeFixture(t, "statereceiver-mainnet-pre-uburu.hex"),
		readCodeFixture(t, "statereceiver-mainnet-uburu-oracle0.hex"),
		receivers)

	_, err := b.FinalizeForPipeline(chain.HeaderChain(), uburuHeader(genesis, b, fork), statedb, &types.Body{}, nil)
	require.NoError(t, err)
	diff := statedb.CommitSnapshot(true)
	require.Contains(t, diff.Code, uburuStateReceiverHash)

	next, err := chain.StateAtWithFlatDiff(genesis.Root, diff)
	require.NoError(t, err)
	require.Equal(t, uburuStateReceiverHash, next.GetCodeHash(stateReceiverAddr))

	gc := contract.NewGenesisContractsClient(b.chainConfig, b.config.ValidatorContract, stateReceiverAddr.Hex(), nil)
	header := uburuHeader(genesis, b, 32)
	cx := statefull.ChainContext{Chain: chain.HeaderChain(), Bor: b}
	firstID := stateReceiverSlots()[common.Hash{}].Big().Uint64() + 1

	tests := []struct {
		receiver   common.Address
		wantFailed bool
	}{
		{stoppingReceiver, false},
		{revertingReceiver, true},
		{burningReceiver, true},
	}
	gasByReceiver := make(map[common.Address]uint64, len(tests))
	for i, tc := range tests {
		id := firstID + uint64(i)
		data := []byte{byte(i + 1)}
		event := &clerk.EventRecordWithTime{
			EventRecord: clerk.EventRecord{ID: id, Contract: tc.receiver, Data: data, ChainID: b.chainConfig.ChainID.String()},
			Time:        time.Unix(int64(header.Time), 0),
		}
		statefull.PrepareStateSyncContext(next, b.chainConfig, header.Number, header.Time, header.Coinbase, stateReceiverAddr)
		gasUsed, err := gc.CommitState(event, next, header, cx, vm.Config{})
		require.NoError(t, err, "receiver %s", tc.receiver)
		gasByReceiver[tc.receiver] = gasUsed

		require.Equal(t, new(big.Int).SetUint64(id), lastStateIDAt(t, b.chainConfig, next, 32), "receiver %s", tc.receiver)
		want := common.Hash{}
		if tc.wantFailed {
			want = encodeReceiverData(t, tc.receiver, data)
		}
		require.Equal(t, want, failedStateSyncHash(next, id), "receiver %s", tc.receiver)
	}
	// The last two records both fail and store a hash after the first record
	// dirtied lastStateId, so the difference is the gas the burning receiver was
	// given: 160k with no gas oracle, not the pre-Uburu 5M.
	require.InDelta(t, 160_000, float64(gasByReceiver[burningReceiver])-float64(gasByReceiver[revertingReceiver]), 100)
}

// gasProbeReceiver stores the gas it was called with in slot 0.
var gasProbeReceiver = common.HexToAddress("0xa004")

// stateReceiverAtState reads lastStateId from the given state instead of
// through the eth API, which these test chains don't wire up.
type stateReceiverAtState struct {
	*contract.GenesisContractsClient
	cfg *params.ChainConfig
}

func (s stateReceiverAtState) LastStateId(st *state.StateDB, number uint64, _ common.Hash) (*big.Int, error) {
	return callLastStateID(s.cfg, st, number)
}

// TestFinalize_UburuSprintStartStateSync checks that on a sprint-start fork
// block the due record runs against the old StateReceiver and the code swap
// lands after it, so the new code first commits a record at the next sprint.
func TestFinalize_UburuSprintStartStateSync(t *testing.T) {
	t.Parallel()

	preCode := readCodeFixture(t, "statereceiver-mainnet-pre-uburu.hex")
	uburuCode := readCodeFixture(t, "statereceiver-mainnet-uburu-oracle0.hex")

	for name, finalize := range uburuFinalizers() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testUburuSprintStartStateSync(t, finalize, preCode, uburuCode)
		})
	}
}

func testUburuSprintStartStateSync(t *testing.T, finalize finalizeFunc, preCode, uburuCode string) {
	t.Helper()

	const fork, nextSprint = 32, 48
	chain, b, genesis, statedb := newUburuChain(t, fork, preCode, uburuCode, types.GenesisAlloc{
		gasProbeReceiver: {Balance: big.NewInt(0), Code: common.FromHex("0x5a60005500")},
	})
	b.GenesisContractsClient = stateReceiverAtState{
		GenesisContractsClient: contract.NewGenesisContractsClient(b.chainConfig, b.config.ValidatorContract, stateReceiverAddr.Hex(), nil),
		cfg:                    b.chainConfig,
	}

	firstID := stateReceiverSlots()[common.Hash{}].Big().Uint64() + 1
	record := func(id, dueAt uint64) *clerk.EventRecordWithTime {
		return &clerk.EventRecordWithTime{
			EventRecord: clerk.EventRecord{ID: id, Contract: gasProbeReceiver, Data: []byte{0x01}, ChainID: b.chainConfig.ChainID.String()},
			Time:        time.Unix(int64(uburuHeader(genesis, b, dueAt).Time)-1, 0),
		}
	}
	// Heimdall returns both records each time; the second is not due until nextSprint.
	b.SetHeimdallClient(&mockHeimdallClient{events: []*clerk.EventRecordWithTime{
		record(firstID, fork),
		record(firstID+1, nextSprint),
	}})

	// The old StateReceiver forwards 5M gas to the receiver, the Uburu one
	// 160k with no gas oracle.
	cases := []struct {
		number  uint64
		wantID  uint64
		wantGas uint64
	}{
		{fork, firstID, 5_000_000},
		{nextSprint, firstID + 1, 160_000},
	}
	for _, tc := range cases {
		require.NoError(t, finalize(b, chain, uburuHeader(genesis, b, tc.number), statedb), "block %d", tc.number)

		require.Equal(t, uburuStateReceiverHash, statedb.GetCodeHash(stateReceiverAddr), "block %d", tc.number)
		require.Equal(t, new(big.Int).SetUint64(tc.wantID), lastStateIDAt(t, b.chainConfig, statedb, tc.number), "block %d", tc.number)
		gasSeen := statedb.GetState(gasProbeReceiver, common.Hash{}).Big().Uint64()
		require.InDelta(t, tc.wantGas, gasSeen, 100, "block %d", tc.number)
	}
}
