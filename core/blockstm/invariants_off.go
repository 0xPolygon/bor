//go:build !invariants

package blockstm

// Invariants build tag — production builds use these zero-cost stubs.
// Build with `-tags invariants` to enable the runtime checks defined in
// invariants_on.go. Inlined by the compiler; no perf cost in prod.

// assertSettleOrder verifies the load-bearing inductive invariant of the
// V2 validation loop: when validateOne(idx) runs, every reexecDone[j] for
// j < idx-1 must be nil, because validateOne(j+1) finalised it on the
// previous iteration. A future "skip earlier validations" optimisation
// that violates this would silently break settle order and split state
// roots — keep the check on in CI.
func (x *v2ExecCtx) assertSettleOrder(reexecDone []chan struct{}, idx int) {}

// assertReexecVisitedExactlyOnce ensures runValidationLoop's drain loop
// observes every dispatched re-execution exactly once. Catches a future
// off-by-one in the drain that would either skip a settle or send twice
// (deadlocking on chSettle's bounded buffer).
func (x *v2ExecCtx) assertReexecVisitedExactlyOnce(reexecDone []chan struct{}) {}
