// Package wit2test is a throwaway dev-only harness for observing WIT2
// witness propagation on a kurtosis devnet. Nothing here ships — every call
// site is gated on Enabled() so a binary built without WIT2_TEST_ENABLED=1
// stays inert.
package wit2test

import (
	"os"
	"strconv"
	"sync/atomic"
)

type Knobs struct {
	enabled         atomic.Bool
	ImportDelayMs   int
	SampleN         int
	WitnessPadBytes int
}

var k Knobs

func init() { Load() }

func Load() {
	if os.Getenv("WIT2_TEST_ENABLED") != "1" {
		k.enabled.Store(false)
		return
	}
	k.enabled.Store(true)
	k.ImportDelayMs = atoi("WIT2_TEST_IMPORT_DELAY_MS", 0)
	k.SampleN = atoi("WIT2_TEST_LOG_SAMPLE_N", 1)
	k.WitnessPadBytes = atoi("WIT2_TEST_WITNESS_PAD_BYTES", 0)
}

func Enabled() bool { return k.enabled.Load() }
func Get() *Knobs   { return &k }

func atoi(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
