//go:build lls_shadow

package shadowtrace

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// The late-tx rule may only hold back a tx whose warm is queued or running,
// never one that was not warmed or whose warm already ended.
func TestWarmInProgressLifecycle(t *testing.T) {
	SetEnabledForTest(true)

	h := common.HexToHash("0x05")
	h[31] = 2 // arm A

	if running, _ := WarmInProgress(h); running {
		t.Fatal("unknown tx must not be in progress")
	}

	HeavySeen(h, time.Now(), 9_000_000, "f", "t", "s")

	if running, _ := WarmInProgress(h); running {
		t.Fatal("seen but not queued must not be in progress")
	}

	WarmQueued(h)

	running, waited := WarmInProgress(h)
	if !running || waited < 0 {
		t.Fatalf("queued must be in progress, got %v %v", running, waited)
	}

	start := WarmStart(h)
	if running, _ := WarmInProgress(h); !running {
		t.Fatal("started must be in progress")
	}

	Deferred(1, h, time.Millisecond, false)
	Deferred(1, h, 2*time.Millisecond, false)

	if got := Deferrals(h); got != 2 {
		t.Fatalf("deferrals = %d, want 2", got)
	}

	WarmEnd(h, start, 1, "completed", 1, 0)

	if running, _ := WarmInProgress(h); running {
		t.Fatal("ended warm must not be in progress")
	}
}

func TestP5TogglesDefaultOff(t *testing.T) {
	SetEnabledForTest(true)

	SetToggleForTest("deferheavy", "")
	SetToggleForTest("warmadmit", "")
	SetToggleForTest("flood", "")
	SetToggleForTest("defermaxms", "")

	if DeferHeavy() || WarmAdmit() || FloodRate() != 0 {
		t.Fatal("P5 toggles must default to off")
	}

	if DeferMax() != 800*time.Millisecond {
		t.Fatalf("default defer max = %v", DeferMax())
	}

	SetToggleForTest("deferheavy", "on")
	SetToggleForTest("flood", "40")
	SetToggleForTest("defermaxms", "300")

	if !DeferHeavy() || FloodRate() != 40 || DeferMax() != 300*time.Millisecond {
		t.Fatal("P5 toggles not read")
	}
}
