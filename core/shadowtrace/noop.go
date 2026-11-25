//go:build !lls_shadow

// Package shadowtrace is instrumentation for the LLS shadow-producer
// experiment. This is the default no-op build: every hook is an empty
// function the compiler inlines away. See shadowtrace.go (-tags lls_shadow).
package shadowtrace

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const Compiled = false

const (
	DropIdleBudget   = "idle-budget"
	DropIdleChanFull = "idle-chanfull"
	DropPlanChanFull = "plan-chanfull"
	DropFwdChanFull  = "fwd-chanfull"
	DropHandoffDrain = "handoff-drain"
	DropAbortSkip    = "abort-skip"

	OutcomeCompleted   = "completed"
	OutcomeReverted    = "reverted"
	OutcomeInterrupted = "interrupted"
	OutcomeError       = "error"
	OutcomePreAborted  = "pre-aborted"
)

func Enabled() bool                                               { return false }
func Gap8Fix() bool                                               { return false }
func PoolSeen(common.Hash, time.Time)                             {}
func MarkInjected(common.Hash, time.Time)                         {}
func IsInjected(common.Hash) bool                                 { return false }
func BuildStart(uint64, time.Time, time.Time, time.Time)          {}
func HeaderReady(uint64, time.Time)                               {}
func OnBuildStart(func(uint64, time.Time))                        {}
func OnBuildEnd(func(uint64))                                     {}
func Current() uint64                                             { return 0 }
func IdleStart(uint64)                                            {}
func BuilderStart(uint64)                                         {}
func Handoff(uint64)                                              {}
func PassStart(uint64)                                            {}
func Sent(uint64, common.Hash, string)                            {}
func Dropped(uint64, common.Hash, string)                         {}
func RunStart(uint64, common.Hash) time.Time                      { return time.Time{} }
func RunEnd(uint64, common.Hash, time.Time, string, uint64, bool) {}
func FillEnd(uint64, string)                                      {}
func BuildEnd(uint64, string)                                     {}
func SealSkipped(uint64, error)                                   {}
func Applied(uint64, common.Hash, ApplyInfo)                      {}
func NoDiscard() bool                                             { return false }
func XBWarm() bool                                                { return false }
func HeavyGas() uint64                                            { return 5_000_000 }
func Arm(common.Hash) string                                      { return "" }
func HeavySeen(common.Hash, time.Time, uint64, string, string, string) string {
	return ""
}
func WarmStart(common.Hash) time.Time                                       { return time.Time{} }
func WarmEnd(common.Hash, time.Time, uint64, string, uint64, time.Duration) {}
func HeadSeen(uint64, time.Time, []common.Hash, uint64, int)                {}
func WarmSecond(common.Hash, uint64, string, uint64, time.Duration, time.Duration,
	string, uint64, time.Duration, time.Duration) {
}
