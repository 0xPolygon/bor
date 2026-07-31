package miner

import (
	"testing"
	"time"
)

// TestDecideVeblopFallback covers the producer stall-fallback decision logic in
// newWorkLoop's veblop-timer branch.
//
// Regression context (mainnet incident): a block producer froze because a build
// wedged inside commitWork left pendingWorkBlock pinned at currentBlock+1 with no
// sealing task (pendingTasks empty). The old guard skipped recovery on
// `pendingWorkBlock == nextBlock` alone, so the producer never resubmitted work
// and only a restart recovered.
//
// The fix must ALSO not interrupt a build that is merely slow-but-live (same
// pendingWorkBlock==next, no-task state, but progressing). The two are told apart
// by how long the build has been outstanding (pendingWorkAge) — never by
// block-timestamp age (chainAge), which is meaningless when the head carries
// an old timestamp (e.g. genesis).
func TestDecideVeblopFallback(t *testing.T) {
	const (
		current    = uint64(100)
		next       = current + 1
		timeout    = time.Second // veblop timeout (block period)
		stallAfter = 3 * timeout
		bigAge     = 1_000_000 * time.Second // genesis-like huge chain age
	)

	tests := []struct {
		name             string
		pendingWorkBlock uint64
		hasPendingTasks  bool
		chainAge         time.Duration
		veblopTimeout    time.Duration
		pendingWorkAge   time.Duration
		stallThreshold   time.Duration
		want             veblopFallbackDecision
	}{
		{
			// THE BUG: build wedged in commitWork pinned pendingWorkBlock=next,
			// produced no task, and has been outstanding well past a normal build.
			name:             "wedged build, outstanding past threshold -> recommit",
			pendingWorkBlock: next,
			hasPendingTasks:  false,
			chainAge:         bigAge,
			veblopTimeout:    timeout,
			pendingWorkAge:   5 * timeout,
			stallThreshold:   stallAfter,
			want:             veblopRecommit,
		},
		{
			// THE REGRESSION GUARD: at genesis chainAge is huge, but the build was
			// just submitted and is still in progress — must NOT be interrupted.
			name:             "in-progress build at genesis (huge chainAge, fresh build) -> wait",
			pendingWorkBlock: next,
			hasPendingTasks:  false,
			chainAge:         bigAge,
			veblopTimeout:    timeout,
			pendingWorkAge:   0,
			stallThreshold:   stallAfter,
			want:             veblopWait,
		},
		{
			name:             "task in flight for next block -> skip",
			pendingWorkBlock: next,
			hasPendingTasks:  true,
			chainAge:         bigAge,
			veblopTimeout:    timeout,
			pendingWorkAge:   10 * timeout,
			stallThreshold:   stallAfter,
			want:             veblopSkip,
		},
		{
			// Nothing claimed for next block and chain stale -> resubmit.
			name:             "no work claimed, stale -> recommit",
			pendingWorkBlock: 0,
			hasPendingTasks:  false,
			chainAge:         timeout,
			veblopTimeout:    timeout,
			pendingWorkAge:   0,
			stallThreshold:   stallAfter,
			want:             veblopRecommit,
		},
		{
			// Nothing claimed but chain still fresh -> wait.
			name:             "no work claimed, fresh -> wait",
			pendingWorkBlock: 0,
			hasPendingTasks:  false,
			chainAge:         timeout - time.Millisecond,
			veblopTimeout:    timeout,
			pendingWorkAge:   0,
			stallThreshold:   stallAfter,
			want:             veblopWait,
		},
		{
			// A task is sealing and pendingWorkBlock was already cleared by
			// commitWork's deferred reset -> never resubmit on top of it.
			name:             "task sealing, pendingWorkBlock cleared -> skip",
			pendingWorkBlock: 0,
			hasPendingTasks:  true,
			chainAge:         bigAge,
			veblopTimeout:    timeout,
			pendingWorkAge:   0,
			stallThreshold:   stallAfter,
			want:             veblopSkip,
		},
		{
			name:             "wedge boundary: outstanding exactly at threshold -> recommit",
			pendingWorkBlock: next,
			hasPendingTasks:  false,
			chainAge:         bigAge,
			veblopTimeout:    timeout,
			pendingWorkAge:   stallAfter,
			want:             veblopRecommit,
			stallThreshold:   stallAfter,
		},
		{
			name:             "wedge boundary: just below threshold -> wait",
			pendingWorkBlock: next,
			hasPendingTasks:  false,
			chainAge:         bigAge,
			veblopTimeout:    timeout,
			pendingWorkAge:   stallAfter - time.Millisecond,
			stallThreshold:   stallAfter,
			want:             veblopWait,
		},
		{
			// SUB-SECOND PRECISION GUARD (mainnet runs a 1.5s block time): the
			// threshold is 3x1.5s = 4.5s. A build outstanding 4s is still live and
			// must wait; whole-second truncation would have set the threshold to
			// 3x1s = 3s and wrongly interrupted it at 4s.
			name:             "1.5s block time: 4s-old build is below 4.5s threshold -> wait",
			pendingWorkBlock: next,
			hasPendingTasks:  false,
			chainAge:         bigAge,
			veblopTimeout:    1500 * time.Millisecond,
			pendingWorkAge:   4 * time.Second,
			stallThreshold:   3 * 1500 * time.Millisecond,
			want:             veblopWait,
		},
		{
			// Same 1.5s block time: once the build passes 4.5s it is wedged.
			name:             "1.5s block time: 5s-old build is past 4.5s threshold -> recommit",
			pendingWorkBlock: next,
			hasPendingTasks:  false,
			chainAge:         bigAge,
			veblopTimeout:    1500 * time.Millisecond,
			pendingWorkAge:   5 * time.Second,
			stallThreshold:   3 * 1500 * time.Millisecond,
			want:             veblopRecommit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideVeblopFallback(tt.pendingWorkBlock, next, tt.hasPendingTasks, tt.chainAge, tt.veblopTimeout, tt.pendingWorkAge, tt.stallThreshold)
			if got != tt.want {
				t.Fatalf("decideVeblopFallback(pwb=%d, next=%d, hasTasks=%v, chainAge=%v, timeout=%v, workAge=%v, stall=%v) = %d, want %d",
					tt.pendingWorkBlock, next, tt.hasPendingTasks, tt.chainAge, tt.veblopTimeout, tt.pendingWorkAge, tt.stallThreshold, got, tt.want)
			}
		})
	}
}
