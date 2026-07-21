// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.

package tracers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// parityBurntContract returns the Bor base-fee recipient (the "burnt contract")
// active at the given block, or the zero address if none is configured. Bor
// credits the base fee to this address in the state transition, but the native
// prestate tracer doesn't look it up, so the Parity stateDiff must add it.
func (api *API) parityBurntContract(blockNumber uint64) common.Address {
	cfg := api.backend.ChainConfig()
	if cfg.Bor == nil {
		return common.Address{}
	}
	return common.HexToAddress(cfg.Bor.CalculateBurntContract(blockNumber))
}

// parityStateDiff is the Parity/OpenEthereum "stateDiff" object: a map from
// touched account address to the per-field diff for that account.
type parityStateDiff map[common.Address]*parityAccountDiff

// parityAccountDiff holds the per-field diff for a single account. Each field is
// one of: the string "=" (unchanged), {"+": v} (created), {"-": v} (deleted) or
// {"*": {"from": x, "to": y}} (changed).
type parityAccountDiff struct {
	Balance interface{}                 `json:"balance"`
	Code    interface{}                 `json:"code"`
	Nonce   interface{}                 `json:"nonce"`
	Storage map[common.Hash]interface{} `json:"storage"`
}

// prestateAccount mirrors the JSON shape emitted by the native prestateTracer
// (see eth/tracers/native/prestate.go). Pointer fields let us distinguish an
// absent field (unchanged, in the diffMode "post" map) from a present one.
type prestateAccount struct {
	Balance *hexutil.Big                `json:"balance"`
	Nonce   *uint64                     `json:"nonce"`
	Code    *hexutil.Bytes              `json:"code"`
	Storage map[common.Hash]common.Hash `json:"storage"`
}

// prestateDiffResult mirrors the prestateTracer diffMode result {pre, post}.
type prestateDiffResult struct {
	Pre  map[common.Address]*prestateAccount `json:"pre"`
	Post map[common.Address]*prestateAccount `json:"post"`
}

func sdSame() interface{}                 { return "=" }
func sdAdded(v interface{}) interface{}   { return map[string]interface{}{"+": v} }
func sdRemoved(v interface{}) interface{} { return map[string]interface{}{"-": v} }
func sdChanged(from, to interface{}) interface{} {
	return map[string]interface{}{"*": map[string]interface{}{"from": from, "to": to}}
}

// isRemovalDiff reports whether a per-field diff value is a removal ("-"),
// i.e. the account was deleted. Used to identify deletion entries.
func isRemovalDiff(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	if !ok {
		return false
	}
	_, ok = m["-"]
	return ok
}

// balVal/nonceVal/codeVal return a JSON-encodable value for the field, with
// EVM-empty defaults (0 balance, 0 nonce, empty code).
func balVal(a *prestateAccount) *hexutil.Big {
	if a != nil && a.Balance != nil {
		return a.Balance
	}
	return (*hexutil.Big)(big.NewInt(0))
}

func nonceVal(a *prestateAccount) hexutil.Uint64 {
	if a != nil && a.Nonce != nil {
		return hexutil.Uint64(*a.Nonce)
	}
	return 0
}

func codeVal(a *prestateAccount) hexutil.Bytes {
	if a != nil && a.Code != nil {
		return *a.Code
	}
	return hexutil.Bytes{}
}

// isEmptyAccount reports whether the pre-state account is EVM-empty (no balance,
// nonce or code). Per EIP-161 an empty account is indistinguishable from a
// non-existent one, so such an account appearing with post-state values is
// treated as newly created.
func isEmptyAccount(a *prestateAccount) bool {
	if a == nil {
		return true
	}
	if a.Balance != nil && a.Balance.ToInt().Sign() != 0 {
		return false
	}
	if a.Nonce != nil && *a.Nonce != 0 {
		return false
	}
	if a.Code != nil && len(*a.Code) != 0 {
		return false
	}
	return len(a.Storage) == 0
}

// diffDeletedAccount encodes an account present in pre only (deleted by the
// transaction). erigon's CompareStates emits only balance/code/nonce "-" for a
// deleted account and NO storage entries (storage "-" is never produced), so
// Storage stays empty.
func diffDeletedAccount(preAcc *prestateAccount) *parityAccountDiff {
	return &parityAccountDiff{
		Balance: sdRemoved(balVal(preAcc)),
		Nonce:   sdRemoved(nonceVal(preAcc)),
		Code:    sdRemoved(codeVal(preAcc)),
		Storage: map[common.Hash]interface{}{},
	}
}

// diffCreatedAccount encodes an account with an EVM-empty pre-state and post
// values (created by the transaction): every field and storage slot is "+".
func diffCreatedAccount(postAcc *prestateAccount) *parityAccountDiff {
	acc := &parityAccountDiff{
		Balance: sdAdded(balVal(postAcc)),
		Nonce:   sdAdded(nonceVal(postAcc)),
		Code:    sdAdded(codeVal(postAcc)),
		Storage: map[common.Hash]interface{}{},
	}
	for slot, val := range postAcc.Storage {
		acc.Storage[slot] = sdAdded(val)
	}
	return acc
}

// diffModifiedAccount encodes an account present before and after the
// transaction as per-field changes. The diffMode post map carries only the
// changed fields; an absent field means unchanged ("=").
func diffModifiedAccount(preAcc, postAcc *prestateAccount) *parityAccountDiff {
	acc := &parityAccountDiff{
		Balance: sdSame(),
		Nonce:   sdSame(),
		Code:    sdSame(),
		Storage: diffModifiedStorage(preAcc.Storage, postAcc.Storage),
	}
	if postAcc.Balance != nil {
		acc.Balance = sdChanged(balVal(preAcc), postAcc.Balance)
	}
	if postAcc.Nonce != nil {
		acc.Nonce = sdChanged(nonceVal(preAcc), hexutil.Uint64(*postAcc.Nonce))
	}
	if postAcc.Code != nil {
		acc.Code = sdChanged(codeVal(preAcc), *postAcc.Code)
	}
	return acc
}

// diffModifiedStorage encodes storage changes of an existing account. Every
// change is "*": a freshly written slot reads as 0 -> val and a cleared slot as
// val -> 0 (Parity only uses "+"/"-" on created/deleted accounts).
func diffModifiedStorage(pre, post map[common.Hash]common.Hash) map[common.Hash]interface{} {
	storage := map[common.Hash]interface{}{}

	var zero common.Hash
	for slot, newVal := range post {
		oldVal, ok := pre[slot]
		if !ok {
			oldVal = zero
		}
		storage[slot] = sdChanged(oldVal, newVal)
	}
	for slot, oldVal := range pre {
		if _, ok := post[slot]; !ok {
			storage[slot] = sdChanged(oldVal, zero)
		}
	}
	return storage
}

// buildParityStateDiff converts the prestateTracer diffMode {pre, post} maps into
// the Parity stateDiff encoding.
//
// After diffMode processing the prestateTracer leaves: modified accounts in both
// pre and post (post carrying only the changed fields), deleted accounts in pre
// only, and unmodified accounts in neither. Created accounts appear in both with
// an EVM-empty pre-state.
func buildParityStateDiff(pre, post map[common.Address]*prestateAccount) parityStateDiff {
	diff := make(parityStateDiff)

	hint := len(pre)
	if len(post) > hint {
		hint = len(post)
	}
	addrs := make(map[common.Address]struct{}, hint)
	for a := range pre {
		addrs[a] = struct{}{}
	}
	for a := range post {
		addrs[a] = struct{}{}
	}

	for addr := range addrs {
		preAcc, postAcc := pre[addr], post[addr]

		switch {
		case postAcc == nil:
			diff[addr] = diffDeletedAccount(preAcc)
		case isEmptyAccount(preAcc):
			diff[addr] = diffCreatedAccount(postAcc)
		default:
			diff[addr] = diffModifiedAccount(preAcc, postAcc)
		}
	}

	return diff
}

// parityStateDiffFor executes the message with the native prestateTracer in
// diffMode and converts the result into the Parity stateDiff encoding.
//
// preState MUST be a pre-execution copy of the state (e.g. statedb.Copy()): the
// tracer re-executes the message and advances the state it is given, so callers
// pass a throwaway copy rather than the canonical state used for trace output.
func (api *API) parityStateDiffFor(ctx context.Context, in parityExecInput) (parityStateDiff, error) {
	cfg := parityPhaseConfig("prestateTracer", json.RawMessage(`{"diffMode":true}`), in.config)

	// Snapshot the true pre-execution state. erigon's CompareStates omits any
	// account that did not exist before the tx and does not exist after it
	// (created-and-destroyed transients, e.g. CREATE2 gas tokens minted+freed in
	// one tx). traceTx mutates preState, so capture existence beforehand.
	initial := in.statedb.Copy()

	// The native prestate tracer doesn't track the Bor base-fee recipient (burnt
	// contract), so snapshot its balance before/after the (re-)execution and add
	// the diff manually below. traceTx executes the message on preState.
	burnAddr := api.parityBurntContract(in.vmctx.BlockNumber.Uint64())
	var burnPre *big.Int
	if burnAddr != (common.Address{}) {
		burnPre = in.statedb.GetBalance(burnAddr).ToBig()
	}
	res, _, err := api.traceTx(ctx, in.tx, in.msg, in.txctx, in.vmctx, in.statedb, cfg, nil)
	if err != nil {
		return nil, err
	}

	raw, ok := res.(json.RawMessage)
	if !ok {
		if raw, err = json.Marshal(res); err != nil {
			return nil, fmt.Errorf("marshal stateDiff result: %w", err)
		}
	}

	var pd prestateDiffResult
	if err := json.Unmarshal(raw, &pd); err != nil {
		return nil, fmt.Errorf("unmarshal stateDiff result: %w", err)
	}

	sd := buildParityStateDiff(pd.Pre, pd.Post)
	// erigon's CompareStates emits a deletion only for an account that existed
	// before the tx AND does not exist after it. The prestate tracer marks an
	// account deleted on the SELFDESTRUCT opcode without regard to a later revert,
	// and can record a created-and-destroyed account's code as "pre". Both produce
	// spurious deletion entries. Drop any deletion where the account still exists
	// after execution (reverted/ineffective self-destruct) or did not exist before
	// it (created-and-destroyed transient), checked against the true pre/post state.
	for addr, acc := range sd {
		if isRemovalDiff(acc.Balance) && !(initial.Exist(addr) && !in.statedb.Exist(addr)) {
			delete(sd, addr)
		}
	}
	if burnPre != nil {
		addBalanceOnlyDiff(sd, burnAddr, burnPre, in.statedb.GetBalance(burnAddr).ToBig())
	}
	return sd, nil
}

// addBalanceOnlyDiff adds a balance-only account change to the stateDiff when the
// balance changed and the account isn't already present. Used for Bor fee
// recipients that the native prestate tracer doesn't capture.
func addBalanceOnlyDiff(sd parityStateDiff, addr common.Address, pre, post *big.Int) {
	if pre.Cmp(post) == 0 {
		return
	}
	if _, ok := sd[addr]; ok {
		return
	}
	sd[addr] = &parityAccountDiff{
		Balance: sdChanged((*hexutil.Big)(pre), (*hexutil.Big)(post)),
		Code:    sdSame(),
		Nonce:   sdSame(),
		Storage: map[common.Hash]interface{}{},
	}
}
