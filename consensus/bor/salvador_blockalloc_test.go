package bor

import (
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

var (
	sweepSelector    = common.FromHex("0x35faa416")
	recoverySelector = common.FromHex("0x78ed5d81")
)

type salvadorAccount struct {
	key     *ecdsa.PrivateKey
	addr    common.Address
	code    string
	balance *big.Int
	nonce   uint64
}

// salvadorPresetCode returns the Sweeper runtime the mainnet preset installs at addr.
func salvadorPresetCode(t *testing.T, addr string) string {
	t.Helper()
	alloc, ok := params.BorMainnetChainConfig.Bor.BlockAlloc["0"].(map[string]interface{})
	require.True(t, ok)
	account, ok := alloc[addr].(map[string]interface{})
	require.True(t, ok, "no Salvador entry for %s", addr)
	code, ok := account["code"].(string)
	require.True(t, ok)
	return code
}

// salvadorAccounts stands in for the blocked addresses with fresh keys so the
// tests can sign as them, carrying the preset's two Sweeper builds.
func salvadorAccounts(t *testing.T) []*salvadorAccount {
	t.Helper()
	first := salvadorPresetCode(t, "e4C4C39C1733e1C7c06B79f97ED442B68Df41EC6")
	second := salvadorPresetCode(t, "5227f603C1eE34CE3C06eFe6477003BDfCCB2f8c")
	balances := []*big.Int{big.NewInt(5e18), big.NewInt(7e18), big.NewInt(0)}
	codes := []string{first, second, first}

	accounts := make([]*salvadorAccount, len(balances))
	for i := range balances {
		key, err := crypto.GenerateKey()
		require.NoError(t, err)
		accounts[i] = &salvadorAccount{key: key, addr: crypto.PubkeyToAddress(key.PublicKey), code: codes[i], balance: balances[i], nonce: 3}
	}
	return accounts
}

func newSalvadorChain(t *testing.T, fork uint64, accounts []*salvadorAccount) (*core.BlockChain, *Bor, *types.Header, *state.StateDB) {
	t.Helper()

	addr1 := common.HexToAddress("0x1")
	borCfg := borConfigWithDelays(16)
	borCfg.RioBlock = big.NewInt(0)
	borCfg.IndoreBlock = big.NewInt(0)
	borCfg.StateSyncConfirmationDelay = map[string]uint64{"0": 0}
	borCfg.SalvadorBlock = new(big.Int).SetUint64(fork)
	borCfg.BurntContract = map[string]string{"0": "0x000000000000000000000000000000000000dead"}

	alloc := make(map[string]interface{}, len(accounts))
	for _, a := range accounts {
		alloc[a.addr.Hex()] = map[string]interface{}{"balance": "0x0", "code": a.code}
	}
	borCfg.BlockAlloc = map[string]interface{}{borCfg.SalvadorBlock.String(): alloc}

	cfg := newAllForksChainConfig(borCfg)
	cfg.ShanghaiBlock = big.NewInt(0)
	cfg.CancunBlock = big.NewInt(0)
	cfg.PragueBlock = big.NewInt(0)
	cfg.BlobScheduleConfig = params.MergedTestChainConfig.BlobScheduleConfig

	withAccounts := func(g *core.Genesis) {
		g.Alloc = types.GenesisAlloc{}
		for _, a := range accounts {
			g.Alloc[a.addr] = types.Account{Balance: a.balance, Nonce: a.nonce}
		}
	}
	sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
	chain, b := newChainAndBorForTestWithConfig(t, sp, cfg, true, addr1, uint64(time.Now().Unix())-200, withAccounts)
	t.Cleanup(chain.Stop)

	genesis := chain.HeaderChain().GetHeaderByNumber(0)
	require.NotNil(t, genesis)
	statedb, err := chain.StateAt(genesis.Root)
	require.NoError(t, err)
	return chain, b, genesis, statedb
}

// TestFinalize_SalvadorInstallsSweeper checks the code install at N-1 / N / N+1
// on every finalize path, on and off a sprint start, with balances and nonces
// left as they were.
func TestFinalize_SalvadorInstallsSweeper(t *testing.T) {
	t.Parallel()

	// sprint is 16: 20 is mid-sprint, 32 is a sprint start.
	for name, finalize := range uburuFinalizers() {
		for _, fork := range []uint64{20, 32} {
			t.Run(name+"/"+new(big.Int).SetUint64(fork).String(), func(t *testing.T) {
				t.Parallel()
				testSalvadorInstall(t, finalize, fork)
			})
		}
	}
}

func testSalvadorInstall(t *testing.T, finalize finalizeFunc, fork uint64) {
	t.Helper()

	accounts := salvadorAccounts(t)
	chain, b, genesis, statedb := newSalvadorChain(t, fork, accounts)

	for _, number := range []uint64{fork - 1, fork, fork + 1} {
		header := uburuHeader(genesis, b, number)
		require.NoError(t, finalize(b, chain, header, statedb), "block %d", number)
		installed := number >= fork
		require.Equal(t, installed, b.config.IsSalvador(header.Number), "block %d", number)

		for _, a := range accounts {
			want := []byte(nil)
			if installed {
				want = common.FromHex(a.code)
			}
			require.Equal(t, want, statedb.GetCode(a.addr), "block %d %s", number, a.addr)
			require.Zero(t, a.balance.Cmp(statedb.GetBalance(a.addr).ToBig()), "block %d %s", number, a.addr)
			require.Equal(t, a.nonce, statedb.GetNonce(a.addr), "block %d %s", number, a.addr)
		}
	}
}

func salvadorEVM(cfg *params.ChainConfig, statedb *state.StateDB, number uint64) *vm.EVM {
	blockCtx := vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		BlockNumber: new(big.Int).SetUint64(number),
		Time:        number * 2,
		Difficulty:  big.NewInt(1),
		BaseFee:     big.NewInt(0),
		GasLimit:    30_000_000,
	}
	return vm.NewEVM(blockCtx, statedb, cfg, vm.Config{})
}

func applySalvadorMsg(cfg *params.ChainConfig, statedb *state.StateDB, number uint64, msg *core.Message) (*core.ExecutionResult, error) {
	msg.Value = new(big.Int)
	msg.GasPrice, msg.GasFeeCap, msg.GasTipCap = new(big.Int), new(big.Int), new(big.Int)
	return core.ApplyMessage(salvadorEVM(cfg, statedb, number), msg, new(core.GasPool).AddGas(30_000_000))
}

// installSalvador finalizes the fork block and returns the state after it.
func installSalvador(t *testing.T, fork uint64, accounts []*salvadorAccount) (*Bor, *state.StateDB) {
	t.Helper()
	chain, b, genesis, statedb := newSalvadorChain(t, fork, accounts)
	_, err := b.Finalize(chain.HeaderChain(), uburuHeader(genesis, b, fork), statedb, &types.Body{}, nil)
	require.NoError(t, err)
	return b, statedb
}

// TestSalvadorBlocksKeyHolder checks that after the install neither a
// transaction nor a 7702 authorization signed by the address's own key can act.
func TestSalvadorBlocksKeyHolder(t *testing.T) {
	t.Parallel()

	const fork = 20
	accounts := salvadorAccounts(t)
	b, statedb := installSalvador(t, fork, accounts)
	cfg := b.chainConfig
	target := accounts[0]
	sink := common.HexToAddress("0xbeef")

	_, err := applySalvadorMsg(cfg, statedb, fork+1, &core.Message{From: target.addr, To: &sink, Nonce: target.nonce, GasLimit: 21_000})
	require.ErrorIs(t, err, core.ErrSenderNoEOA)

	auth, err := types.SignSetCode(target.key, types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(cfg.ChainID),
		Address: sink,
		Nonce:   target.nonce,
	})
	require.NoError(t, err)
	carrierKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	carrier := crypto.PubkeyToAddress(carrierKey.PublicKey)
	res, err := applySalvadorMsg(cfg, statedb, fork+1, &core.Message{
		From: carrier, To: &carrier, GasLimit: 100_000,
		SetCodeAuthorizations: []types.SetCodeAuthorization{auth},
	})
	require.NoError(t, err)
	require.False(t, res.Failed())
	require.Equal(t, common.FromHex(target.code), statedb.GetCode(target.addr))
	require.Equal(t, target.nonce, statedb.GetNonce(target.addr))
}

// TestSalvadorSweep checks that any caller can move the full balance to the
// recovery address built into the code, and only there.
func TestSalvadorSweep(t *testing.T) {
	t.Parallel()

	const fork = 20
	accounts := salvadorAccounts(t)
	b, statedb := installSalvador(t, fork, accounts)
	cfg := b.chainConfig
	callerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	caller := crypto.PubkeyToAddress(callerKey.PublicKey)

	recoveries := make(map[common.Address]bool)
	for _, a := range accounts {
		ret, _, err := salvadorEVM(cfg, statedb, fork+1).StaticCall(caller, a.addr, recoverySelector, 100_000)
		require.NoError(t, err)
		recovery := common.BytesToAddress(ret)
		require.NotEqual(t, common.Address{}, recovery)
		recoveries[recovery] = true

		before := statedb.GetBalance(recovery).ToBig()
		for range 2 {
			res, err := applySalvadorMsg(cfg, statedb, fork+1, &core.Message{From: caller, To: &a.addr, Nonce: statedb.GetNonce(caller), GasLimit: 100_000, Data: sweepSelector})
			require.NoError(t, err)
			require.False(t, res.Failed(), "sweep %s: %v", a.addr, res.Err)
		}
		require.Zero(t, statedb.GetBalance(a.addr).Sign(), "%s", a.addr)
		require.Zero(t, new(big.Int).Add(before, a.balance).Cmp(statedb.GetBalance(recovery).ToBig()), "%s", a.addr)
	}
	require.Len(t, recoveries, 2)
}
