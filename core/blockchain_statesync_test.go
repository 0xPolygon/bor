package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestGetStateSyncLockedWithQueuedWriter(t *testing.T) {
	_, _, chain, err := newCanonical(ethash.NewFaker(), 0, true, rawdb.HashScheme)
	require.NoError(t, err)
	defer chain.Stop()

	data := &types.StateSyncData{ID: 99}
	chain.SetStateSync([]*types.StateSyncData{data})
	require.Equal(t, []*types.StateSyncData{data}, chain.GetStateSync())

	chain.stateSyncMu.RLock()
	lockHeld := true
	defer func() {
		if lockHeld {
			chain.stateSyncMu.RUnlock()
		}
	}()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		chain.SetStateSync(nil)
	}()
	require.Eventually(t, func() bool {
		if chain.stateSyncMu.TryRLock() {
			chain.stateSyncMu.RUnlock()
			return false
		}
		return true
	}, 5*time.Second, time.Millisecond, "writer did not wait for state sync lock")

	readDone := make(chan []*types.StateSyncData, 1)
	go func() { readDone <- chain.getStateSyncLocked() }()
	select {
	case got := <-readDone:
		require.Equal(t, []*types.StateSyncData{data}, got)
	case <-time.After(5 * time.Second):
		chain.stateSyncMu.RUnlock()
		lockHeld = false
		<-writerDone
		t.Fatal("lock-held state sync read blocked behind a queued writer")
	}
	chain.stateSyncMu.RUnlock()
	lockHeld = false
	<-writerDone
}
