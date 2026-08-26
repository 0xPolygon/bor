//go:build invariants

package blockstm

import (
	"strings"
	"testing"
)

// TestInvariant_SettleOrder_FiresOnViolation directly invokes
// assertSettleOrder with a synthesised reexecDone slice that violates
// the induction (a non-nil channel at j < idx-1) and confirms the
// `-tags invariants` build panics with the expected message. Without
// this, the assertion would silently regress to a stub.
func TestInvariant_SettleOrder_FiresOnViolation(t *testing.T) {
	x := &v2ExecCtx{}
	reexecDone := make([]chan struct{}, 5)
	reexecDone[1] = make(chan struct{}) // violator: j=1 < idx-1=3

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected assertSettleOrder to panic on a non-nil reexecDone[1] when validating idx=4")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "settle-order invariant") {
			t.Fatalf("unexpected panic payload: %v", r)
		}
	}()
	x.assertSettleOrder(reexecDone, 4)
}

// TestInvariant_DrainExactlyOnce_FiresOnViolation pins the drain
// invariant: any reexecDone[i] left non-nil after the drain loop is a
// state-loss bug (tx settled twice or never).
func TestInvariant_DrainExactlyOnce_FiresOnViolation(t *testing.T) {
	x := &v2ExecCtx{}
	reexecDone := make([]chan struct{}, 3)
	reexecDone[2] = make(chan struct{}) // never finished

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected assertReexecVisitedExactlyOnce to panic on a non-nil entry after drain")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "drain invariant") {
			t.Fatalf("unexpected panic payload: %v", r)
		}
	}()
	x.assertReexecVisitedExactlyOnce(reexecDone)
}
