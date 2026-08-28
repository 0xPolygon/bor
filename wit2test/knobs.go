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
	// MaliciousMode selects the attacker-simulation loop for this node, or ""
	// to run this node honestly. Values: "forge" (one bad-signer announcement
	// per new head), "flood" (high-rate bad-signer + fabricated-witness-hash
	// announcements, for anti-abuse strike/quarantine testing). Never real
	// validator key material — always a throwaway key generated at process
	// start, so this can never accidentally leak or misuse a devnet signer.
	MaliciousMode string
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
	k.MaliciousMode = os.Getenv("WIT2_TEST_MALICIOUS_MODE")
}

func Enabled() bool          { return k.enabled.Load() }
func Get() *Knobs            { return &k }
func MaliciousMode() string  { return Get().MaliciousMode }

func atoi(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
