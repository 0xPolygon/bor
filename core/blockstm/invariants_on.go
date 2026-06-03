//go:build invariants

package blockstm

import "fmt"

// assertSettleOrder is the runtime check for the V2 validation loop's
// settle-order invariant. See invariants_off.go for the contract.
func (x *v2ExecCtx) assertSettleOrder(reexecDone []chan struct{}, idx int) {
	for j := 0; j < idx-1; j++ {
		if reexecDone[j] != nil {
			panic(fmt.Sprintf("v2 settle-order invariant violated: reexecDone[%d] still non-nil while validating idx=%d (validateOne(j+1) must finishReexec(j))", j, idx))
		}
	}
}

// assertReexecVisitedExactlyOnce panics if runValidationLoop's drain
// loop leaves any reexecDone[i] != nil — that would mean some
// re-execution finished but its tx was never settled, an invisible
// state-loss bug.
func (x *v2ExecCtx) assertReexecVisitedExactlyOnce(reexecDone []chan struct{}) {
	for i, ch := range reexecDone {
		if ch != nil {
			panic(fmt.Sprintf("v2 drain invariant violated: reexecDone[%d] still non-nil after drain — tx settled twice or never", i))
		}
	}
}
