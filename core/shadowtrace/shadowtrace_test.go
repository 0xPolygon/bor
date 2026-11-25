//go:build lls_shadow

package shadowtrace

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func TestArmIsFixedByLastHashBit(t *testing.T) {
	var even, odd common.Hash
	odd[31] = 1

	if Arm(even) != "A" || Arm(odd) != "B" {
		t.Fatalf("arms: even=%s odd=%s", Arm(even), Arm(odd))
	}
}

func TestHeavyRetiredWhenIncluded(t *testing.T) {
	SetEnabledForTest(true)
	defer SetEnabledForTest(false)

	h := common.HexToHash("0x02")
	if arm := HeavySeen(h, time.Now(), 10_000_000, "f", "t", "0x765e827f"); arm != "A" {
		t.Fatalf("arm %s", arm)
	}

	start := WarmStart(h)
	WarmEnd(h, start, 1, OutcomeCompleted, 9_000_000, time.Millisecond)

	heavyMu.Lock()
	r := heavy[h]
	heavyMu.Unlock()
	if r == nil || r.warmOutcome != OutcomeCompleted || r.warmGas != 9_000_000 {
		t.Fatalf("warm not recorded: %+v", r)
	}

	HeadSeen(2, time.Now(), []common.Hash{h}, 0, 1)

	heavyMu.Lock()
	_, still := heavy[h]
	heavyMu.Unlock()
	if still {
		t.Fatal("included heavy tx must be retired")
	}
}

func TestRunListAndDoneBeforeApply(t *testing.T) {
	SetEnabledForTest(true)
	defer SetEnabledForTest(false)

	const n = 77
	h := common.HexToHash("0x03")

	BuildStart(n, time.Now(), time.Time{}, time.Time{})
	PassStart(n)
	Sent(n, h, "plan")

	s1 := RunStart(n, h)
	RunEnd(n, h, s1, OutcomeInterrupted, 0, true) // gap-8 run: does not count
	applyAt := time.Now()
	s2 := RunStart(n, h)
	RunEnd(n, h, s2, OutcomeCompleted, 1, true) // ended after the apply started

	Applied(n, h, ApplyInfo{Start: applyAt, Dur: time.Millisecond, Result: "ok"})

	b := get(n)
	b.mu.Lock()
	tt := b.txs[h]
	runs, gap8 := len(tt.runList), b.gap8
	b.mu.Unlock()

	if runs != 2 || gap8 != 1 {
		t.Fatalf("runs=%d gap8=%d", runs, gap8)
	}

	for _, r := range tt.runList {
		if r.marked && r.outcome != OutcomeInterrupted && !r.end.After(applyAt) {
			t.Fatal("no run ended before the apply, so doneBeforeApply must be false")
		}
	}
}
