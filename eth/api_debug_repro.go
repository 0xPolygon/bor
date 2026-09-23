//go:build devnet_repro

package eth

import (
	"fmt"

	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/miner"
)

// devnetPeerCount returns the number of connected p2p peers for api.eth, used
// by StageFakeTx to enforce the p2p-disconnected precondition (final review
// finding I3). Overridable in tests: constructing a real, running
// *p2p.Server with connected peers is impractical in a unit test, so tests
// swap this var instead. Guards against a nil eth.p2pServer (e.g. the
// minimal test fixture in api_debug_repro_test.go, which only sets the
// fields its other tests need) by treating "no server configured" as zero
// peers, matching the safe default in any real node (p2pServer is always
// set by the time RPC methods are reachable).
var devnetPeerCount = func(api *DebugAPI) int {
	if api.eth == nil || api.eth.p2pServer == nil {
		return 0
	}

	return api.eth.p2pServer.PeerCount()
}

// StageFakeTx stages a devnet-only, signature-bypassed transaction to be
// injected into the next block this node's miner builds. Only exists in
// binaries built with -tags devnet_repro. See design doc
// docs/superpowers/specs/2026-09-03-heavy-contract-slow-block-repro-design.md §5b.
//
// Fails closed if the node currently has any connected p2p peers (final
// review finding I3): the design's entire safety argument for "this can't
// reach real mainnet peers" otherwise rests on a runbook step (disconnect
// p2p) that a human could skip under time pressure. Staging a fake tx alone
// is network-inert — it never enters the txpool — but the block the miner
// later builds from it is indistinguishable from a real locally-mined block
// once inserted, and WILL be gossiped if peers are connected (see design doc
// §7). This check is a cheap, code-level backstop for that runbook step, not
// a replacement for actually disconnecting p2p — it only catches the
// "forgot to disconnect" case, not e.g. a peer reconnecting mid-experiment.
func (api *DebugAPI) StageFakeTx(spec miner.FakeTxSpec) error {
	if n := devnetPeerCount(api); n > 0 {
		return fmt.Errorf("debug_stageFakeTx: refusing to stage: node has %d connected peer(s); disconnect p2p before staging a fake transaction (see design doc §7)", n)
	}

	api.eth.miner.StageFakeTx(spec)
	return nil
}

// DrainSlowOpcodeGaps returns and clears the log of per-opcode execution gaps
// exceeding the slow-opcode threshold, recorded since the last call. Only
// exists in binaries built with -tags devnet_repro. See design doc §8.
func (api *DebugAPI) DrainSlowOpcodeGaps() []vm.SlowOpcodeGap {
	gaps := vm.DrainSlowOpcodeGaps()
	if gaps == nil {
		return []vm.SlowOpcodeGap{}
	}

	return gaps
}

// StageStateDiff stages a write set (accounts, balances, nonces, code,
// storage) to be applied directly to the next block this node's miner builds,
// replaying a historical block's state footprint without executing its
// transactions. Same p2p-disconnected precondition as StageFakeTx.
func (api *DebugAPI) StageStateDiff(spec miner.StateDiffSpec) error {
	if n := devnetPeerCount(api); n > 0 {
		return fmt.Errorf("debug_stageStateDiff: refusing to stage: node has %d connected peer(s)", n)
	}

	api.eth.miner.StageStateDiff(spec)
	return nil
}
