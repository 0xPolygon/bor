//go:build !lls_shadow

package shadowtrace

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func DeferHeavy() bool                                    { return false }
func HWOff() bool                                         { return false }
func DeferMax() time.Duration                             { return 0 }
func WarmAdmit() bool                                     { return false }
func FloodRate() int                                      { return 0 }
func WarmQueued(common.Hash)                              {}
func WarmInProgress(common.Hash) (bool, time.Duration)    { return false, 0 }
func Deferred(uint64, common.Hash, time.Duration, bool)   {}
func Deferrals(common.Hash) int                           { return 0 }
func FloodTick(int, int, int, int, float64, float64, int) {}
func WarmAdmitLog(common.Hash, string, bool, string, float64, float64, float64, int64, bool) {
}
