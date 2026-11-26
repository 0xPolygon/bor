package core

import (
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

// prefetchLoopCode is `JUMPDEST PUSH1 0 JUMP`: it spins until gas runs out
// or the EVM interrupt fires.
var prefetchLoopCode = []byte{0x5b, 0x60, 0x00, 0x56}

func newPrefetchLoopFixture(t *testing.T) (*StatePrefetcher, *state.StateDB, *types.Header, *types.Transaction, types.Signer) {
	t.Helper()

	cfg := params.TestChainConfig
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	target := common.HexToAddress("0x1000")

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb.SetBalance(from, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
	statedb.SetCode(target, prefetchLoopCode, tracing.CodeChangeUnspecified)

	header := &types.Header{
		Number: big.NewInt(1), Time: 1, GasLimit: 1_000_000_000,
		BaseFee: big.NewInt(1), Difficulty: big.NewInt(0),
	}
	signer := types.MakeSigner(cfg, header.Number, header.Time)
	tx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID: cfg.ChainID, Nonce: 0, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(10),
		Gas: 900_000_000, To: &target,
	})

	db := rawdb.NewMemoryDatabase()
	gspec := &Genesis{BaseFee: big.NewInt(params.InitialBaseFee), Config: cfg}
	if _, err := gspec.Commit(db, triedb.NewDatabase(db, triedb.HashDefaults)); err != nil {
		t.Fatal(err)
	}
	hc, err := NewHeaderChain(db, cfg, ethash.NewFaker(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}

	return NewStatePrefetcher(cfg, hc), statedb, header, tx, signer
}

// An EVM run stopped by the interrupt has warmed only part of the tx's state,
// so it must count as a failure, not as prefetched.
func TestPrefetchOneTxInterruptedRunIsFailure(t *testing.T) {
	t.Parallel()

	p, statedb, header, tx, signer := newPrefetchLoopFixture(t)

	interrupt := new(atomic.Bool)
	time.AfterFunc(30*time.Millisecond, func() { interrupt.Store(true) })

	var fails atomic.Int64
	gas, ok := p.prefetchOneTx(tx, 0, header, statedb, statedb.Reader(), signer, vm.Config{}, false, interrupt, &fails)

	if ok || gas != 0 {
		t.Fatalf("interrupted run: got ok=%v gas=%d, want ok=false gas=0", ok, gas)
	}
	if fails.Load() != 1 {
		t.Fatalf("interrupted run: fails=%d, want 1", fails.Load())
	}
}

// The stream path must not report an interrupted run to onSuccess (which
// would mark the tx prefetched and skip it in later rounds).
func TestPrefetchStreamInterruptedRunNotMarkedDone(t *testing.T) {
	t.Parallel()

	p, statedb, header, tx, _ := newPrefetchLoopFixture(t)

	txsCh := make(chan *types.Transaction, 1)
	txsCh <- tx
	close(txsCh)

	evmAbort := new(atomic.Bool)
	time.AfterFunc(30*time.Millisecond, func() { evmAbort.Store(true) })

	var success atomic.Int64
	p.PrefetchStream(header, statedb, vm.Config{}, false, new(atomic.Bool), evmAbort, txsCh,
		func(common.Hash, uint64) { success.Add(1) })

	if got := success.Load(); got != 0 {
		t.Fatalf("onSuccess calls = %d, want 0 for an interrupted run", got)
	}
}
