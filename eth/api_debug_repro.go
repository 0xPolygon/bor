//go:build devnet_repro

package eth

import (
	"time"

	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/miner"
)

// StageFakeTx stages a devnet-only, signature-bypassed transaction to be
// injected into the next block this node's miner builds. Only exists in
// binaries built with -tags devnet_repro. See design doc
// docs/superpowers/specs/2026-09-03-heavy-contract-slow-block-repro-design.md §5b.
func (api *DebugAPI) StageFakeTx(spec miner.FakeTxSpec) error {
	api.eth.miner.StageFakeTx(spec)
	return nil
}

// DrainSlowOpcodeGaps returns and clears the log of per-opcode execution gaps
// exceeding the slow-opcode threshold, recorded since the last call. Only
// exists in binaries built with -tags devnet_repro. See design doc §8.
func (api *DebugAPI) DrainSlowOpcodeGaps() []time.Duration {
	gaps := vm.DrainSlowOpcodeGaps()
	if gaps == nil {
		return []time.Duration{}
	}

	return gaps
}
