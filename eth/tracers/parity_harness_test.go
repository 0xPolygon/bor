// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.

package tracers

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// newTransferChainAPI builds a hermetic chain of `blocks` blocks, each carrying
// one 1000-wei transfer from a funded sender to a second account, and returns
// the trace API, the head-block transaction hash and the sender address.
// The native callTracer is registered for tests via register_native_test.go.
func newTransferChainAPI(t *testing.T, blocks int) (*TraceAPI, common.Hash, common.Address) {
	t.Helper()

	accounts := newAccounts(2)
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
			accounts[1].addr: {Balance: big.NewInt(params.Ether)},
		},
	}
	signer := types.HomesteadSigner{}
	var target common.Hash
	backend := newTestBackend(t, blocks, genesis, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &accounts[1].addr,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: b.BaseFee(),
		}), signer, accounts[0].key)
		b.AddTx(tx)
		if i == blocks-1 {
			target = tx.Hash()
		}
	})
	t.Cleanup(backend.teardown)

	return &TraceAPI{API: NewAPI(backend)}, target, accounts[0].addr
}

// newPrefundedContractAPI builds a single-block chain whose only transaction
// calls a contract pre-deployed in genesis with the given code and storage,
// returning the trace API and that transaction's hash.
func newPrefundedContractAPI(t *testing.T, contract common.Address, code []byte, storage map[common.Hash]common.Hash) (*TraceAPI, common.Hash) {
	t.Helper()

	accounts := newAccounts(1)
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
			contract: {
				Code:    code,
				Balance: big.NewInt(0),
				Storage: storage,
			},
		},
	}
	signer := types.HomesteadSigner{}
	var target common.Hash
	backend := newTestBackend(t, 1, genesis, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce: uint64(i), To: &contract, Gas: 100000, GasPrice: b.BaseFee(),
		}), signer, accounts[0].key)
		b.AddTx(tx)
		target = tx.Hash()
	})
	t.Cleanup(backend.teardown)

	return &TraceAPI{API: NewAPI(backend)}, target
}

// replayVMTraceFrame runs trace_replayTransaction with the vmTrace type and
// decodes the result into the generic frame shape used by assertions.
func replayVMTraceFrame(t *testing.T, api *TraceAPI, target common.Hash) vmTraceFrameJSON {
	t.Helper()

	res, err := api.ReplayTransaction(t.Context(), target, []string{"vmTrace"})
	if err != nil {
		t.Fatalf("trace_replayTransaction vmTrace error: %v", err)
	}
	if res.VMTrace == nil {
		t.Fatalf("expected non-nil VMTrace")
	}
	// vmTrace-only request must not populate trace/stateDiff.
	if res.Trace != nil {
		t.Errorf("expected nil Trace when only vmTrace requested, got %+v", res.Trace)
	}
	if res.StateDiff != nil {
		t.Errorf("expected nil StateDiff when only vmTrace requested, got %+v", res.StateDiff)
	}

	raw, err := json.Marshal(res.VMTrace)
	if err != nil {
		t.Fatalf("marshal VMTrace: %v", err)
	}
	var frame vmTraceFrameJSON
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal VMTrace: %v", err)
	}
	return frame
}

// newRegisteredRPCServer builds a one-block backend, registers the RPC API set
// produced by apisFor on an in-proc server, and returns the server.
func newRegisteredRPCServer(t *testing.T, apisFor func(Backend) []rpc.API) *rpc.Server {
	t.Helper()

	accounts := newAccounts(1)
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
		},
	}
	backend := newTestBackend(t, 1, genesis, func(i int, b *core.BlockGen) {})
	t.Cleanup(backend.teardown)

	server := rpc.NewServer("", 1, 5*time.Second)
	for _, api := range apisFor(backend) {
		if err := server.RegisterName(api.Namespace, api.Service); err != nil {
			t.Fatalf("failed to register %s API: %v", api.Namespace, err)
		}
	}
	return server
}
