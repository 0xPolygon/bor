//go:build devnet_repro

package miner

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/log"
	"github.com/holiman/uint256"
)

// StateDiffAccount is the post-state of one account in a staged write set,
// matching the "post" shape of the prestateTracer in diffMode.
type StateDiffAccount struct {
	Balance *hexutil.Big                `json:"balance,omitempty"`
	Nonce   *hexutil.Uint64             `json:"nonce,omitempty"`
	Code    hexutil.Bytes               `json:"code,omitempty"`
	Storage map[common.Hash]common.Hash `json:"storage,omitempty"`
}

// StateDiffSpec is a write set to apply directly to the next block this
// worker builds, without executing any transaction. It replays the state
// footprint of a historical block so the state-root computation in
// FinalizeAndAssemble does the same trie work the original block did.
type StateDiffSpec struct {
	Label    string                              `json:"label"`
	Accounts map[common.Address]StateDiffAccount `json:"accounts"`
}

var (
	stateDiffMu      sync.Mutex
	stateDiffPending *StateDiffSpec
)

// StageStateDiff stages spec for the next block build, replacing any spec
// staged earlier.
func (miner *Miner) StageStateDiff(spec StateDiffSpec) {
	stateDiffMu.Lock()
	defer stateDiffMu.Unlock()

	stateDiffPending = &spec
}

func takeStateDiff() *StateDiffSpec {
	stateDiffMu.Lock()
	defer stateDiffMu.Unlock()

	spec := stateDiffPending
	stateDiffPending = nil

	return spec
}

// devnetInjectStateDiff applies the staged write set (if any) to env.state.
func devnetInjectStateDiff(env *environment) {
	spec := takeStateDiff()
	if spec == nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Error("devnet_repro: panic while injecting state diff, recovered", "label", spec.Label, "panic", r)
		}
	}()

	start := time.Now()
	slots := 0

	for addr, acc := range spec.Accounts {
		if acc.Balance != nil {
			bal, overflow := uint256.FromBig(acc.Balance.ToInt())
			if !overflow {
				env.state.SetBalance(addr, bal, tracing.BalanceChangeUnspecified)
			}
		}
		if acc.Nonce != nil {
			env.state.SetNonce(addr, uint64(*acc.Nonce), tracing.NonceChangeUnspecified)
		}
		if len(acc.Code) > 0 {
			env.state.SetCode(addr, acc.Code, tracing.CodeChangeUnspecified)
		}
		for k, v := range acc.Storage {
			env.state.SetState(addr, k, v)
			slots++
		}
	}

	log.Info("devnet_repro: injected state diff", "label", spec.Label, "accounts", len(spec.Accounts),
		"slots", slots, "number", env.header.Number, "elapsed", time.Since(start))
}
