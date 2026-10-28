//go:build integration

package eth

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/stretchr/testify/require"
)

func TestPeerContributionReadinessWithoutAdminQueries(t *testing.T) {
	clock := new(mclock.Simulated)
	h := newContributionTestHandler(t, clock, false).handler
	h.synced.Store(true)
	h.snapSync.Store(false)
	h.peerPolicy.Connected("peer")
	h.peerPolicy.SetReady(true)
	clock.Run(10 * time.Minute)
	require.Equal(t, "idle", h.peerPolicy.Snapshot("peer").Contribution.Status)
	// No new heads or admin reads refresh this stale chain's readiness.
	require.Eventually(t, func() bool {
		return h.peerPolicy.Snapshot("peer").Contribution.Status == "unknown"
	}, 2*time.Minute, 100*time.Millisecond)
}
