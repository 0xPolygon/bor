//go:build !devnet_repro

package vm

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// SlowOpcodeGap mirrors the devnet_repro-tagged type of the same name (see
// core/vm/devnet_repro.go) so DrainSlowOpcodeGaps has an identical signature
// in both builds. Never populated in a build without the devnet_repro tag.
type SlowOpcodeGap struct {
	Duration time.Duration
	Op       OpCode
	Pc       uint64
	Contract common.Address
}

// devnetTraceOpcodeGapForKey is a no-op in any build without the devnet_repro
// tag. See core/vm/devnet_repro.go for the real implementation.
func devnetTraceOpcodeGapForKey(key *uint64, op OpCode, pc uint64, contractAddr common.Address) {}

// devnetClearOpcodeGapKey is a no-op in any build without the devnet_repro
// tag. See core/vm/devnet_repro.go for the real implementation.
func devnetClearOpcodeGapKey(key *uint64) {}

// DrainSlowOpcodeGaps is a no-op in any build without the devnet_repro tag.
// See core/vm/devnet_repro.go for the real implementation.
func DrainSlowOpcodeGaps() []SlowOpcodeGap { return nil }
