package tracers

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

func sdBig(n int64) *hexutil.Big       { return (*hexutil.Big)(big.NewInt(n)) }
func sdU64(n uint64) *uint64           { return &n }
func sdBytes(b ...byte) *hexutil.Bytes { x := hexutil.Bytes(b); return &x }

// accountJSON marshals one account diff and returns its JSON string for substring
// assertions.
func accountJSON(t *testing.T, diff parityStateDiff, addr common.Address) string {
	t.Helper()
	acc, ok := diff[addr]
	if !ok {
		t.Fatalf("address %s missing from stateDiff", addr.Hex())
	}
	b, err := json.Marshal(acc)
	if err != nil {
		t.Fatalf("marshal account diff: %v", err)
	}
	return string(b)
}

func TestBuildParityStateDiff(t *testing.T) {
	t.Parallel()

	modified := common.HexToAddress("0x1111111111111111111111111111111111111111")
	created := common.HexToAddress("0x2222222222222222222222222222222222222222")
	deleted := common.HexToAddress("0x3333333333333333333333333333333333333333")

	slotChanged := common.HexToHash("0x01")
	slotAdded := common.HexToHash("0x02")
	slotRemoved := common.HexToHash("0x03")

	pre := map[common.Address]*prestateAccount{
		modified: {
			Balance: sdBig(100),
			Nonce:   sdU64(1),
			Storage: map[common.Hash]common.Hash{
				slotChanged: common.HexToHash("0xaa"),
				slotRemoved: common.HexToHash("0xbb"),
			},
		},
		// created: empty pre-state
		created: {Balance: sdBig(0)},
		// deleted: present in pre, absent in post
		deleted: {Balance: sdBig(7), Nonce: sdU64(4)},
	}
	post := map[common.Address]*prestateAccount{
		modified: {
			Balance: sdBig(50), // changed
			// nonce absent -> unchanged
			Storage: map[common.Hash]common.Hash{
				slotChanged: common.HexToHash("0xcc"),
				slotAdded:   common.HexToHash("0xdd"),
			},
		},
		created: {Balance: sdBig(999), Nonce: sdU64(1), Code: sdBytes(0x60, 0x00)},
	}

	diff := buildParityStateDiff(pre, post)

	if len(diff) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(diff))
	}

	// modified: balance "*", nonce "=", storage slot changed/added/removed
	mj := accountJSON(t, diff, modified)
	for _, want := range []string{
		`"balance":{"*":`, `"nonce":"="`,
		// On an existing account all storage changes are "*" (new slot reads as
		// from 0x0; cleared slot reads as to 0x0).
		strings.ToLower(slotChanged.Hex()) + `":{"*":`,
		strings.ToLower(slotAdded.Hex()) + `":{"*":`,
		strings.ToLower(slotRemoved.Hex()) + `":{"*":`,
	} {
		if !strings.Contains(mj, want) {
			t.Errorf("modified diff missing %q in %s", want, mj)
		}
	}

	// created: all fields "+"
	cj := accountJSON(t, diff, created)
	for _, want := range []string{`"balance":{"+":`, `"nonce":{"+":`, `"code":{"+":`} {
		if !strings.Contains(cj, want) {
			t.Errorf("created diff missing %q in %s", want, cj)
		}
	}

	// deleted: all fields "-"
	dj := accountJSON(t, diff, deleted)
	for _, want := range []string{`"balance":{"-":`, `"nonce":{"-":`, `"code":{"-":`} {
		if !strings.Contains(dj, want) {
			t.Errorf("deleted diff missing %q in %s", want, dj)
		}
	}
}

// TestReplayTransactionParity_StateDiff exercises the stateDiff trace type end to
// end: a value transfer must show the sender's balance and nonce changing.
func TestReplayTransactionParity_StateDiff(t *testing.T) {
	t.Parallel()
	api, target, from := newTransferChainAPI(t, 3)

	res, err := api.ReplayTransaction(t.Context(), target, []string{"stateDiff"})
	if err != nil {
		t.Fatalf("trace_replayTransaction stateDiff error: %v", err)
	}
	if res.StateDiff == nil {
		t.Fatalf("expected non-nil stateDiff")
	}
	if res.Trace != nil {
		t.Errorf("trace must be nil when only stateDiff requested, got %v", res.Trace)
	}
	// trace_replayTransaction carries the replayed tx hash (erigon parity).
	if res.TransactionHash == nil || *res.TransactionHash != target {
		t.Errorf("expected transactionHash %x, got %v", target, res.TransactionHash)
	}

	b, err := json.Marshal(res.StateDiff)
	if err != nil {
		t.Fatalf("marshal stateDiff: %v", err)
	}
	var sd map[string]map[string]json.RawMessage
	if err := json.Unmarshal(b, &sd); err != nil {
		t.Fatalf("unmarshal stateDiff: %v", err)
	}

	acc, ok := sd[strings.ToLower(from.Hex())]
	if !ok {
		t.Fatalf("sender %s not present in stateDiff: %s", from.Hex(), string(b))
	}
	if string(acc["balance"]) == `"="` {
		t.Errorf("sender balance should have changed, got %s", string(b))
	}
	if string(acc["nonce"]) == `"="` {
		t.Errorf("sender nonce should have changed, got %s", string(b))
	}

	// Replay of a mined tx is NOT feeless: the sender's decrease must include
	// the gas debit on top of the 1000 wei transfer value.
	var balChange struct {
		Star struct {
			From *hexutil.Big `json:"from"`
			To   *hexutil.Big `json:"to"`
		} `json:"*"`
	}
	if err := json.Unmarshal(acc["balance"], &balChange); err != nil {
		t.Fatalf("unmarshal sender balance change: %v (%s)", err, acc["balance"])
	}
	decrease := new(big.Int).Sub(balChange.Star.From.ToInt(), balChange.Star.To.ToInt())
	if decrease.Cmp(big.NewInt(1000)) <= 0 {
		t.Errorf("sender decrease = %s, want > 1000 (transfer value + gas debit)", decrease)
	}
}

func TestStateDiffFieldDefaults(t *testing.T) {
	t.Parallel()

	// balVal/nonceVal/codeVal fall back to EVM-empty defaults for nil accounts
	// or unset fields.
	if got := balVal(nil); got.ToInt().Sign() != 0 {
		t.Errorf("balVal(nil) = %v, want 0", got)
	}
	if got := balVal(&prestateAccount{Balance: sdBig(7)}); got.ToInt().Int64() != 7 {
		t.Errorf("balVal(set) = %v, want 7", got)
	}
	if got := nonceVal(nil); got != 0 {
		t.Errorf("nonceVal(nil) = %d, want 0", got)
	}
	if got := nonceVal(&prestateAccount{Nonce: sdU64(9)}); got != 9 {
		t.Errorf("nonceVal(set) = %d, want 9", got)
	}
	if got := codeVal(nil); len(got) != 0 {
		t.Errorf("codeVal(nil) = %v, want empty", got)
	}
	if got := codeVal(&prestateAccount{Code: sdBytes(0xde, 0xad)}); len(got) != 2 {
		t.Errorf("codeVal(set) = %v, want 2 bytes", got)
	}
}

func TestIsRemovalDiff(t *testing.T) {
	t.Parallel()

	if !isRemovalDiff(sdRemoved(sdBig(1))) {
		t.Error(`isRemovalDiff("-") = false, want true`)
	}
	for name, v := range map[string]interface{}{
		"same":    sdSame(),
		"added":   sdAdded(sdBig(1)),
		"changed": sdChanged(sdBig(1), sdBig(2)),
		"string":  "=",
	} {
		if isRemovalDiff(v) {
			t.Errorf("isRemovalDiff(%s) = true, want false", name)
		}
	}
}

func TestIsEmptyAccount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		acc  *prestateAccount
		want bool
	}{
		{name: "nil account", acc: nil, want: true},
		{name: "zero fields", acc: &prestateAccount{}, want: true},
		{name: "explicit zeros", acc: &prestateAccount{Balance: sdBig(0), Nonce: sdU64(0), Code: sdBytes()}, want: true},
		{name: "nonzero balance", acc: &prestateAccount{Balance: sdBig(1)}, want: false},
		{name: "nonzero nonce", acc: &prestateAccount{Nonce: sdU64(1)}, want: false},
		{name: "nonempty code", acc: &prestateAccount{Code: sdBytes(0x60)}, want: false},
		{name: "nonempty storage", acc: &prestateAccount{Storage: map[common.Hash]common.Hash{{1}: {2}}}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isEmptyAccount(tc.acc); got != tc.want {
				t.Errorf("isEmptyAccount = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiffAccountHelpers(t *testing.T) {
	t.Parallel()

	t.Run("deleted has no storage entries", func(t *testing.T) {
		t.Parallel()
		acc := diffDeletedAccount(&prestateAccount{
			Balance: sdBig(5), Nonce: sdU64(1), Code: sdBytes(0x60),
			Storage: map[common.Hash]common.Hash{{1}: {2}},
		})
		if !isRemovalDiff(acc.Balance) || !isRemovalDiff(acc.Nonce) || !isRemovalDiff(acc.Code) {
			t.Errorf("deleted account fields must all be removals: %+v", acc)
		}
		// erigon emits NO storage "-" entries for deleted accounts.
		if len(acc.Storage) != 0 {
			t.Errorf("deleted account storage = %v, want empty", acc.Storage)
		}
	})

	t.Run("created adds all fields and storage", func(t *testing.T) {
		t.Parallel()
		acc := diffCreatedAccount(&prestateAccount{
			Balance: sdBig(5), Nonce: sdU64(1), Code: sdBytes(0x60),
			Storage: map[common.Hash]common.Hash{{1}: {2}},
		})
		b, _ := json.Marshal(acc)
		s := string(b)
		for _, want := range []string{`"balance":{"+"`, `"nonce":{"+"`, `"code":{"+"`} {
			if !strings.Contains(s, want) {
				t.Errorf("created account missing %s in %s", want, s)
			}
		}
		if len(acc.Storage) != 1 {
			t.Errorf("created account storage entries = %d, want 1", len(acc.Storage))
		}
	})

	t.Run("modified keeps unset fields same", func(t *testing.T) {
		t.Parallel()
		pre := &prestateAccount{Balance: sdBig(10), Nonce: sdU64(1)}
		post := &prestateAccount{Balance: sdBig(20)} // only balance changed
		acc := diffModifiedAccount(pre, post)
		b, _ := json.Marshal(acc)
		s := string(b)
		if !strings.Contains(s, `"balance":{"*"`) {
			t.Errorf("balance should be changed: %s", s)
		}
		if !strings.Contains(s, `"nonce":"="`) || !strings.Contains(s, `"code":"="`) {
			t.Errorf("nonce/code should be unchanged: %s", s)
		}
	})
}

func TestDiffModifiedStorage(t *testing.T) {
	t.Parallel()

	slotA, slotB, slotC := common.Hash{0xa}, common.Hash{0xb}, common.Hash{0xc}
	valOld, valNew := common.Hash{1}, common.Hash{2}

	got := diffModifiedStorage(
		map[common.Hash]common.Hash{slotA: valOld, slotC: valOld},
		map[common.Hash]common.Hash{slotA: valNew, slotB: valNew},
	)
	if len(got) != 3 {
		t.Fatalf("storage entries = %d, want 3", len(got))
	}
	assertChange := func(slot common.Hash, from, to common.Hash) {
		t.Helper()
		b, _ := json.Marshal(got[slot])
		s := string(b)
		if !strings.Contains(s, from.Hex()) || !strings.Contains(s, to.Hex()) {
			t.Errorf("slot %x diff = %s, want %s -> %s", slot, s, from.Hex(), to.Hex())
		}
	}
	var zero common.Hash
	assertChange(slotA, valOld, valNew) // changed slot: old -> new
	assertChange(slotB, zero, valNew)   // fresh write: 0 -> new
	assertChange(slotC, valOld, zero)   // cleared slot: old -> 0
}

func TestSenderAndBalanceOnlyDiffs(t *testing.T) {
	t.Parallel()

	addr := common.HexToAddress("0x00000000000000000000000000000000000000aa")

	t.Run("addBalanceOnlyDiff skips unchanged", func(t *testing.T) {
		t.Parallel()
		sd := parityStateDiff{}
		addBalanceOnlyDiff(sd, addr, big.NewInt(5), big.NewInt(5))
		if len(sd) != 0 {
			t.Errorf("unchanged balance added an entry: %v", sd)
		}
	})

	t.Run("addBalanceOnlyDiff adds change", func(t *testing.T) {
		t.Parallel()
		sd := parityStateDiff{}
		addBalanceOnlyDiff(sd, addr, big.NewInt(5), big.NewInt(9))
		acc, ok := sd[addr]
		if !ok {
			t.Fatal("balance change not added")
		}
		b, _ := json.Marshal(acc)
		if !strings.Contains(string(b), `"balance":{"*"`) {
			t.Errorf("expected balance change, got %s", b)
		}
	})

	t.Run("addBalanceOnlyDiff keeps existing entry", func(t *testing.T) {
		t.Parallel()
		existing := &parityAccountDiff{Balance: sdSame()}
		sd := parityStateDiff{addr: existing}
		addBalanceOnlyDiff(sd, addr, big.NewInt(5), big.NewInt(9))
		if sd[addr] != existing {
			t.Error("existing entry was replaced")
		}
	})
}
