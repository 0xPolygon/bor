package eth

import (
	"bytes"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyPermittedBodyRequestsStayNeutral(t *testing.T) {
	backend := newTestBackend(2)
	defer backend.close()
	synctest.Test(t, func(t *testing.T) {
		peer, rw := newObservedReplyTestPeer(t)
		clock := new(mclock.Simulated)
		tracker := peerpolicy.New(clock)
		tracker.Connected(peer.ID())
		peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe(peer.ID(), e) })
		query := GetBlockBodiesPacket{RequestId: 1, GetBlockBodiesRequest: make(GetBlockBodiesRequest, maxBodiesServe)}
		for i := range query.GetBlockBodiesRequest {
			query.GetBlockBodiesRequest[i] = backend.chain.GetBlockByNumber(1).Hash()
		}
		encoded, err := rlp.EncodeToBytes(query)
		require.NoError(t, err)
		requests := 0
		request := func() error {
			if requests >= peerRequestBurst {
				time.Sleep(time.Second / peerRequestRate)
				clock.Run(time.Second / peerRequestRate)
			}
			requests++
			rw.msg = p2p.Msg{Code: GetBlockBodiesMsg, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
			return handleMessage(backend, peer)
		}
		const successfulRequests = 512
		require.Greater(t, successfulRequests*len(encoded), 16<<20)
		for range successfulRequests {
			require.NoError(t, request())
			synctest.Wait()
			require.Len(t, rw.sent, 1)
			msg := <-rw.sent
			require.NoError(t, msg.Discard())
		}
		got := tracker.Snapshot(peer.ID())
		require.Zero(t, got.Risk, "successful repeated requests passed native admission")
		require.Empty(t, got.Windows)
		require.Equal(t, uint64(successfulRequests), got.Contribution.ServedResponses)
		require.Positive(t, got.Contribution.ServedBytes)
		peer.blockReplies.mu.Lock()
		peer.blockReplies.bytes = peerByteBurst / 2
		peer.blockReplies.mu.Unlock()
		for range 3 {
			require.ErrorIs(t, request(), errReplyQueueFull)
		}
		got = tracker.Snapshot(peer.ID())
		require.Equal(t, uint64(20), got.Risk)
		require.Equal(t, uint64(1), got.Windows[peerpolicy.ServingRepetition.String()])
		require.Equal(t, uint64(successfulRequests), got.Contribution.ServedResponses)
	})
}

func TestPeerPolicyNativeBodyByteAllowanceStaysNeutral(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := peerpolicy.New(clock)
	tracker.Connected("peer")
	peer := &Peer{observer: func(e peerpolicy.Evidence) { tracker.Observe("peer", e) }}
	replies := newPeerReplies()
	start := time.Now()
	const responseBytes = 8 << 20
	completed := peer.replyObserver(peerpolicy.Evidence{Family: peerpolicy.BodyReplies, Items: 1, Bytes: responseBytes})
	for second := range 10 {
		budget := peerByteRate
		if second == 0 {
			budget = peerByteBurst
		} else {
			clock.Run(time.Second)
		}
		for range budget / responseBytes {
			require.True(t, replies.limiter.AllowN(start.Add(time.Duration(second)*time.Second), responseBytes))
			completed(responseBytes)
		}
	}
	got := tracker.Snapshot("peer")
	require.Zero(t, got.Risk)
	require.Empty(t, got.Windows)
	require.Equal(t, uint64(26), got.Contribution.ServedResponses)
	require.Equal(t, uint64(208<<20), got.Contribution.ServedBytes)
}

func TestPeerPolicyNativeRequestBurstStaysNeutral(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := peerpolicy.New(clock)
	peer := limitedTestPeer()
	peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe("peer", e) })
	const window = 10 * time.Second
	const capacity = peerRequestBurst + peerRequestRate*int(window/time.Second)
	now := time.Now()
	const refill = time.Second / time.Duration(peerRequestRate)
	codes := []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg}
	for i := range capacity - 1 {
		if i >= peerRequestBurst {
			now = now.Add(refill)
			clock.Run(refill)
		}
		require.NoError(t, peer.checkMessageRate(codes[i%len(codes)], 1, now))
		tracker.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.Requests, Items: 1, Bytes: 1})
	}
	require.Less(t, time.Duration(clock.Now()), window)
	require.Zero(t, tracker.Snapshot("peer").Risk, "permitted requests fit within one aligned window")
	tracker.Observe("profile", peerpolicy.Evidence{Family: peerpolicy.Requests, Items: uint64(capacity)})
	require.Zero(t, tracker.Snapshot("profile").Risk, "profile must cover native rate plus burst")
	require.ErrorIs(t, peer.checkMessageRate(GetBlockBodiesMsg, 1, now), ErrPeerRateLimit)
	peer.observeRateLimit(p2p.Msg{Code: GetBlockBodiesMsg, Size: 1})
	got := tracker.Snapshot("peer")
	require.Equal(t, uint64(20), got.Risk)
	require.Equal(t, uint64(1), got.Windows[peerpolicy.RateLimited.String()])
	require.Zero(t, got.Windows[peerpolicy.RequestVolume.String()])
}
