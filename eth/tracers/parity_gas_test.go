package tracers

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/params"
)

// TestConvertRootGasUsedGrossExec locks the root gas accounting against the real
// values observed from an erigon reference node (staticcall tx 0xbe61...):
// the converter emits the caller-supplied gross gasUsed (erigon's 0x5b628) for
// the root, and action.gas is the gas limit (0xc5a70) minus the intrinsic
// (21856 = 0xc0510).
func TestConvertRootGasUsedGrossExec(t *testing.T) {
	t.Parallel()

	frame := map[string]interface{}{
		"type": "CALL", "from": "0xaa00000000000000000000000000000000000001",
		"to":  "0xbb00000000000000000000000000000000000002",
		"gas": "0xc5a70", "gasUsed": "0x5a6d4", "input": "0x", "output": "0x", "value": "0x0",
	}

	traces, err := convertCallFrameToParityTraces(frame, []uint64{}, parityFrameCtx{
		intrinsicGas: 21856,
		rootGasUsed:  0x5b628,
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	root := traces[0]
	if root.Result == nil || root.Result.GasUsed == nil {
		t.Fatalf("missing result.gasUsed")
	}
	if got := uint64(*root.Result.GasUsed); got != 0x5b628 {
		t.Errorf("gross gasUsed = %#x, want 0x5b628", got)
	}
	if root.Action == nil || root.Action.Gas == nil || uint64(*root.Action.Gas) != 0xc0510 {
		t.Errorf("action.gas = %v, want 0xc0510", root.Action.Gas)
	}
}

// TestReplayTransactionGrossGasRefund executes a transaction that clears a
// preset storage slot (triggering an EIP-3529 refund) and asserts the Parity
// root gasUsed reports the gross execution gas (refund added back), i.e. it
// exceeds net-minus-intrinsic. Without the refund fix the two would be equal.
func TestReplayTransactionGrossGasRefund(t *testing.T) {
	t.Parallel()

	contract := common.HexToAddress("0x00000000000000000000000000000000000c0de1")
	// PUSH1 0x00; PUSH1 0x00; SSTORE; STOP  -> sets slot 0 to 0 (clears it).
	code := []byte{0x60, 0x00, 0x60, 0x00, 0x55, 0x00}
	traceAPI, target := newPrefundedContractAPI(t, contract, code, map[common.Hash]common.Hash{{}: common.HexToHash("0x1")})

	ptraces, err := traceAPI.Transaction(t.Context(), target)
	if err != nil {
		t.Fatalf("trace_transaction: %v", err)
	}
	if len(ptraces) == 0 || ptraces[0].Result == nil || ptraces[0].Result.GasUsed == nil {
		t.Fatalf("missing parity root gasUsed")
	}
	gross := uint64(*ptraces[0].Result.GasUsed)

	// Net root gasUsed from the plain callTracer (post-refund, intrinsic-inclusive).
	ct := "callTracer"
	res, err := traceAPI.TraceTransaction(t.Context(), target, &TraceConfig{Tracer: &ct})
	if err != nil {
		t.Fatalf("debug callTracer: %v", err)
	}
	raw, _ := res.(json.RawMessage)
	var frame map[string]interface{}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal callTracer frame: %v", err)
	}
	net, err := hexutil.DecodeUint64(frame["gasUsed"].(string))
	if err != nil {
		t.Fatalf("decode net gasUsed: %v", err)
	}

	// With the refund added back, gross > net - intrinsic. (Equal would mean the
	// refund was not captured.) Intrinsic for this no-calldata call is params.TxGas.
	if gross <= net-params.TxGas {
		t.Errorf("expected gross gasUsed > net-intrinsic (refund applied): gross=%d net=%d intrinsic=%d", gross, net, params.TxGas)
	}
}
