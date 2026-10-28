package p2p

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func openTestBulkChannel(t *testing.T, left, right *testBulkServer, lp, rp *Peer, channel string) (MsgReadWriter, MsgReadWriter) {
	t.Helper()
	opened := make(chan bulkChannelResult, 1)
	go func() {
		rw, err := left.bulk.OpenChannelContext(t.Context(), lp, channel)
		opened <- bulkChannelResult{rw: rw, err: err}
	}()
	rrw, err := right.bulk.OpenChannelContext(t.Context(), rp, channel)
	require.NoError(t, err)
	result := <-opened
	require.NoError(t, result.err)
	return result.rw, rrw
}

func TestBulkChannelWaitsForReceiver(t *testing.T) {
	left, right, lp, rp := channelReadyTestServers(t)
	opened := make(chan bulkChannelResult, 1)
	go func() {
		rw, err := left.bulk.OpenChannelContext(t.Context(), lp, "eth-bulk")
		opened <- bulkChannelResult{rw: rw, err: err}
	}()
	session := right.bulk.peerSession(rp.Node(), rp)
	require.Eventually(t, func() bool {
		_, ok := session.getChannel("eth-bulk")
		return ok
	}, time.Second, time.Millisecond)
	require.Empty(t, opened, "the opener must wait for the receiver to claim the stream")
	inbound, err := right.bulk.OpenChannelContext(t.Context(), rp, "eth-bulk")
	require.NoError(t, err)
	result := <-opened
	require.NoError(t, result.err)
	for _, pair := range [][2]MsgReadWriter{{result.rw, inbound}, {inbound, result.rw}} {
		sent := make(chan error, 1)
		go func() { sent <- SendItems(pair[0], 5, uint64(42)) }()
		require.NoError(t, ExpectMsg(pair[1], 5, []uint64{42}))
		require.NoError(t, <-sent)
	}
}

func TestBulkChannelAfterReceiverTimeout(t *testing.T) {
	left, right, lp, rp := channelReadyTestServers(t)
	sender := left.bulk.peerSession(lp.Node(), lp)
	_, err := sender.ensureConn(t.Context())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = right.bulk.OpenChannelContext(ctx, rp, "eth-bulk")
	require.Error(t, err)
	receiver := right.bulk.peerSession(rp.Node(), rp)
	ctx, cancelLate := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelLate()
	rw, err := left.bulk.OpenChannelContext(ctx, lp, "eth-bulk")
	require.Error(t, err)
	require.Nil(t, rw, "a late stream without a receiver must never become a live outgoing lane")
	_, exists := sender.getChannel("eth-bulk")
	require.False(t, exists)
	require.Eventually(t, func() bool {
		_, exists := receiver.getChannel("eth-bulk")
		return !exists
	}, time.Second, time.Millisecond)
	openTestBulkChannel(t, left, right, lp, rp, "eth-control")
}

func TestBulkChannelOpenResetAfterCancellation(t *testing.T) {
	session := newHelloTestSession()
	local, remote := net.Pipe()
	t.Cleanup(func() { require.NoError(t, local.Close()) })
	t.Cleanup(func() { require.NoError(t, remote.Close()) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := session.openChannelStream(ctx, nil, local, "eth-bulk")
		result <- err
	}()
	var hello bulkChannelHello
	require.NoError(t, readBulkControl(remote, bulkChannelControlMaxSize, &hello))
	cancel()
	select {
	case <-result:
		t.Fatal("caller cancellation abandoned a hello already received by the peer")
	case <-time.After(20 * time.Millisecond):
	}
	require.NoError(t, remote.Close())
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("closed stream kept waiting for its acknowledgement")
	}
	require.Empty(t, session.channels)
}

func TestBulkChannelProtocolOpenOrder(t *testing.T) {
	left, right, lp, rp := channelReadyTestServers(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	results := make(chan error, 4)
	open := func(server *testBulkServer, peer *Peer, channel string) {
		_, err := server.bulk.OpenChannelContext(ctx, peer, channel)
		results <- err
	}
	go open(left, lp, "eth-bulk")
	receiver := right.bulk.peerSession(rp.Node(), rp)
	require.Eventually(t, func() bool {
		_, exists := receiver.getChannel("eth-bulk")
		return exists
	}, time.Second, time.Millisecond)
	go open(right, rp, "snap-trie")
	require.Eventually(t, func() bool {
		receiver.lock.Lock()
		defer receiver.lock.Unlock()
		return len(receiver.waiters["snap-trie"]) == 1
	}, time.Second, time.Millisecond)
	go open(left, lp, "snap-trie")
	go open(right, rp, "eth-bulk")
	for range 4 {
		require.NoError(t, <-results, "protocols can attach channels in different orders on each node")
	}
}

func TestBulkChannelAcknowledgementValidation(t *testing.T) {
	for _, ack := range []bulkChannelHello{
		{Version: bulkSidecarVersion + 1, Channel: "eth-bulk"},
		{Version: bulkSidecarVersion, Channel: "snap-trie"},
	} {
		t.Run(ack.Channel, func(t *testing.T) {
			session := newHelloTestSession()
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()
			result := make(chan error, 1)
			go func() {
				_, err := session.openChannelStream(t.Context(), nil, local, "eth-bulk")
				result <- err
			}()
			var hello bulkChannelHello
			require.NoError(t, readBulkControl(remote, bulkChannelControlMaxSize, &hello))
			require.NoError(t, writeBulkControl(remote, ack))
			require.ErrorContains(t, <-result, "invalid bulk channel acknowledgement")
			require.Empty(t, session.channels)
		})
	}
}

func TestBulkChannelClaimCompletesAfterCancellation(t *testing.T) {
	session := newHelloTestSession()
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	accepted := make(chan error, 1)
	go func() { accepted <- session.acceptChannel(nil, local) }()
	require.NoError(t, writeBulkControl(remote, bulkChannelHello{Version: bulkSidecarVersion, Channel: "eth-bulk"}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	claimed := make(chan error, 1)
	go func() {
		_, err := session.waitChannel(ctx, nil, "eth-bulk")
		claimed <- err
	}()
	require.Eventually(t, func() bool {
		rw, exists := session.getChannel("eth-bulk")
		if !exists {
			return false
		}
		select {
		case <-rw.(*bulkStreamMsgRW).ready.claimed:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	cancel()
	var ack bulkChannelHello
	require.NoError(t, readBulkControl(remote, bulkChannelControlMaxSize, &ack))
	require.Equal(t, bulkSidecarVersion, ack.Version)
	require.Equal(t, "eth-bulk", ack.Channel)
	select {
	case err := <-claimed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("channel claim did not complete")
	}
	select {
	case err := <-accepted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("acknowledgement write did not complete")
	}
	_, exists := session.getChannel("eth-bulk")
	require.True(t, exists)
}

func TestBulkChannelAbandonedAckPreservesSession(t *testing.T) {
	left, right, lp, rp := channelReadyTestServers(t)
	outbound, inbound := openTestBulkChannel(t, left, right, lp, rp, "eth-bulk")
	sender := left.bulk.peerSession(lp.Node(), lp)
	conn, err := sender.ensureConn(t.Context())
	require.NoError(t, err)
	stream, err := conn.OpenStreamSync(t.Context())
	require.NoError(t, err)
	require.NoError(t, writeBulkControl(stream, bulkChannelHello{Version: bulkSidecarVersion, Channel: "snap-trie"}))
	receiver := right.bulk.peerSession(rp.Node(), rp)
	require.Eventually(t, func() bool {
		_, exists := receiver.getChannel("snap-trie")
		return exists
	}, time.Second, time.Millisecond)
	pending, exists := receiver.getChannel("snap-trie")
	require.True(t, exists)
	stream.CancelRead(quic.StreamErrorCode(bulkSidecarCloseErrorCode))
	stream.CancelWrite(quic.StreamErrorCode(bulkSidecarCloseErrorCode))
	select {
	case <-pending.(*bulkStreamMsgRW).ready.done:
		require.ErrorIs(t, pending.(*bulkStreamMsgRW).ready.err, errBulkChannelAbandoned)
	case <-time.After(time.Second):
		t.Fatal("reset stream kept waiting for a receiver")
	}
	_, exists = receiver.getChannel("snap-trie")
	require.False(t, exists)
	openTestBulkChannel(t, left, right, lp, rp, "eth-control")
	require.NoError(t, conn.Context().Err())
	sent := make(chan error, 1)
	go func() { sent <- SendItems(outbound, 5, uint64(42)) }()
	require.NoError(t, ExpectMsg(inbound, 5, []uint64{42}))
	require.NoError(t, <-sent)
}

func TestBulkChannelClaimCancelledBeforeOwnership(t *testing.T) {
	session := newHelloTestSession()
	stream := &helloTestStream{writeLimit: 1024}
	rw := &bulkStreamMsgRW{stream: stream, ready: &bulkChannelReady{claimed: make(chan struct{}), done: make(chan struct{})}}
	require.NoError(t, session.storeChannel(nil, "eth-bulk", rw, true))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := session.claimChannel(ctx, "eth-bulk", rw)
	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, session.channels)
	select {
	case <-rw.ready.claimed:
		t.Fatal("cancelled caller claimed ownership")
	default:
	}
	assertHelloStreamClosed(t, stream)
}

func TestBulkChannelAcknowledgementShutdown(t *testing.T) {
	for _, closeSidecar := range []bool{false, true} {
		left, _, lp, _ := channelReadyTestServers(t)
		session := left.bulk.peerSession(lp.Node(), lp)
		conn, err := session.ensureConn(t.Context())
		require.NoError(t, err)
		rw := &bulkStreamMsgRW{ready: &bulkChannelReady{claimed: make(chan struct{})}}
		var ackConn *quic.Conn
		if closeSidecar {
			left.bulk.Close()
		} else {
			ackConn = conn
			require.NoError(t, conn.CloseWithError(bulkSidecarCloseErrorCode, "test shutdown"))
		}
		require.ErrorIs(t, session.acknowledgeChannel(ackConn, rw, bulkChannelHello{}), io.EOF)
	}
}

func channelReadyTestServers(t *testing.T) (*testBulkServer, *testBulkServer, *Peer, *Peer) {
	t.Helper()
	left, right := newTestBulkServer(t), newTestBulkServer(t)
	t.Cleanup(left.close)
	t.Cleanup(right.close)
	if bytes.Compare(left.bulk.localID[:], right.bulk.localID[:]) > 0 {
		left, right = right, left
	}
	left.setQUICPort()
	right.setQUICPort()
	lp, rp := newTestTrackedPeer(right.localnode.Node()), newTestTrackedPeer(left.localnode.Node())
	left.setPeer(lp)
	right.setPeer(rp)
	return left, right, lp, rp
}
