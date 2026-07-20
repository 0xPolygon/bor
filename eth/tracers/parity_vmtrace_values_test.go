package tracers

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

type testVMTraceMemEntry struct {
	Off  int    `json:"off"`
	Data string `json:"data"`
}

type testVMTraceOpEx struct {
	Push []string             `json:"push"`
	Mem  *testVMTraceMemEntry `json:"mem"`
}

type testVMTraceOp struct {
	Op string          `json:"op"`
	Ex testVMTraceOpEx `json:"ex"`
}

type testVMTraceResult struct {
	Code string          `json:"code"`
	Ops  []testVMTraceOp `json:"ops"`
}

// TestVMTracePushMemValues locks the precise push/mem values: a contract running
// PUSH1 0x80; PUSH1 0x40; MSTORE; STOP must report push ["0x80"], ["0x40"] for
// the PUSH1s and mem {off:64, 32 bytes} with empty push for MSTORE.
func TestVMTracePushMemValues(t *testing.T) {
	t.Parallel()

	contract := common.HexToAddress("0x00000000000000000000000000000000000c0de2")
	code := []byte{0x60, 0x80, 0x60, 0x40, 0x52, 0x00} // PUSH1 0x80; PUSH1 0x40; MSTORE; STOP
	api, target := newPrefundedContractAPI(t, contract, code, nil)
	res, err := api.ReplayTransaction(t.Context(), target, []string{"vmTrace"})
	if err != nil {
		t.Fatalf("replayTransaction vmTrace: %v", err)
	}
	raw, _ := res.VMTrace.(json.RawMessage)
	var vt testVMTraceResult
	if err := json.Unmarshal(raw, &vt); err != nil {
		t.Fatalf("unmarshal vmTrace: %v (%s)", err, string(raw))
	}
	if len(vt.Ops) < 3 {
		t.Fatalf("expected >=3 ops, got %d: %s", len(vt.Ops), string(raw))
	}

	if vt.Ops[0].Op != "PUSH1" || len(vt.Ops[0].Ex.Push) != 1 || vt.Ops[0].Ex.Push[0] != "0x80" {
		t.Errorf("op0 expected PUSH1 push [0x80], got %s %v", vt.Ops[0].Op, vt.Ops[0].Ex.Push)
	}
	if vt.Ops[1].Op != "PUSH1" || len(vt.Ops[1].Ex.Push) != 1 || vt.Ops[1].Ex.Push[0] != "0x40" {
		t.Errorf("op1 expected PUSH1 push [0x40], got %s %v", vt.Ops[1].Op, vt.Ops[1].Ex.Push)
	}
	m := vt.Ops[2]
	if m.Op != "MSTORE" || len(m.Ex.Push) != 0 {
		t.Errorf("op2 expected MSTORE push [], got %s %v", m.Op, m.Ex.Push)
	}
	if m.Ex.Mem == nil || m.Ex.Mem.Off != 64 {
		t.Fatalf("MSTORE mem expected off 64, got %+v", m.Ex.Mem)
	}
	// data is the full 32-byte word written (0x00..0080).
	if len(m.Ex.Mem.Data) != 2+64 { // "0x" + 32 bytes hex
		t.Errorf("MSTORE mem data expected 32 bytes, got %q", m.Ex.Mem.Data)
	}
}
