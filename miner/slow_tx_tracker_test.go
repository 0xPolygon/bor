package miner

import (
	"math/big"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
)

func TestSlowTxTopTrackerKeepsTop10(t *testing.T) {
	t.Parallel()

	tracker := newSlowTxTopTracker()

	for i := 1; i <= 30; i++ {
		tracker.Add(txTimingEntry{
			hash:     common.BigToHash(big.NewInt(int64(i))),
			duration: time.Duration(i) * time.Millisecond,
		})
	}

	got := tracker.SnapshotAndReset()
	require.Len(t, got, slowTxTopKSize)

	for i := 0; i < slowTxTopKSize; i++ {
		expectedDuration := time.Duration(30-i) * time.Millisecond
		require.Equal(t, expectedDuration, got[i].duration)
	}
}

func TestSlowTxTopTrackerRandomOrder(t *testing.T) {
	t.Parallel()

	tracker := newSlowTxTopTracker()

	// Insert 30 entries in random order and verify top 10 are still correct.
	durations := make([]int, 30)
	for i := range durations {
		durations[i] = i + 1
	}

	rng := rand.New(rand.NewSource(42))
	rng.Shuffle(len(durations), func(i, j int) { durations[i], durations[j] = durations[j], durations[i] })

	for _, d := range durations {
		tracker.Add(txTimingEntry{
			hash:     common.BigToHash(big.NewInt(int64(d))),
			duration: time.Duration(d) * time.Millisecond,
		})
	}

	got := tracker.SnapshotAndReset()
	require.Len(t, got, slowTxTopKSize)

	for i := 0; i < slowTxTopKSize; i++ {
		expectedDuration := time.Duration(30-i) * time.Millisecond
		require.Equal(t, expectedDuration, got[i].duration)
	}
}

func TestSlowTxTopTrackerSnapshotAndReset(t *testing.T) {
	t.Parallel()

	tracker := newSlowTxTopTracker()
	tracker.Add(txTimingEntry{duration: 4 * time.Millisecond, gasUsed: 21_000})
	tracker.Add(txTimingEntry{duration: 9 * time.Millisecond, gasUsed: 21_000, prefetched: true})

	first := tracker.SnapshotAndReset()
	require.Len(t, first, 2)
	require.Equal(t, 9*time.Millisecond, first[0].duration)
	require.Equal(t, 4*time.Millisecond, first[1].duration)

	empty := tracker.SnapshotAndReset()
	require.Nil(t, empty)

	tracker.Add(txTimingEntry{duration: 7 * time.Millisecond})
	afterReset := tracker.SnapshotAndReset()
	require.Len(t, afterReset, 1)
	require.Equal(t, 7*time.Millisecond, afterReset[0].duration)
}

func TestFormatSlowTxsAnnotatesPrefetchedAndMGasPerSecond(t *testing.T) {
	t.Parallel()

	// 21,000 gas in 100µs = 210 MGas/s with integer math: 21000*1000/100000 = 210.
	entries := []txTimingEntry{
		{
			hash:       common.BigToHash(big.NewInt(1)),
			duration:   100 * time.Microsecond,
			gasUsed:    21_000,
			prefetched: true,
		},
		{
			hash:       common.BigToHash(big.NewInt(2)),
			duration:   500 * time.Microsecond,
			gasUsed:    50_000,
			prefetched: false,
		},
	}

	out := formatSlowTxs(entries)

	require.Contains(t, out, "210 MGas/s")
	require.Contains(t, out, ", prefetched)")
	require.Contains(t, out, "100 MGas/s") // 50000*1000/500000
	require.Contains(t, out, ", not-prefetched)")
	// Both entries should be separated by a single space.
	require.Equal(t, 2, strings.Count(out, "MGas/s"))
}

func TestMGasPerSecondZeroDuration(t *testing.T) {
	t.Parallel()

	e := txTimingEntry{gasUsed: 21_000, duration: 0}
	require.Equal(t, uint64(0), e.mgasPerSecond())
}
