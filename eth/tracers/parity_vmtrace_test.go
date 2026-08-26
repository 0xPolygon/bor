// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.

package tracers

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// vmTraceFrameJSON mirrors the JSON shape produced by the vmTrace tracer for a
// single call frame.
type vmTraceFrameJSON struct {
	Code string           `json:"code"`
	Ops  []map[string]any `json:"ops"`
}

// vmTraceContractCode runs a few opcodes: it writes to memory (MSTORE), writes a
// storage slot (SSTORE) and stops. Disassembly:
//
//	PUSH1 0x80 PUSH1 0x40 MSTORE PUSH1 0x01 PUSH1 0x00 SSTORE STOP
var vmTraceContractCode = []byte{0x60, 0x80, 0x60, 0x40, 0x52, 0x60, 0x01, 0x60, 0x00, 0x55, 0x00}

// vmTraceNewContractAPI builds a chain whose head block sends a tx to a contract
// account preloaded with vmTraceContractCode, and returns the trace API plus the
// tx hash.
func vmTraceNewContractAPI(t *testing.T) (*TraceAPI, common.Hash) {
	t.Helper()
	accounts := newAccounts(2)
	contract := accounts[1].addr
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
			contract:         {Balance: big.NewInt(0), Code: vmTraceContractCode},
		},
	}
	genBlocks := 2
	signer := types.HomesteadSigner{}
	var target common.Hash
	backend := newTestBackend(t, genBlocks, genesis, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &contract,
			Value:    big.NewInt(0),
			Gas:      100000,
			GasPrice: b.BaseFee(),
		}), signer, accounts[0].key)
		b.AddTx(tx)
		if i == genBlocks-1 {
			target = tx.Hash()
		}
	})
	t.Cleanup(backend.teardown)
	return &TraceAPI{API: NewAPI(backend)}, target
}

func TestReplayTransactionVMTrace(t *testing.T) {
	t.Parallel()
	api, target := vmTraceNewContractAPI(t)

	frame := replayVMTraceFrame(t, api, target)

	if frame.Code == "" || frame.Code == "0x" {
		t.Fatalf("expected non-empty frame code, got %q", frame.Code)
	}
	if len(frame.Ops) == 0 {
		t.Fatalf("expected at least one op in vmTrace")
	}

	first := frame.Ops[0]
	for _, key := range []string{"pc", "cost", "op", "idx", "ex"} {
		if _, ok := first[key]; !ok {
			t.Errorf("first op missing key %q: %+v", key, first)
		}
	}
	if got := first["op"]; got != "PUSH1" {
		t.Errorf("expected first op PUSH1, got %v", got)
	}
	if got := first["idx"]; got != "0-0" {
		t.Errorf("expected first op idx 0-0, got %v", got)
	}

	ex, ok := first["ex"].(map[string]any)
	if !ok {
		t.Fatalf("first op ex is not an object: %+v", first["ex"])
	}
	used, ok := ex["used"].(float64)
	if !ok {
		t.Fatalf("first op ex.used missing/not numeric: %+v", ex)
	}
	cost, ok := first["cost"].(float64)
	if !ok {
		t.Fatalf("first op cost missing/not numeric: %+v", first)
	}
	// Invariant: used = gasStart - cost, so used must be strictly below the tx
	// gas cap and below the (gasStart) implied bound.
	if used >= 100000 {
		t.Errorf("expected ex.used below the gas cap, got %v", used)
	}
	if cost <= 0 {
		t.Errorf("expected positive cost for PUSH1, got %v", cost)
	}

	// PUSH1 0x80 should push a single value.
	push, ok := ex["push"].([]any)
	if !ok || len(push) != 1 {
		t.Errorf("expected PUSH1 to push exactly one value, got %+v", ex["push"])
	} else if push[0] != "0x80" {
		t.Errorf("expected pushed value 0x80, got %v", push[0])
	}

	// Locate the SSTORE op and assert its store is captured.
	var foundStore bool
	for _, op := range frame.Ops {
		if op["op"] != "SSTORE" {
			continue
		}
		opEx, ok := op["ex"].(map[string]any)
		if !ok {
			t.Fatalf("SSTORE op missing ex: %+v", op)
		}
		store, ok := opEx["store"].(map[string]any)
		if !ok {
			t.Fatalf("SSTORE op missing store: %+v", opEx)
		}
		if _, ok := store["key"]; !ok {
			t.Errorf("SSTORE store missing key: %+v", store)
		}
		if _, ok := store["val"]; !ok {
			t.Errorf("SSTORE store missing val: %+v", store)
		}
		foundStore = true
	}
	if !foundStore {
		t.Errorf("expected an SSTORE op with a store field")
	}
}

func TestReplayTransactionVMTrace_EOA(t *testing.T) {
	t.Parallel()
	api, target, _ := newTransferChainAPI(t, 2)

	frame := replayVMTraceFrame(t, api, target)

	if frame.Code != "0x" {
		t.Errorf("expected code 0x for EOA transfer, got %q", frame.Code)
	}
	if len(frame.Ops) != 0 {
		t.Errorf("expected no ops for EOA transfer, got %d", len(frame.Ops))
	}
}
