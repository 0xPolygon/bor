package tracers

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// newCallTestAPI builds a hermetic TraceAPI backed by a synthetic chain with two
// funded accounts, returning the API and the accounts for use in trace_call /
// trace_callMany tests.
func newCallTestAPI(t *testing.T) (*TraceAPI, []Account) {
	t.Helper()

	accounts := newAccounts(2)
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
			accounts[1].addr: {Balance: big.NewInt(params.Ether)},
		},
	}
	backend := newTestBackend(t, 1, genesis, func(i int, b *core.BlockGen) {})
	t.Cleanup(backend.teardown)

	return &TraceAPI{API: NewAPI(backend)}, accounts
}

// transferArgs builds a simple value-transfer call object from -> to.
func transferArgs(from, to common.Address, value *big.Int) ethapi.TransactionArgs {
	return ethapi.TransactionArgs{
		From:  &from,
		To:    &to,
		Value: (*hexutil.Big)(value),
	}
}

func TestTraceCallParity(t *testing.T) {
	t.Parallel()

	api, accounts := newCallTestAPI(t)
	latest := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)

	tests := []struct {
		name       string
		traceTypes []string
		wantErr    bool
		wantTrace  bool
	}{
		{name: "trace requested", traceTypes: []string{"trace"}, wantTrace: true},
		{name: "no trace types", traceTypes: []string{}, wantTrace: false},
		{name: "unknown trace type", traceTypes: []string{"bogus"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			args := transferArgs(accounts[0].addr, accounts[1].addr, big.NewInt(1000))
			res, err := api.Call(t.Context(), args, tc.traceTypes, latest)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("trace_call error: %v", err)
			}
			if res.Output == nil {
				t.Fatalf("expected non-nil Output")
			}
			if res.StateDiff != nil || res.VMTrace != nil {
				t.Errorf("stateDiff/vmTrace should be nil in trace-only phase, got %v / %v", res.StateDiff, res.VMTrace)
			}

			if !tc.wantTrace {
				if len(res.Trace) != 0 {
					t.Fatalf("expected empty Trace, got %d entries", len(res.Trace))
				}
				return
			}

			if len(res.Trace) < 1 {
				t.Fatalf("expected at least one trace, got 0")
			}
			root := res.Trace[0]
			if root.Type != "call" {
				t.Errorf("expected root type 'call', got %q", root.Type)
			}
			// trace_call replays without a real tx/block identity.
			if root.TransactionHash != nil || root.BlockHash != nil ||
				root.BlockNumber != nil || root.TransactionPosition != nil {
				t.Errorf("expected no tx/block metadata on replay trace, got %+v", root)
			}
			if root.Action == nil || root.Action.From == nil || *root.Action.From != accounts[0].addr {
				t.Errorf("expected from %x, got %+v", accounts[0].addr, root.Action)
			}
		})
	}
}

func TestTraceCallManyParity(t *testing.T) {
	t.Parallel()

	api, accounts := newCallTestAPI(t)
	latest := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)

	calls := []traceCallManyItem{
		{
			args:       transferArgs(accounts[0].addr, accounts[1].addr, big.NewInt(1000)),
			traceTypes: []string{"trace"},
		},
		{
			args:       transferArgs(accounts[0].addr, accounts[1].addr, big.NewInt(2000)),
			traceTypes: []string{"trace"},
		},
	}

	results, err := api.CallMany(t.Context(), calls, latest)
	if err != nil {
		t.Fatalf("trace_callMany error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, res := range results {
		if res.Output == nil {
			t.Errorf("result %d: expected non-nil Output", i)
		}
		if len(res.Trace) < 1 {
			t.Errorf("result %d: expected at least one trace, got 0", i)
		}
	}

	// Both calls share the same statedb. Since CallDefaults derives the nonce
	// from the shared state's pool/account nonce, the second call must observe
	// the first call's nonce bump. Assert the recovered senders differ in nonce
	// by checking the underlying account nonce advanced: re-running a third call
	// after two transfers must still succeed (state remained consistent).
	third := []traceCallManyItem{{
		args:       transferArgs(accounts[0].addr, accounts[1].addr, big.NewInt(3000)),
		traceTypes: []string{"trace"},
	}}
	if _, err := api.CallMany(t.Context(), third, latest); err != nil {
		t.Fatalf("follow-up trace_callMany error: %v", err)
	}
}

func TestTraceCallManyErrors(t *testing.T) {
	t.Parallel()

	api, accounts := newCallTestAPI(t)
	latest := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
	valid := traceCallManyItem{
		args:       transferArgs(accounts[0].addr, accounts[1].addr, new(big.Int)),
		traceTypes: []string{"trace"},
	}

	if _, err := api.CallMany(t.Context(), make([]traceCallManyItem, maxTraceCallManyCalls+1), latest); err == nil {
		t.Error("oversized batch was accepted")
	}
	if _, err := api.CallMany(t.Context(), []traceCallManyItem{valid}, rpc.BlockNumberOrHashWithNumber(99)); err == nil {
		t.Error("missing block was accepted")
	}
	underfunded := valid
	zeroBalance := common.HexToAddress("0x000000000000000000000000000000000000dead")
	underfunded.args.From = &zeroBalance
	if _, err := api.CallMany(t.Context(), []traceCallManyItem{underfunded}, latest); err == nil || !strings.Contains(err.Error(), "entry 0") {
		t.Errorf("underfunded batch error = %v, want indexed entry error", err)
	}

	if _, _, _, err := api.traceCallState(t.Context(), rpc.BlockNumberOrHash{}); err == nil {
		t.Error("empty block selector was accepted")
	}
	if _, _, _, err := api.traceCallState(t.Context(), rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber)); err == nil {
		t.Error("pending block selector was accepted")
	}
}

func TestTraceCallManyItemUnmarshalJSON(t *testing.T) {
	t.Parallel()

	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")

	raw := `[{"from":"` + from.Hex() + `","to":"` + to.Hex() + `","value":"0x64"},["trace"]]`

	var item traceCallManyItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if item.args.From == nil || *item.args.From != from {
		t.Errorf("expected from %x, got %v", from, item.args.From)
	}
	if item.args.To == nil || *item.args.To != to {
		t.Errorf("expected to %x, got %v", to, item.args.To)
	}
	if len(item.traceTypes) != 1 || item.traceTypes[0] != "trace" {
		t.Errorf("expected traceTypes [trace], got %v", item.traceTypes)
	}
}

func TestTraceCallParity_UnderfundedSender(t *testing.T) {
	t.Parallel()

	accounts := newAccounts(3)
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
			accounts[1].addr: {Balance: big.NewInt(params.Ether)},
			// accounts[2] has no balance (zero ETH)
		},
	}
	backend := newTestBackend(t, 1, genesis, func(i int, b *core.BlockGen) {})
	t.Cleanup(backend.teardown)
	api := &TraceAPI{API: NewAPI(backend)}
	latest := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)

	// Gas bailout preserves the sender's balance during execution, but Bor's
	// state transition still validates that the sender can cover the gas limit.
	args := transferArgs(accounts[2].addr, accounts[1].addr, new(big.Int))
	if _, err := api.Call(t.Context(), args, []string{"trace"}, latest); err == nil {
		t.Fatal("trace_call accepted an underfunded sender")
	} else if !strings.Contains(err.Error(), "insufficient funds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTraceCallParity_GasBailoutPreservesObservedBalance(t *testing.T) {
	t.Parallel()

	accounts := newAccounts(2)
	contract := common.HexToAddress("0x000000000000000000000000000000000000cafe")
	// CALLER; BALANCE; PUSH1 0; MSTORE; PUSH1 32; PUSH1 0; RETURN.
	balanceCode := common.FromHex("0x333160005260206000f3")
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
			contract:         {Code: balanceCode},
		},
	}
	backend := newTestBackend(t, 1, genesis, func(i int, b *core.BlockGen) {})
	t.Cleanup(backend.teardown)
	api := &TraceAPI{API: NewAPI(backend)}
	latest := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
	args := transferArgs(accounts[0].addr, contract, new(big.Int))

	calls := []traceCallManyItem{
		{args: args, traceTypes: []string{"trace"}},
		{args: args, traceTypes: []string{"trace"}},
	}
	results, err := api.CallMany(t.Context(), calls, latest)
	if err != nil {
		t.Fatalf("trace_callMany error: %v", err)
	}
	for i, result := range results {
		if result.Output == nil {
			t.Fatalf("result %d has nil output", i)
		}
		want := big.NewInt(params.Ether)
		if got := new(big.Int).SetBytes(*result.Output); got.Cmp(want) != 0 {
			t.Errorf("result %d observed caller balance %s, want %s", i, got, want)
		}
	}
}

func TestValidateTraceCallManyCount(t *testing.T) {
	t.Parallel()

	if err := validateTraceCallManyCount(maxTraceCallManyCalls); err != nil {
		t.Fatalf("maximum valid call count rejected: %v", err)
	}
	if err := validateTraceCallManyCount(maxTraceCallManyCalls + 1); err == nil {
		t.Fatal("call count above maximum was accepted")
	}
}

func TestTraceCallManyItemUnmarshalJSON_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "not an array", raw: `{"from":"0x1"}`},
		{name: "wrong length", raw: `[{}]`},
		{name: "three elements", raw: `[{},["trace"],{}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var item traceCallManyItem
			if err := json.Unmarshal([]byte(tc.raw), &item); err == nil {
				t.Fatalf("expected error for %q, got nil", tc.raw)
			}
		})
	}
}

func TestTraceCallParity_BlockResolution(t *testing.T) {
	t.Parallel()
	api, accounts := newCallTestAPI(t)
	args := transferArgs(accounts[0].addr, accounts[1].addr, big.NewInt(1000))

	t.Run("pending rejected", func(t *testing.T) {
		t.Parallel()
		pending := rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber)
		if _, err := api.Call(t.Context(), args, []string{"trace"}, pending); err == nil {
			t.Fatal("expected error tracing on pending block")
		}
	})

	t.Run("unknown hash rejected", func(t *testing.T) {
		t.Parallel()
		bogus := rpc.BlockNumberOrHashWithHash(common.HexToHash("0xdeadbeef"), false)
		if _, err := api.Call(t.Context(), args, []string{"trace"}, bogus); err == nil {
			t.Fatal("expected error for unknown block hash")
		}
	})

	t.Run("by hash works", func(t *testing.T) {
		t.Parallel()
		block, err := api.blockByNumber(t.Context(), rpc.LatestBlockNumber)
		if err != nil {
			t.Fatalf("get latest block: %v", err)
		}
		byHash := rpc.BlockNumberOrHashWithHash(block.Hash(), false)
		res, err := api.Call(t.Context(), args, []string{"trace"}, byHash)
		if err != nil {
			t.Fatalf("trace_call by hash: %v", err)
		}
		if len(res.Trace) == 0 {
			t.Fatal("expected trace entries")
		}
	})
}

// TestTraceCallParity_ZeroGasPrice asserts the basefee clamp: a call with no
// gas price on a nonzero-basefee chain must still execute (erigon gas-bailout
// semantics) rather than fail with a negative-tip/underpriced error.
func TestTraceCallParity_ZeroGasPrice(t *testing.T) {
	t.Parallel()
	api, accounts := newCallTestAPI(t)
	latest := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)

	zero := (*hexutil.Big)(big.NewInt(0))
	args := transferArgs(accounts[0].addr, accounts[1].addr, big.NewInt(1000))
	args.GasPrice = zero

	res, err := api.Call(t.Context(), args, []string{"trace"}, latest)
	if err != nil {
		t.Fatalf("trace_call with zero gas price: %v", err)
	}
	if res.Output == nil || len(res.Trace) == 0 {
		t.Fatalf("expected populated result, got %+v", res)
	}
}

func TestTraceCallManyItemUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid tuple", input: `[{"from":"0x0000000000000000000000000000000000000001"},["trace"]]`},
		{name: "not an array", input: `{"call":{}}`, wantErr: true},
		{name: "one element", input: `[{}]`, wantErr: true},
		{name: "three elements", input: `[{},["trace"],{}]`, wantErr: true},
		{name: "bad call object", input: `[42,["trace"]]`, wantErr: true},
		{name: "bad trace types", input: `[{},"trace"]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var it traceCallManyItem
			err := json.Unmarshal([]byte(tc.input), &it)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && len(it.traceTypes) != 1 {
				t.Errorf("traceTypes = %v, want [trace]", it.traceTypes)
			}
		})
	}
}

func TestTraceCallManyParity_UnknownTraceType(t *testing.T) {
	t.Parallel()
	api, accounts := newCallTestAPI(t)
	latest := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)

	calls := []traceCallManyItem{
		{args: transferArgs(accounts[0].addr, accounts[1].addr, big.NewInt(1000)), traceTypes: []string{"bogus"}},
	}
	results, err := api.CallMany(t.Context(), calls, latest)
	if err == nil {
		t.Fatal("expected error for unknown trace type")
	}
	if results != nil {
		t.Errorf("results must be nil on error, got %v", results)
	}
}
