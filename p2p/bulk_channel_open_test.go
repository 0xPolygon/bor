package p2p

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cancelOnAckStream struct {
	net.Conn
	cancel context.CancelFunc
	reads  int
}

func (s *cancelOnAckStream) Read(p []byte) (int, error) {
	n, err := s.Conn.Read(p)
	s.reads++
	if s.reads == 2 {
		s.cancel()
	}
	return n, err
}

type cancelOnHelloStream struct {
	net.Conn
	cancel context.CancelFunc
	writes int
}

func (s *cancelOnHelloStream) Write(p []byte) (int, error) {
	s.writes++
	if s.writes == 2 {
		s.cancel()
	}
	return s.Conn.Write(p)
}

func TestBulkChannelOpenAcknowledgementWinsCancellation(t *testing.T) {
	for _, timing := range []string{"during hello", "before ack", "during ack"} {
		t.Run(timing, func(t *testing.T) {
			sender, receiver := newHelloTestSession(), newHelloTestSession()
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var stream bulkFrameStream = local
			if timing == "during ack" {
				stream = &cancelOnAckStream{Conn: local, cancel: cancel}
			} else if timing == "during hello" {
				stream = &cancelOnHelloStream{Conn: local, cancel: cancel}
			}
			opened := make(chan bulkChannelResult, 1)
			accepted := make(chan error, 1)
			go func() {
				rw, err := sender.openChannelStream(ctx, nil, stream, "eth-bulk")
				opened <- bulkChannelResult{rw: rw, err: err}
			}()
			go func() { accepted <- receiver.acceptChannel(nil, remote) }()
			require.Eventually(t, func() bool {
				_, exists := receiver.getChannel("eth-bulk")
				return exists
			}, time.Second, time.Millisecond)
			if timing == "before ack" {
				cancel()
			}
			inbound, err := receiver.waitChannel(t.Context(), nil, "eth-bulk")
			require.NoError(t, err)
			result := <-opened
			require.NoError(t, result.err)
			require.NoError(t, <-accepted)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			assertBulkChannelExchange(t, result.rw, inbound)
		})
	}
}

func assertBulkChannelExchange(t *testing.T, left, right MsgReadWriter) {
	t.Helper()
	for _, pair := range [][2]MsgReadWriter{{left, right}, {right, left}} {
		sent := make(chan error, 1)
		go func() { sent <- SendItems(pair[0], 5, uint64(42)) }()
		require.NoError(t, ExpectMsg(pair[1], 5, []uint64{42}))
		require.NoError(t, <-sent)
	}
}

func TestBulkChannelOpenCancelledBeforeHello(t *testing.T) {
	session := newHelloTestSession()
	stream := &helloTestStream{writeLimit: 1024}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rw, err := session.openChannelStream(ctx, nil, stream, "eth-bulk")
	require.Nil(t, rw)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, stream.Bytes())
	require.Equal(t, 1024, stream.writeLimit)
	require.Empty(t, session.channels)
	assertHelloStreamClosed(t, stream)
}

func TestBulkChannelOpenReadDeadline(t *testing.T) {
	session := newHelloTestSession()
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	stream := &ackDeadlineStream{Conn: local}
	done := make(chan error, 1)
	go func() {
		_, err := session.openChannelStream(t.Context(), nil, stream, "eth-bulk")
		done <- err
	}()
	var hello bulkChannelHello
	require.NoError(t, readBulkControl(remote, bulkChannelControlMaxSize, &hello))
	select {
	case err := <-done:
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("stalled acknowledgement was not bounded")
	}
	require.Greater(t, stream.timeout, bulkChannelOpenTimeout+bulkAuthTimeout)
	require.Empty(t, session.channels)
}

type ackDeadlineStream struct {
	net.Conn
	timeout time.Duration
}

func (s *ackDeadlineStream) SetReadDeadline(deadline time.Time) error {
	s.timeout = time.Until(deadline)
	return s.Conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
}

func TestBulkControlReadDeadlineFailure(t *testing.T) {
	stream := &helloTestStream{writeLimit: 1024, readDeadline: errPartialPayload}
	hello := bulkChannelHello{Version: bulkSidecarVersion, Channel: "eth-bulk"}
	require.NoError(t, writeBulkControl(stream, hello))
	buffered := stream.Len()
	var got bulkChannelHello
	require.ErrorIs(t, readBulkControl(stream, bulkChannelControlMaxSize, &got), errPartialPayload)
	require.Equal(t, buffered, stream.Len(), "a read without a deadline must not proceed")
}

func TestBulkChannelOpenConnectionShutdown(t *testing.T) {
	left, right, lp, rp := channelReadyTestServers(t)
	sender := left.bulk.peerSession(lp.Node(), lp)
	_, err := sender.ensureConn(t.Context())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opened := make(chan error, 1)
	go func() {
		_, err := left.bulk.OpenChannelContext(ctx, lp, "eth-bulk")
		opened <- err
	}()
	receiver := right.bulk.peerSession(rp.Node(), rp)
	require.Eventually(t, func() bool {
		_, exists := receiver.getChannel("eth-bulk")
		return exists
	}, time.Second, time.Millisecond)
	cancel()
	right.bulk.DropPeer(rp.ID())
	select {
	case err := <-opened:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("peer shutdown did not interrupt acknowledgement wait")
	}
	_, exists := sender.getChannel("eth-bulk")
	require.False(t, exists)
}
