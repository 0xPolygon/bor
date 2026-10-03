package p2p

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"
)

func TestBulkBufferWriteHeadroom(t *testing.T) {
	for _, scope := range []string{"peer", "process"} {
		t.Run(scope, func(t *testing.T) {
			budget := testBulkBudget(4, 4)
			if scope == "peer" {
				budget.writePeer = semaphore.NewWeighted(2)
			} else {
				budget.writeGlobal = semaphore.NewWeighted(2)
			}
			require.True(t, budget.tryAcquireWrite(2))
			require.False(t, budget.tryAcquireWrite(1))
			require.True(t, budget.tryAcquire(2), "writes must leave receive capacity")
			budget.release(2)
			budget.releaseWrite(2)
			assertBulkBudgetAvailable(t, budget, 2)
		})
	}
}

func TestBulkBufferWriteAdmissionRollback(t *testing.T) {
	for _, scope := range []string{"peer", "global"} {
		t.Run(scope, func(t *testing.T) {
			budget := testBulkBudget(4, 4)
			occupied := budget.peer
			if scope == "global" {
				occupied = budget.global
			}
			require.True(t, occupied.TryAcquire(4))
			require.False(t, budget.tryAcquireWrite(4))
			occupied.Release(4)
			assertBulkBudgetAvailable(t, budget, 4)
		})
	}
}

func TestBulkBufferDefaultWriteLimits(t *testing.T) {
	first, second := newBulkBufferBudget(), newBulkBufferBudget()
	require.Same(t, first.writeGlobal, second.writeGlobal)
	require.NotSame(t, first.writePeer, second.writePeer)
	require.True(t, first.writePeer.TryAcquire(bulkPeerBufferLimit/2))
	require.False(t, first.writePeer.TryAcquire(1))
	first.writePeer.Release(bulkPeerBufferLimit / 2)
	require.True(t, first.writeGlobal.TryAcquire(bulkProcessBufferLimit/2))
	require.False(t, second.writeGlobal.TryAcquire(1))
	first.writeGlobal.Release(bulkProcessBufferLimit / 2)
}
