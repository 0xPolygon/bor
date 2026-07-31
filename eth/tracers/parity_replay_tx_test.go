package tracers

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

func TestReplayTransactionParity(t *testing.T) {
	t.Parallel()
	api, target, from := newTransferChainAPI(t, 3)

	res, err := api.ReplayTransaction(t.Context(), target, []string{"trace"})
	if err != nil {
		t.Fatalf("trace_replayTransaction error: %v", err)
	}
	if res.Output == nil {
		t.Fatalf("expected non-nil output")
	}
	if res.StateDiff != nil || res.VMTrace != nil {
		t.Errorf("stateDiff/vmTrace must be nil in trace-only phase, got %+v / %+v", res.StateDiff, res.VMTrace)
	}
	if len(res.Trace) == 0 {
		t.Fatalf("expected at least one trace entry")
	}
	root := res.Trace[0]
	if root.Type != "call" {
		t.Errorf("expected root type 'call', got %q", root.Type)
	}
	// Replay semantics: no per-tx/block identifiers on entries.
	if root.TransactionHash != nil || root.BlockHash != nil || root.TransactionPosition != nil || root.BlockNumber != nil {
		t.Errorf("replay traces must omit tx/block metadata, got %+v", root)
	}
	if root.Action == nil || root.Action.From == nil || *root.Action.From != from {
		t.Errorf("expected from %x, got %+v", from, root.Action)
	}
}

func TestReplayTransactionParity_NoTraceType(t *testing.T) {
	t.Parallel()
	api, target, _ := newTransferChainAPI(t, 3)

	res, err := api.ReplayTransaction(t.Context(), target, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output == nil {
		t.Errorf("expected output even with no trace types")
	}
	if len(res.Trace) != 0 {
		t.Errorf("expected no trace entries when 'trace' not requested, got %d", len(res.Trace))
	}
}

func TestReplayTransactionParity_Errors(t *testing.T) {
	t.Parallel()
	api, target, _ := newTransferChainAPI(t, 3)

	if _, err := api.ReplayTransaction(t.Context(), target, []string{"bogus"}); err == nil {
		t.Errorf("expected error for unknown trace type")
	}
	if _, err := api.ReplayTransaction(t.Context(), crypto.Keccak256Hash([]byte("missing")), []string{"trace"}); err == nil {
		t.Errorf("expected error for unknown transaction")
	}
}

type transactionLookupTestBackend struct {
	*testBackend
	found        bool
	blockNumber  uint64
	txIndexReady bool
}

func (b *transactionLookupTestBackend) GetCanonicalTransaction(common.Hash) (bool, *types.Transaction, common.Hash, uint64, uint64) {
	return b.found, nil, common.Hash{}, b.blockNumber, 0
}

func (b *transactionLookupTestBackend) TxIndexDone() bool {
	return b.txIndexReady
}

func TestTransactionParityLookupErrors(t *testing.T) {
	t.Parallel()

	accounts := newAccounts(1)
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc:  types.GenesisAlloc{accounts[0].addr: {Balance: big.NewInt(params.Ether)}},
	}
	base := newTestBackend(t, 1, genesis, func(int, *core.BlockGen) {})
	t.Cleanup(base.teardown)
	hash := common.HexToHash("0x1234")

	t.Run("transaction index still building", func(t *testing.T) {
		backend := &transactionLookupTestBackend{testBackend: base, txIndexReady: false}
		api := &TraceAPI{API: NewAPI(backend)}
		if _, err := api.ReplayTransaction(t.Context(), hash, []string{"trace"}); err == nil || !strings.Contains(err.Error(), "indexing") {
			t.Errorf("replay error = %v, want indexing error", err)
		}
		if _, err := api.Transaction(t.Context(), hash); err == nil || !strings.Contains(err.Error(), "indexing") {
			t.Errorf("transaction error = %v, want indexing error", err)
		}
	})

	t.Run("genesis transaction", func(t *testing.T) {
		backend := &transactionLookupTestBackend{testBackend: base, found: true, txIndexReady: true}
		api := &TraceAPI{API: NewAPI(backend)}
		if _, err := api.ReplayTransaction(t.Context(), hash, []string{"trace"}); err == nil || !strings.Contains(err.Error(), "genesis") {
			t.Errorf("replay error = %v, want genesis error", err)
		}
		if _, err := api.Transaction(t.Context(), hash); err == nil || !strings.Contains(err.Error(), "genesis") {
			t.Errorf("transaction error = %v, want genesis error", err)
		}
	})
}

// TestReplayTransactionParity_PrunedNode asserts that trace_replayTransaction
// on a pruned (non-archive) node returns a deterministic error rather than
// timing out or returning partial data.
func TestReplayTransactionParity_PrunedNode(t *testing.T) {
	t.Parallel()

	accounts := newAccounts(2)
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
			accounts[1].addr: {Balance: big.NewInt(params.Ether)},
		},
	}
	genBlocks := 2
	signer := types.HomesteadSigner{}
	var target common.Hash
	base := newTestBackend(t, genBlocks, genesis, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce: uint64(i), To: &accounts[1].addr, Value: big.NewInt(1000),
			Gas: params.TxGas, GasPrice: b.BaseFee(),
		}), signer, accounts[0].key)
		b.AddTx(tx)
		if i == genBlocks-1 {
			target = tx.Hash()
		}
	})
	t.Cleanup(base.teardown)

	// prunedTestBackend is defined in api_test.go; it overrides StateAtBlock /
	// StateAtTransaction to always return errStateNotFound, simulating a node
	// that has pruned historical trie data.
	api := &TraceAPI{API: NewAPI(&prunedTestBackend{base})}

	_, err := api.ReplayTransaction(t.Context(), target, []string{"trace"})
	if err == nil {
		t.Fatal("expected error from pruned node, got nil")
	}
	// The error must be deterministic, not a context timeout.
	if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("pruned node caused a timeout instead of a deterministic error: %v", err)
	}
}
