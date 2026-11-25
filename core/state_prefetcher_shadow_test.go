//go:build lls_shadow

package core

import (
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/shadowtrace"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// loopCode is `JUMPDEST PUSH1 0 JUMP`: spins until gas runs out or the EVM
// interrupt fires.
var loopCode = []byte{0x5b, 0x60, 0x00, 0x56}

func newLoopFixture(t *testing.T) (*StatePrefetcher, *state.StateDB, *types.Header, *types.Transaction, types.Signer) {
	t.Helper()

	cfg := params.TestChainConfig
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	target := common.HexToAddress("0x1000")

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}

	statedb.SetBalance(from, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
	statedb.SetCode(target, loopCode, tracing.CodeChangeUnspecified)

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
	_, _ = gspec.Commit(db, triedb.NewDatabase(db, triedb.HashDefaults))

	hc, err := NewHeaderChain(db, cfg, ethash.NewFaker(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}

	return NewStatePrefetcher(cfg, hc), statedb, header, tx, signer
}

// TestPrefetchInterruptedRunCountsAsSuccess pins gap 8: an EVM run stopped by
// the interrupt returns ok=true (it would be recorded as prefetched), and the
// shadow outcome labels it "interrupted".
func TestPrefetchInterruptedRunCountsAsSuccess(t *testing.T) {
	p, statedb, header, tx, signer := newLoopFixture(t)

	interrupt := new(atomic.Bool)
	time.AfterFunc(30*time.Millisecond, func() { interrupt.Store(true) })

	var fails atomic.Int64

	_, ok, outcome := p.prefetchOneTxOutcome(tx, 0, header, statedb, statedb.Reader(), signer,
		vm.Config{}, false, interrupt, &fails)

	if !ok || outcome != shadowtrace.OutcomeInterrupted {
		t.Fatalf("got ok=%v outcome=%q, want ok=true outcome=interrupted (gap 8)", ok, outcome)
	}

	if fails.Load() != 0 {
		t.Fatalf("interrupted run counted as failure: %d", fails.Load())
	}
}

// TestPrefetchStreamGap8Toggle checks the stream path: without the toggle an
// interrupted run reaches onSuccess (marked prefetched); with gap8fix=on it
// does not.
func TestPrefetchStreamGap8Toggle(t *testing.T) {
	shadowtrace.SetEnabledForTest(true)
	t.Cleanup(func() { shadowtrace.SetEnabledForTest(false) })

	run := func(fix bool) int {
		dir := t.TempDir()
		path := filepath.Join(dir, "toggles")
		val := "off"
		if fix {
			val = "on"
		}
		if err := os.WriteFile(path, []byte("gap8fix="+val+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BOR_SHADOW_TOGGLES", path)
		time.Sleep(1100 * time.Millisecond) // toggles are re-read at most once per second

		p, statedb, header, tx, _ := newLoopFixture(t)

		evmAbort := new(atomic.Bool)
		txsCh := make(chan *types.Transaction, 1)
		txsCh <- tx
		close(txsCh)

		var (
			mu      sync.Mutex
			success int
		)

		time.AfterFunc(30*time.Millisecond, func() { evmAbort.Store(true) })

		p.PrefetchStream(header, statedb, vm.Config{}, false, new(atomic.Bool), evmAbort, txsCh,
			func(common.Hash, uint64) {
				mu.Lock()
				success++
				mu.Unlock()
			})

		return success
	}

	if got := run(false); got != 1 {
		t.Fatalf("gap8fix=off: onSuccess calls = %d, want 1 (interrupted run marked done)", got)
	}

	if got := run(true); got != 0 {
		t.Fatalf("gap8fix=on: onSuccess calls = %d, want 0", got)
	}
}
