package wit2test

import (
	"time"

	"github.com/ethereum/go-ethereum/log"
)

// Stamp emits a tagged INFO log used by the kurtosis log scraper.
// No-op unless WIT2_TEST_ENABLED=1.
func Stamp(stage string, kvs ...any) {
	if !Enabled() {
		return
	}
	args := append([]any{"stage", stage, "ts_ns", time.Now().UnixNano()}, kvs...)
	log.Info("[WIT2-TEST]", args...)
}

// MaybeSleep injects WIT2_TEST_IMPORT_DELAY_MS at a named site.
// Used to amplify the per-hop import gate without heavy tx loads.
func MaybeSleep(site string) {
	if !Enabled() {
		return
	}
	d := Get().ImportDelayMs
	if d <= 0 {
		return
	}
	time.Sleep(time.Duration(d) * time.Millisecond)
	log.Info("[WIT2-TEST]", "stage", "DELAY_INJECTED", "site", site, "delay_ms", d)
}
