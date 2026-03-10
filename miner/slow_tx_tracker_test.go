package miner

import (
	"math/big"
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

func TestSlowTxTopTrackerSnapshotAndReset(t *testing.T) {
	t.Parallel()

	tracker := newSlowTxTopTracker()
	tracker.Add(txTimingEntry{duration: 4 * time.Millisecond})
	tracker.Add(txTimingEntry{duration: 9 * time.Millisecond})

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
