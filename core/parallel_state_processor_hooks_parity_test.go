package core

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/core/tracing"
)

// This file pins the contract that every tracing.Hooks field is
// consciously classified as either "V2 fires it" or "V2 deliberately
// skips it (known gap)". When upstream go-ethereum adds a new hook
// to tracing.Hooks, this test fails until the V2 author decides which
// bucket the hook belongs in.
//
// Background: V2 BlockSTM runs the EVM through ApplyMessageNoFeeLog,
// not through the serial StateProcessor.applyTransactionWithEVM. The
// serial path fires OnTxStart/OnTxEnd inside its applyTransaction
// wrapper; V2 does not. Other hooks (OnEnter, OnOpcode, …) are fired
// by the EVM core (vm/evm.go, vm/instructions.go, vm/interpreter.go)
// which is shared between paths, so they fire identically.
//
// We don't run a real tx through both paths and diff the firings here
// because (1) constructing a tx that exercises every hook is brittle,
// and (2) the diff would be noisy. Instead we encode the
// firing-versus-skipping decision per hook so any new hook upstream
// forces a deliberate choice.

// hookV2Status enumerates every field of tracing.Hooks. firedInV2 says
// whether the V2 path fires the hook. rationale documents the
// justification — required when firedInV2 is false.
type hookV2Status struct {
	firedInV2 bool
	rationale string // populated when firedInV2 is false
}

// hookV2Statuses is the source of truth for "is this hook fired by V2?".
// New entries must come with either firedInV2=true (with the file:line
// where it fires for reviewer convenience) or a rationale explaining
// why V2 deliberately skips it.
var hookV2Statuses = map[string]hookV2Status{
	// --- Fired by the shared EVM core (vm/evm.go, vm/interpreter.go) ---
	// V2 inherits these for free since vm.NewEVM is path-agnostic.
	"OnEnter":         {firedInV2: true},
	"OnExit":          {firedInV2: true},
	"OnOpcode":        {firedInV2: true},
	"OnFault":         {firedInV2: true},
	"OnGasChange":     {firedInV2: true},
	"OnBalanceChange": {firedInV2: true},
	"OnNonceChange":   {firedInV2: true},
	"OnNonceChangeV2": {firedInV2: true},
	"OnCodeChange":    {firedInV2: true},
	"OnCodeChangeV2":  {firedInV2: true},
	"OnStorageChange": {firedInV2: true},
	"OnLog":           {firedInV2: true},
	"OnBlockHashRead": {firedInV2: true},

	// --- Fired by BlockChain.ProcessBlock or chain init paths,
	//     orthogonal to which processor (V1/V2) ran the block ---
	"OnBlockStart":     {firedInV2: true},
	"OnBlockEnd":       {firedInV2: true},
	"OnBlockchainInit": {firedInV2: true},
	"OnGenesisBlock":   {firedInV2: true},
	"OnSkippedBlock":   {firedInV2: true},
	"OnClose":          {firedInV2: true},

	// --- Fired by the system-call helpers (e.g., EIP-4788 beacon root,
	//     EIP-2935 parent hash). V2's applyV2PreExecSystemCalls invokes
	//     them through the same vm.NewEVM that the serial path uses, so
	//     these inherit firing as well. ---
	"OnSystemCallStart":   {firedInV2: true},
	"OnSystemCallStartV2": {firedInV2: true},
	"OnSystemCallEnd":     {firedInV2: true},

	// --- Known V2 gap: per-tx start/end hooks ---
	"OnTxStart": {
		firedInV2: false,
		rationale: "V2's applyMessage calls ApplyMessageNoFeeLog directly without the OnTxStart wrapper that serial state_processor.go:197 fires. Tracing tools that hook OnTxStart see no V2 events. Tracked as a known gap; fixing requires either inlining the wrapper or refactoring state_transition.go.",
	},
	"OnTxEnd": {
		firedInV2: false,
		rationale: "Pair of OnTxStart — same gap, same fix.",
	},
}

// TestV2TracingHookParity enumerates tracing.Hooks fields via reflect
// and asserts every hook is classified in hookV2Statuses. New hooks
// upstream fail this test until the V2 author classifies them.
//
// The test does NOT verify that hooks marked firedInV2=true actually
// fire — that's the job of the EVM-level tracer tests in core/vm/. It
// only forces the conscious decision per hook on the V2 side.
func TestV2TracingHookParity(t *testing.T) {
	hooksType := reflect.TypeOf(tracing.Hooks{})

	var actual []string
	for i := 0; i < hooksType.NumField(); i++ {
		f := hooksType.Field(i)
		if f.Type.Kind() != reflect.Func {
			continue
		}
		actual = append(actual, f.Name)
	}
	sort.Strings(actual)

	var unclassified []string
	seenInTable := make(map[string]bool)
	for _, name := range actual {
		if _, ok := hookV2Statuses[name]; ok {
			seenInTable[name] = true
			continue
		}
		unclassified = append(unclassified, name)
	}

	if len(unclassified) > 0 {
		t.Errorf(`tracing.Hooks fields with no V2-handling classification (drift detected):

  %s

Add each to hookV2Statuses as either:
    firedInV2=true                          (V2 already fires it via shared EVM/EthAPI)
    firedInV2=false, rationale="..."        (V2 deliberately skips, document why)`,
			strings.Join(unclassified, "\n  "))
	}

	// Stale-entry check.
	var stale []string
	for name := range hookV2Statuses {
		if !seenInTable[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("hookV2Statuses has entries no longer in tracing.Hooks: %v", stale)
	}

	// Sanity: every firedInV2=false entry has a non-empty rationale.
	for name, status := range hookV2Statuses {
		if !status.firedInV2 && strings.TrimSpace(status.rationale) == "" {
			t.Errorf("hookV2Statuses[%s].firedInV2=false but no rationale provided", name)
		}
	}
}
