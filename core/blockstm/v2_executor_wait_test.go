package blockstm

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// TestPredecessor_SameToChain returns the previously-seen task with the
// same "To" address once the chain has grown to minChainSize occurrences.
func TestPredecessor_SameToChain(t *testing.T) {
	c := &chainState{lastTo: map[common.Address]int{}, lastConflict: -1}
	to := common.Address{1}
	counts := map[common.Address]int{to: 3}

	// i=0, first occurrence — no predecessor.
	if got := c.predecessor(&to, 0, counts, nil, 2); got != -1 {
		t.Fatalf("first tx: got %d, want -1", got)
	}
	// i=1, same To — predecessor is 0.
	if got := c.predecessor(&to, 1, counts, nil, 2); got != 0 {
		t.Fatalf("same-to chain: got %d, want 0", got)
	}
	// i=2, same To — predecessor is 1.
	if got := c.predecessor(&to, 2, counts, nil, 2); got != 1 {
		t.Fatalf("same-to chain: got %d, want 1", got)
	}
}

// TestPredecessor_BelowMinChain does NOT chain when the To address appears
// fewer than minChainSize times.
func TestPredecessor_BelowMinChain(t *testing.T) {
	c := &chainState{lastTo: map[common.Address]int{}, lastConflict: -1}
	to := common.Address{1}
	counts := map[common.Address]int{to: 1}

	if got := c.predecessor(&to, 0, counts, nil, 2); got != -1 {
		t.Fatalf("first tx: got %d, want -1", got)
	}
	if got := c.predecessor(&to, 1, counts, nil, 2); got != -1 {
		t.Fatalf("below minChainSize: got %d, want -1", got)
	}
}

// TestPredecessor_ConflictFallback chains through the conflictAddrs path
// when no same-To predecessor applies.
func TestPredecessor_ConflictFallback(t *testing.T) {
	c := &chainState{lastTo: map[common.Address]int{}, lastConflict: -1}
	to1 := common.Address{1}
	to2 := common.Address{2}
	// No same-To chain (counts all 1), both marked conflict.
	counts := map[common.Address]int{to1: 1, to2: 1}
	conflict := map[common.Address]bool{to1: true, to2: true}

	if got := c.predecessor(&to1, 0, counts, conflict, 2); got != -1 {
		t.Fatalf("first conflict: got %d, want -1", got)
	}
	// Different To, but still a conflict address — falls back to lastConflict.
	if got := c.predecessor(&to2, 1, counts, conflict, 2); got != 0 {
		t.Fatalf("conflict fallback: got %d, want 0", got)
	}
}

// TestPredecessor_NilTo returns -1 for contract-creation txs (to == nil).
func TestPredecessor_NilTo(t *testing.T) {
	c := &chainState{lastTo: map[common.Address]int{}, lastConflict: -1}
	if got := c.predecessor(nil, 0, nil, nil, 2); got != -1 {
		t.Fatalf("nil To: got %d, want -1", got)
	}
}

// TestWaitForTx_FinalizedFastPath returns immediately when the target is
// already finalized.
func TestWaitForTx_FinalizedFastPath(t *testing.T) {
	x := &v2ExecCtx{n: 3}
	x.finalized = make([]atomic.Bool, 3)
	x.execDone = make([]chan struct{}, 3)
	for i := range x.execDone {
		x.execDone[i] = make(chan struct{})
	}
	x.finalized[1].Store(true)

	done := make(chan struct{})
	go func() { x.waitForTx(1); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("waitForTx on finalized tx should return immediately")
	}
}

// TestWaitForTx_BlocksThenReturns blocks until execDone closes.
func TestWaitForTx_BlocksThenReturns(t *testing.T) {
	x := &v2ExecCtx{n: 3}
	x.finalized = make([]atomic.Bool, 3)
	x.execDone = make([]chan struct{}, 3)
	for i := range x.execDone {
		x.execDone[i] = make(chan struct{})
	}

	done := make(chan struct{})
	go func() { x.waitForTx(1); close(done) }()
	select {
	case <-done:
		t.Fatalf("waitForTx returned before execDone closed")
	case <-time.After(20 * time.Millisecond):
	}
	close(x.execDone[1])
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("waitForTx did not unblock after execDone")
	}
}

// TestWaitForTx_OutOfRange returns immediately for negative or out-of-bounds
// writerIdx — defensive guard for settlement edge cases.
func TestWaitForTx_OutOfRange(t *testing.T) {
	x := &v2ExecCtx{n: 3}
	x.finalized = make([]atomic.Bool, 3)
	x.execDone = make([]chan struct{}, 3)
	for i := range x.execDone {
		x.execDone[i] = make(chan struct{})
	}
	x.waitForTx(-1)
	x.waitForTx(5) // must not panic
}

// TestWaitForFinal_FinalizedBeforeExec returns as soon as finalized is set,
// even without waiting for execDone or completionCh.
func TestWaitForFinal_FinalizedBeforeExec(t *testing.T) {
	x := &v2ExecCtx{n: 3}
	x.finalized = make([]atomic.Bool, 3)
	x.execDone = make([]chan struct{}, 3)
	x.completionCh = make([]chan struct{}, 3)
	for i := range x.execDone {
		x.execDone[i] = make(chan struct{})
		x.completionCh[i] = make(chan struct{})
	}
	x.finalized[1].Store(true)

	done := make(chan struct{})
	go func() { x.waitForFinal(1); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("waitForFinal must return immediately when finalized")
	}
}

// TestWaitForFinal_ExecDoneThenFinalized: execDone closes, then finalized
// flips before waitForFinal reaches the completionCh receive — skip it.
func TestWaitForFinal_ExecDoneThenFinalized(t *testing.T) {
	x := &v2ExecCtx{n: 3}
	x.finalized = make([]atomic.Bool, 3)
	x.execDone = make([]chan struct{}, 3)
	x.completionCh = make([]chan struct{}, 3)
	for i := range x.execDone {
		x.execDone[i] = make(chan struct{})
		x.completionCh[i] = make(chan struct{})
	}

	done := make(chan struct{})
	go func() { x.waitForFinal(1); close(done) }()

	// Signal execDone and mark finalized BEFORE the goroutine reads
	// finalized the second time. The sleep is for ordering scheduling.
	x.finalized[1].Store(true)
	close(x.execDone[1])
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("waitForFinal stuck after execDone + finalized")
	}
}

// TestWaitForFinal_FullPath waits through both execDone and completionCh.
func TestWaitForFinal_FullPath(t *testing.T) {
	x := &v2ExecCtx{n: 3}
	x.finalized = make([]atomic.Bool, 3)
	x.execDone = make([]chan struct{}, 3)
	x.completionCh = make([]chan struct{}, 3)
	for i := range x.execDone {
		x.execDone[i] = make(chan struct{})
		x.completionCh[i] = make(chan struct{})
	}

	done := make(chan struct{})
	go func() { x.waitForFinal(1); close(done) }()
	// Close execDone: loop continues to the second check, finalized still
	// false, so it must block on completionCh.
	close(x.execDone[1])
	select {
	case <-done:
		t.Fatalf("waitForFinal returned before completionCh closed")
	case <-time.After(20 * time.Millisecond):
	}
	close(x.completionCh[1])
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("waitForFinal did not unblock after completionCh")
	}
}

// TestWaitForFinal_OutOfRange is a defensive guard mirror of waitForTx.
func TestWaitForFinal_OutOfRange(t *testing.T) {
	x := &v2ExecCtx{n: 2}
	x.finalized = make([]atomic.Bool, 2)
	x.execDone = make([]chan struct{}, 2)
	x.completionCh = make([]chan struct{}, 2)
	x.waitForFinal(-1)
	x.waitForFinal(42) // must not panic
}
