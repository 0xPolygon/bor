package miner

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

// metric snapshots used for delta assertions: the mid-block metrics are global
// registry entries, but recordMidblockBuildWindow is their only writer and every
// other miner test runs with txVisibleAt == nil (metrics disabled), so deltas
// observed inside one test are isolated.
func midblockMetricCounts() (missedBy, during, ready int64) {
	return snapshotMissedByHistogram.Snapshot().Count(),
		candidatesDuringBuildMeter.Snapshot().Count(),
		candidatesReadyEstimateMeter.Snapshot().Count()
}

func legacyTx(nonce uint64) *types.Transaction {
	return types.NewTx(&types.LegacyTx{Nonce: nonce})
}

// TestRecordMidblockBuildWindow exercises the one non-trivial path of the Phase-1
// instrumentation: visibility-map consumption, window attribution, signed miss
// emission against the prior snapshot boundary, and the TTL sweep.
func TestRecordMidblockBuildWindow(t *testing.T) {
	// Sample/meter updates are no-ops while the global metrics switch is off
	// (metrics/sample.go gates Update on metricsEnabled). One-way and safe for
	// the rest of the suite: recording becomes live, behavior is unchanged.
	metrics.Enable()

	now := time.Now()
	var (
		txIncludedEarly = legacyTx(0) // included; visible before the prior snapshot → negative miss
		txIncludedLate  = legacyTx(1) // included; visible after the prior snapshot → positive miss
		txCandidate     = legacyTx(2) // not included; visible inside this build window
		txStale         = legacyTx(3) // not included; older than the TTL → swept
		txRecent        = legacyTx(4) // not included; before the window but fresh → kept
	)

	w := &worker{txVisibleAt: map[common.Hash]time.Time{
		txIncludedEarly.Hash(): now.Add(-4 * time.Second),
		txIncludedLate.Hash():  now.Add(-1 * time.Second),
		txCandidate.Hash():     now.Add(-1200 * time.Millisecond),
		txStale.Hash():         now.Add(-5 * time.Minute),
		txRecent.Hash():        now.Add(-2 * time.Second),
	}}
	w.running.Store(true)
	w.prevSnapshotAt = now.Add(-3 * time.Second)

	// Guarantee a non-zero (and small, ≪ the 1.2 s candidate margin) prefetch p50
	// so the readiness estimate is emitted.
	prefetchDurationTimer.Update(time.Millisecond)

	env := &environment{txs: []*types.Transaction{txIncludedEarly, txIncludedLate}}
	snapshotAt, exhaustionAt := now.Add(-1500*time.Millisecond), now

	missedBy0, during0, ready0 := midblockMetricCounts()
	w.recordMidblockBuildWindow(env, snapshotAt, exhaustionAt)
	missedBy1, during1, ready1 := midblockMetricCounts()

	require.Equal(t, int64(2), missedBy1-missedBy0, "both included txs with known visibility emit a signed miss")
	require.Equal(t, int64(1), during1-during0, "only the in-window non-included tx is a candidate")
	require.Equal(t, int64(1), ready1-ready0, "the candidate is prefetch-ready within the window")

	w.midblockMu.Lock()
	defer w.midblockMu.Unlock()
	require.Equal(t, snapshotAt, w.prevSnapshotAt, "snapshot boundary advances to this build's")
	require.NotContains(t, w.txVisibleAt, txIncludedEarly.Hash(), "included txs are consumed")
	require.NotContains(t, w.txVisibleAt, txIncludedLate.Hash(), "included txs are consumed")
	require.NotContains(t, w.txVisibleAt, txStale.Hash(), "stale entries are swept")
	require.Contains(t, w.txVisibleAt, txCandidate.Hash(), "candidates stay until included or stale")
	require.Contains(t, w.txVisibleAt, txRecent.Hash(), "fresh pre-window entries stay")
}

// TestRecordMidblockBuildWindowDisabled confirms the hook is a strict no-op when
// the instrumentation is off (nil map — metrics disabled) or the node is not
// producing.
func TestRecordMidblockBuildWindowDisabled(t *testing.T) {
	now := time.Now()
	env := &environment{txs: []*types.Transaction{legacyTx(0)}}

	missedBy0, during0, ready0 := midblockMetricCounts()

	// Nil map (metrics disabled at construction).
	w := &worker{}
	w.running.Store(true)
	w.recordMidblockBuildWindow(env, now.Add(-time.Second), now)

	// Not running (non-producer).
	w = &worker{txVisibleAt: map[common.Hash]time.Time{legacyTx(0).Hash(): now}}
	w.recordMidblockBuildWindow(env, now.Add(-time.Second), now)
	require.Len(t, w.txVisibleAt, 1, "non-producer hook must not touch the map")

	missedBy1, during1, ready1 := midblockMetricCounts()
	require.Equal(t, missedBy0, missedBy1)
	require.Equal(t, during0, during1)
	require.Equal(t, ready0, ready1)
}
