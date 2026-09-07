//go:build !devnet_repro

package miner

// devnetInjectFakeTx is a no-op in any build without the devnet_repro tag.
// See miner/devnet_repro.go for the real implementation and
// docs/superpowers/specs/2026-09-03-heavy-contract-slow-block-repro-design.md §9
// for why this must be a compile-time, not runtime, gate.
func devnetInjectFakeTx(w *worker, env *environment) {}
