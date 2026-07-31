// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.

package tracers

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// parityFrameCtx carries the per-transaction/block identifiers and configuration
// that every frame in a convertCallFrameToParityTraces call tree shares.
// intrinsicGas and rootGasUsed are nonzero only for the top-level call;
// recursive calls zero them out.
type parityFrameCtx struct {
	txHash       common.Hash
	txIndex      uint64
	blockHash    common.Hash
	blockNumber  uint64
	intrinsicGas uint64
	rootGasUsed  uint64
	precompiles  map[common.Address]struct{}
}

// parityFrame wraps a single callTracer JSON frame and exposes typed accessors
// for its fields. JSON numbers decode to float64 in map[string]interface{}, so
// numeric fields (gas, gasUsed) need the float64 fallback to yield a hex string.
type parityFrame map[string]interface{}

// str returns the frame field as a string, converting numeric JSON values to
// hex strings and returning "" for absent fields.
func (f parityFrame) str(key string) string {
	val, ok := f[key]
	if !ok || val == nil {
		return ""
	}
	if s, ok := val.(string); ok {
		return s
	}
	if n, ok := val.(float64); ok {
		return hexutil.EncodeUint64(uint64(n))
	}
	if n, ok := val.(uint64); ok {
		return hexutil.EncodeUint64(n)
	}
	return ""
}

// to returns the frame's "to" address, with ok reporting a non-empty value.
func (f parityFrame) to() (common.Address, bool) {
	toAddr, ok := f["to"].(string)
	if !ok || toAddr == "" {
		return common.Address{}, false
	}
	return common.HexToAddress(toAddr), true
}

// bigOrZero parses the frame field as a big.Int, defaulting to 0.
func (f parityFrame) bigOrZero(key string) *big.Int {
	if s := f.str(key); s != "" {
		if v, ok := new(big.Int).SetString(s, 0); ok {
			return v
		}
	}
	return new(big.Int)
}

// parityTraceType maps a callTracer type string to the Parity traceType
// ("call", "create", "suicide") and, for call-like ops, the Parity callType
// ("call", "delegatecall", "staticcall", "callcode").
func parityTraceType(typeStr string) (traceType string, callType *string) {
	switch typeStr {
	case "CREATE", "CREATE2":
		return "create", nil
	case "SELFDESTRUCT", "SUICIDE":
		return "suicide", nil
	}
	ct := "call"
	switch typeStr {
	case "DELEGATECALL":
		ct = "delegatecall"
	case "STATICCALL":
		ct = "staticcall"
	case "CALLCODE":
		ct = "callcode"
	}
	return "call", &ct
}

// parityChildFrames returns the frame's direct subcalls that are not pure
// precompile calls. Parity/erigon omits value-less sub-calls to precompiles
// from the trace list, so they must be dropped before counting subtraces or
// assigning traceAddress indices.
func parityChildFrames(f parityFrame, precompiles map[common.Address]struct{}) []parityFrame {
	calls, _ := f["calls"].([]interface{})
	out := make([]parityFrame, 0, len(calls))
	for _, c := range calls {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		// Children of any frame are sub-calls (deep == true); the top-level frame
		// is never run through this filter (the root is added unconditionally).
		if !isPrecompileFrame(m, true, precompiles) {
			out = append(out, parityFrame(m))
		}
	}
	return out
}

// paritySuicideAction builds the SELFDESTRUCT action: {address, refundAddress,
// balance}, with no callType/gas/input (distinct from call/create actions).
func paritySuicideAction(f parityFrame) *ParityTraceAction {
	action := &ParityTraceAction{}
	if fromAddr := f.str("from"); fromAddr != "" {
		a := common.HexToAddress(fromAddr)
		action.Address = &a
	}
	if to, ok := f.to(); ok {
		action.RefundAddress = &to
	}
	action.Balance = (*hexutil.Big)(f.bigOrZero("value"))
	return action
}

// parityCallAction builds the action for a call or create trace. intrinsicGas is
// nonzero only for the top-level call; it is subtracted from action.gas so the
// reported gas reflects what was available to the EVM call itself (Parity/erigon
// semantics).
func parityCallAction(f parityFrame, traceType string, callType *string, intrinsicGas uint64) *ParityTraceAction {
	action := &ParityTraceAction{CallType: callType}
	if fromAddr := f.str("from"); fromAddr != "" {
		from := common.HexToAddress(fromAddr)
		action.From = &from
	}
	// create actions carry no "to"; they use creationMethod and report the new
	// contract address in result.address instead.
	if to, ok := f.to(); ok && traceType != "create" {
		action.To = &to
	}
	if traceType == "create" {
		cm := "create"
		if f.str("type") == "CREATE2" {
			cm = "create2"
		}
		action.CreationMethod = &cm
	}
	setParityActionGas(action, f.str("gas"), intrinsicGas)
	setParityActionInput(action, f.str("input"), traceType)
	// Parity always includes a value field on call/create actions (default 0x0),
	// even for staticcall/delegatecall which carry no value of their own.
	if traceType == "call" || traceType == "create" {
		action.Value = (*hexutil.Big)(f.bigOrZero("value"))
	}
	return action
}

// setParityActionGas sets action.gas = frame gas - intrinsicGas (floored at the
// intrinsic subtraction only when it doesn't underflow).
func setParityActionGas(action *ParityTraceAction, gasStr string, intrinsicGas uint64) {
	if gasStr == "" {
		return
	}
	g, err := hexutil.DecodeUint64(gasStr)
	if err != nil {
		return
	}
	if g >= intrinsicGas {
		g -= intrinsicGas
	}
	gasHex := hexutil.Uint64(g)
	action.Gas = &gasHex
}

// setParityActionInput stores the call input as action.input, or as action.init
// for create traces.
func setParityActionInput(action *ParityTraceAction, inputStr, traceType string) {
	if inputStr == "" {
		return
	}
	inputBytes := hexutil.MustDecode(inputStr)
	if traceType == "create" {
		action.Init = (*hexutil.Bytes)(&inputBytes)
	} else {
		action.Input = (*hexutil.Bytes)(&inputBytes)
	}
}

// parityErrorString maps a callTracer error to the Parity trace error field:
// nil on success, "Reverted" for reverts, the raw EVM string otherwise.
func parityErrorString(errorStr string) *string {
	if errorStr == "" {
		return nil
	}
	e := errorStr
	if errorStr == "execution reverted" {
		e = "Reverted"
	}
	return &e
}

// parityResultGasUsed returns the result.gasUsed value. The root trace uses the
// precomputed gross EVM execution gas (gasLimit - postExecGasRemaining -
// intrinsicGas, excluding the EIP-7623 data floor and gas refunds — matching
// erigon); subcalls use the callTracer frame gasUsed, which is already gross.
func parityResultGasUsed(gasUsedStr string, isRoot bool, rootGasUsed uint64) *hexutil.Uint64 {
	if isRoot {
		guHex := hexutil.Uint64(rootGasUsed)
		return &guHex
	}
	if gasUsedStr == "" {
		return nil
	}
	gu, err := hexutil.DecodeUint64(gasUsedStr)
	if err != nil {
		return nil
	}
	guHex := hexutil.Uint64(gu)
	return &guHex
}

// parityBytes decodes a hex string to bytes, defaulting to an empty slice
// (which serializes to "0x").
func parityBytes(s string) hexutil.Bytes {
	if s == "" {
		return hexutil.Bytes{}
	}
	return hexutil.MustDecode(s)
}

// parityResult builds the result for a call or create trace, following erigon
// semantics:
//   - success:     result populated, no error
//   - revert:      result populated (gasUsed + revert output) AND error "Reverted"
//   - other error: result nil, error = raw EVM string
func parityResult(f parityFrame, traceType, errorStr string, isRoot bool, ctx parityFrameCtx) *ParityTraceResult {
	isRevert := errorStr == "execution reverted"
	if errorStr != "" && !isRevert {
		return nil
	}

	result := &ParityTraceResult{
		GasUsed: parityResultGasUsed(f.str("gasUsed"), isRoot, ctx.rootGasUsed),
	}
	if traceType == "create" && errorStr == "" {
		// Always emit code (default "0x"): a create deploying empty code still
		// reports result.code, matching erigon.
		ob := parityBytes(f.str("output"))
		result.Code = &ob
		if to, ok := f.to(); ok {
			result.Address = &to
		}
	} else {
		ob := parityBytes(f.str("output"))
		result.Output = &ob
	}
	return result
}

// convertCallFrameToParityTraces converts a callTracer frame into Parity-format
// traces, recursively flattening nested calls and assigning traceAddress indices.
func convertCallFrameToParityTraces(frame map[string]interface{}, traceAddress []uint64, ctx parityFrameCtx) ([]*ParityTrace, error) {
	f := parityFrame(frame)
	traceType, callType := parityTraceType(f.str("type"))
	childCalls := parityChildFrames(f, ctx.precompiles)

	trace := &ParityTrace{
		Type:                traceType,
		TraceAddress:        append([]uint64{}, traceAddress...),
		Subtraces:           uint64(len(childCalls)),
		TransactionHash:     &ctx.txHash,
		TransactionPosition: &ctx.txIndex,
		BlockHash:           &ctx.blockHash,
		BlockNumber:         &ctx.blockNumber,
	}

	// SELFDESTRUCT uses a distinct action shape and carries no result. It has no
	// subcalls.
	if traceType == "suicide" {
		trace.Action = paritySuicideAction(f)
		return []*ParityTrace{trace}, nil
	}

	errorStr := f.str("error")
	trace.Action = parityCallAction(f, traceType, callType, ctx.intrinsicGas)
	trace.Result = parityResult(f, traceType, errorStr, len(traceAddress) == 0, ctx)
	trace.Error = parityErrorString(errorStr)

	traces := []*ParityTrace{trace}
	for i, child := range childCalls {
		subAddress := append(append([]uint64{}, traceAddress...), uint64(i))
		subCtx := ctx
		subCtx.intrinsicGas = 0
		subCtx.rootGasUsed = 0
		subTraces, err := convertCallFrameToParityTraces(child, subAddress, subCtx)
		if err != nil {
			return nil, err
		}
		traces = append(traces, subTraces...)
	}
	return traces, nil
}

// isPrecompileFrame reports whether a callTracer subframe is a precompile call
// that Parity/erigon omits from the trace list. erigon omits a precompile call
// only when it is a sub-call (deep) AND carries zero value; top-level calls and
// value-bearing calls to a precompile are kept. See erigon trace_adhoc.go
// captureStartOrEnter: if precompile && deep && (value == nil || value.IsZero()).
//
// precompiles is the active precompile set for the block (from vm.ActivePrecompiles);
// passing a nil set falls back to checking nothing (no frames filtered).
func isPrecompileFrame(frame map[string]interface{}, deep bool, precompiles map[common.Address]struct{}) bool {
	if !deep || len(precompiles) == 0 {
		return false
	}
	switch t, _ := frame["type"].(string); t {
	case "CREATE", "CREATE2", "SELFDESTRUCT", "SUICIDE":
		return false
	}
	toAddr, ok := frame["to"].(string)
	if !ok || toAddr == "" {
		return false
	}
	addr := common.HexToAddress(toAddr)
	if _, isPrecompile := precompiles[addr]; !isPrecompile {
		return false
	}
	// A precompile call that transfers value is kept; only zero-value ones omitted.
	if v, ok := frame["value"].(string); ok && v != "" {
		if val, ok := new(big.Int).SetString(v, 0); ok && val.Sign() != 0 {
			return false
		}
	}
	return true
}
