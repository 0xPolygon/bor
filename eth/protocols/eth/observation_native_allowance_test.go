// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package eth

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/stretchr/testify/require"
)

const policyWindowSeconds = 10

// Provisional; pooled requests have no native rate limiter.
const pooledRequestAllowance = peerRequestRate*policyWindowSeconds + peerRequestBurst

func nativeAllowance(burst, rate int) uint64 {
	return uint64(burst + rate*policyWindowSeconds)
}

func newAllowanceTracker() (*peerpolicy.Tracker, *mclock.Simulated) {
	clock := new(mclock.Simulated)
	return peerpolicy.New(clock), clock
}

func TestNativePermittedBlockGossipStaysNeutral(t *testing.T) {
	tracker, clock := newAllowanceTracker()
	peer, start := limitedTestPeer(), time.Now()
	admitted := 0
	for second := range policyWindowSeconds {
		budget := peerGossipRate
		if second == 0 {
			budget = peerGossipBurst
		} else {
			clock.Run(time.Second)
		}
		now := start.Add(time.Duration(second) * time.Second)
		for range budget {
			require.NoError(t, peer.checkMessageRate(NewBlockMsg, 1, now))
			tracker.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.Blocks, Items: 1, Bytes: 1})
			admitted++
		}
	}
	got := tracker.Snapshot("peer")
	require.Zerof(t, got.Risk, "native limiter admitted %d block messages", admitted)
	require.Empty(t, got.Windows)
}

func TestNativePermittedBlockGossipBytesStayNeutral(t *testing.T) {
	tracker, clock := newAllowanceTracker()
	peer, start := limitedTestPeer(), time.Now()
	const size = 8 << 20
	admitted := 0
	for second := range policyWindowSeconds {
		budget := peerByteRate
		if second == 0 {
			budget = peerByteBurst
		} else {
			clock.Run(time.Second)
		}
		now := start.Add(time.Duration(second) * time.Second)
		for range budget / size {
			require.NoError(t, peer.checkMessageRate(NewBlockMsg, size, now))
			tracker.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.Blocks, Items: 1, Bytes: size})
			admitted += size
		}
	}
	got := tracker.Snapshot("peer")
	require.Zerof(t, got.Risk, "native limiter admitted %d block gossip bytes", admitted)
	require.Empty(t, got.Windows)
}

func TestNativePermittedBlockAnnouncementsStayNeutral(t *testing.T) {
	tracker, clock := newAllowanceTracker()
	peer, start := limitedTestPeer(), time.Now()
	admitted := 0
	for second := range policyWindowSeconds {
		budget := peerHashRate
		if second == 0 {
			budget = peerHashBurst
		} else {
			clock.Run(time.Second)
		}
		now := start.Add(time.Duration(second) * time.Second)
		require.NoError(t, peer.checkMessageRate(NewBlockHashesMsg, 1, now))
		require.NoError(t, peer.checkAnnouncementRate(budget, now))
		tracker.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.BlockAnnouncements, Items: uint64(budget), Bytes: 1})
		admitted += budget
	}
	got := tracker.Snapshot("peer")
	require.Zerof(t, got.Risk, "native limiter admitted %d announced hashes", admitted)
	require.Empty(t, got.Windows)
}

// The previous profile accumulated risk 100 from permitted traffic.
func TestNativeSustainedBlockGossipStaysNeutral(t *testing.T) {
	tracker, clock := newAllowanceTracker()
	peer, start := limitedTestPeer(), time.Now()
	admitted := 0
	for second := range 6 * policyWindowSeconds {
		if second > 0 {
			clock.Run(time.Second)
		}
		now := start.Add(time.Duration(second) * time.Second)
		// The bucket refills while the window boundary is crossed, so the first
		// second of every window carries one message more than the plain rate.
		budget := peerGossipRate
		if second%policyWindowSeconds == 0 {
			budget++
		}
		for range budget {
			require.NoError(t, peer.checkMessageRate(NewBlockMsg, 1, now))
			tracker.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.Blocks, Items: 1, Bytes: 1})
			admitted++
		}
	}
	got := tracker.Snapshot("peer")
	require.Zerof(t, got.Risk, "native limiter admitted %d block messages across six windows", admitted)
	require.Equal(t, "none", got.Action)
}

func TestPeerPolicyVolumeAllowanceBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		family    peerpolicy.Family
		allowance uint64
		bytes     bool
		reason    peerpolicy.Reason
	}{
		{"block gossip", peerpolicy.Blocks, nativeAllowance(peerGossipBurst, peerGossipRate), false, peerpolicy.BlockVolume},
		{"block gossip bytes", peerpolicy.Blocks, nativeAllowance(peerByteBurst, peerByteRate), true, peerpolicy.BlockVolume},
		{"block announcements", peerpolicy.BlockAnnouncements, nativeAllowance(peerHashBurst, peerHashRate), false, peerpolicy.AnnouncementVolume},
		{"block data requests", peerpolicy.Requests, nativeAllowance(peerRequestBurst, peerRequestRate), false, peerpolicy.RequestVolume},
		{"pooled transaction requests", peerpolicy.TransactionRequests, pooledRequestAllowance, false, peerpolicy.RequestVolume},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker, _ := newAllowanceTracker()
			observe := func(amount uint64) {
				evidence := peerpolicy.Evidence{Family: tc.family, Items: 1, Bytes: amount}
				if !tc.bytes {
					evidence.Items, evidence.Bytes = amount, 1
				}
				tracker.Observe("peer", evidence)
			}
			observe(tc.allowance)
			got := tracker.Snapshot("peer")
			require.Zero(t, got.Risk, "the exact native allowance is not abuse")
			require.Empty(t, got.Windows)

			observe(1)
			got = tracker.Snapshot("peer")
			require.Equal(t, uint64(20), got.Risk)
			require.Equal(t, uint64(1), got.Windows[tc.reason.String()])
		})
	}
}

// Combining allowed block and pooled requests must not share a scoring budget.
func TestPooledTransactionRequestsHaveTheirOwnBudget(t *testing.T) {
	require.Equal(t, peerpolicy.TransactionRequests, messageFamily(GetPooledTransactionsMsg))
	tracker, clock := newAllowanceTracker()
	tracker.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.Requests, Items: nativeAllowance(peerRequestBurst, peerRequestRate), Bytes: 1})
	tracker.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.TransactionRequests, Items: pooledRequestAllowance, Bytes: 1})
	require.Zero(t, tracker.Snapshot("peer").Risk, "pooled transaction requests shared the block-data request budget")

	// Sustained excess accumulates across windows like any other traffic reason.
	for window := uint64(1); window <= 3; window++ {
		tracker.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.TransactionRequests, Items: pooledRequestAllowance + 1, Bytes: 1})
		got := tracker.Snapshot("peer")
		require.Equal(t, window*20, got.Risk)
		require.Equal(t, window, got.Windows[peerpolicy.RequestVolume.String()])
		clock.Run(policyWindowSeconds * time.Second)
	}
}
