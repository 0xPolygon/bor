//go:build !devnet_repro

package vm

import "time"

// devnetTraceOpcodeGapForKey is a no-op in any build without the devnet_repro
// tag. See core/vm/devnet_repro.go for the real implementation.
func devnetTraceOpcodeGapForKey(key *uint64) {}

// devnetClearOpcodeGapKey is a no-op in any build without the devnet_repro
// tag. See core/vm/devnet_repro.go for the real implementation.
func devnetClearOpcodeGapKey(key *uint64) {}

// DrainSlowOpcodeGaps is a no-op in any build without the devnet_repro tag.
// See core/vm/devnet_repro.go for the real implementation.
func DrainSlowOpcodeGaps() []time.Duration { return nil }
