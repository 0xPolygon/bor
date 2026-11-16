package blockstm

import "sync/atomic"

// WriteBudget caps the journal entries and tracked state reads that all
// incarnations of one block's transactions may append in total. Each speculative incarnation runs with
// its tx's full gas limit, so an incarnation that reads a stale value can do
// far more work than the canonical execution the block paid for.
type WriteBudget struct {
	remaining atomic.Int64
	exhausted atomic.Bool
	onExhaust func()
}

// NewWriteBudget returns a budget of limit entries. onExhaust runs once, on
// the first charge that overdraws it, from whichever goroutine charged.
func NewWriteBudget(limit int64, onExhaust func()) *WriteBudget {
	b := &WriteBudget{onExhaust: onExhaust}
	b.remaining.Store(limit)
	return b
}

// Charge consumes n entries from the budget.
func (b *WriteBudget) Charge(n int64) {
	if b.remaining.Add(-n) >= 0 {
		return
	}
	if b.exhausted.CompareAndSwap(false, true) {
		b.onExhaust()
	}
}

// Exhausted reports whether the budget has been overdrawn.
func (b *WriteBudget) Exhausted() bool {
	return b.exhausted.Load()
}

// Remaining returns the entries left; it is negative once overdrawn.
func (b *WriteBudget) Remaining() int64 {
	return b.remaining.Load()
}
