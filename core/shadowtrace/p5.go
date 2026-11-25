//go:build lls_shadow

package shadowtrace

// P5 hooks: late-tx rule (deferheavy), warm admission filter (warmadmit)
// and the local flood cost test (flood). All are runtime toggles in the
// toggles file, default off.

import (
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

// DeferHeavy reports whether the builder must skip a heavy tx while its
// early warm is still queued or running (try it in a later pass/block).
func DeferHeavy() bool { return Enabled() && toggle("deferheavy") == "on" }

// DeferMax is the longest a warm may block its tx; after that the builder
// applies the tx cold (no starvation).
func DeferMax() time.Duration {
	if v, err := strconv.Atoi(toggle("defermaxms")); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}

	return 800 * time.Millisecond
}

// WarmAdmit reports whether warms are restricted to txs the next block can
// take (fee cap >= next base fee + miner min tip).
func WarmAdmit() bool { return Enabled() && toggle("warmadmit") == "on" }

// FloodRate is the number of synthetic warm jobs per second (0 = off).
func FloodRate() int {
	if v, err := strconv.Atoi(toggle("flood")); err == nil && v > 0 {
		return v
	}

	return 0
}

// WarmQueued records that a warm was queued for hash.
func WarmQueued(hash common.Hash) {
	heavyMu.Lock()
	if r := heavy[hash]; r != nil {
		r.warmQueued = time.Now()
	}
	heavyMu.Unlock()
}

// WarmInProgress reports whether hash has a warm queued or running, and for
// how long.
func WarmInProgress(hash common.Hash) (bool, time.Duration) {
	heavyMu.Lock()
	defer heavyMu.Unlock()

	r := heavy[hash]
	if r == nil || r.warmQueued.IsZero() || !r.warmEnd.IsZero() {
		return false, 0
	}

	return true, time.Since(r.warmQueued)
}

// Deferred logs one builder skip of a heavy tx whose warm is still running.
func Deferred(number uint64, hash common.Hash, waited time.Duration, applied bool) {
	heavyMu.Lock()
	n := 0
	if r := heavy[hash]; r != nil {
		r.deferrals++
		n = r.deferrals
	}
	heavyMu.Unlock()

	log.Info("SHADOW deferred", "number", number, "hash", hash.Hex(), "warmRunningMs", durMs(waited),
		"deferrals", n, "appliedColdAfterMax", applied, "sinceBuildStartMs", ms(time.Now(), currentStart()))
}

// Deferrals returns how many times the builder skipped hash.
func Deferrals(hash common.Hash) int {
	heavyMu.Lock()
	defer heavyMu.Unlock()

	if r := heavy[hash]; r != nil {
		return r.deferrals
	}

	return 0
}

// WarmAdmitLog logs the admission decision for one heavy tx (always computed,
// enforced only when warmadmit=on).
func WarmAdmitLog(hash common.Hash, arm string, admit bool, reason string,
	feeCapGwei, nextBaseGwei, tipGwei float64, nonceGap int64, enforced bool,
) {
	log.Info("SHADOW warm-admit", "hash", hash.Hex(), "arm", arm, "admit", admit, "reason", reason,
		"feeCapGwei", strconv.FormatFloat(feeCapGwei, 'f', 2, 64),
		"nextBaseGwei", strconv.FormatFloat(nextBaseGwei, 'f', 2, 64),
		"tipGwei", strconv.FormatFloat(tipGwei, 'f', 2, 64),
		"nonceGap", nonceGap, "enforced", enforced)
}

// FloodTick logs one second of the local flood test.
func FloodTick(rate, submitted, dropped, done int, meanMs, meanReads float64, ring int) {
	log.Info("SHADOW flood", "rate", rate, "submitted", submitted, "dropped", dropped, "done", done,
		"meanMs", strconv.FormatFloat(meanMs, 'f', 2, 64),
		"meanAccountReads", strconv.FormatFloat(meanReads, 'f', 0, 64), "ring", ring)
}
