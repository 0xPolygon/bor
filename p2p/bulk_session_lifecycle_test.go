package p2p

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestBulkSessionDialWaitOutcomes(t *testing.T) {
	for _, outcome := range []string{"connected", "missing", "closed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			session := newHelloTestSession()
			wait := make(chan struct{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if outcome == "cancelled" {
				cancel()
			} else {
				close(wait)
			}
			if outcome == "connected" {
				session.conn, session.connClosed = new(quic.Conn), make(chan struct{})
			}
			session.closed = outcome == "closed"
			conn, err := session.waitDial(ctx, wait)
			switch outcome {
			case "connected":
				require.NoError(t, err)
				require.Same(t, session.conn, conn)
			case "cancelled":
				require.ErrorIs(t, err, context.Canceled)
			default:
				require.ErrorIs(t, err, errBulkSidecarNoPeer)
			}
		})
	}
}

func TestBulkSessionPendingDialAndClose(t *testing.T) {
	session := newHelloTestSession()
	session.sidecar.localID = enode.ID{}
	session.dialing, session.dialWait = true, make(chan struct{})
	close(session.dialWait)
	_, err := session.ensureConn(t.Context())
	require.ErrorIs(t, err, errBulkSidecarNoPeer)
	ctx, cancel := context.WithCancel(t.Context())
	session.dialCancel = cancel
	session.close()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	_, err = session.waitIncomingConn(t.Context())
	require.ErrorIs(t, err, io.EOF)
	_, ok := session.getChannel("eth-bulk")
	require.False(t, ok)
	session.closed = false
	_, err = session.waitIncomingConn(ctx)
	require.ErrorIs(t, err, context.Canceled)
	_, err = session.openChannel(t.Context(), "unknown")
	require.ErrorContains(t, err, "invalid bulk channel")
}

func TestBulkSessionChannelWaitersAndLimits(t *testing.T) {
	session := newHelloTestSession()
	first, second := make(chan bulkChannelResult, 1), make(chan bulkChannelResult, 1)
	session.waiters["eth-bulk"] = []chan bulkChannelResult{first, second}
	session.removeWaiter("eth-bulk", first)
	require.Equal(t, []chan bulkChannelResult{second}, session.waiters["eth-bulk"])
	rw := &closeTrackingRW{closed: make(chan struct{})}
	require.NoError(t, session.storeChannel(nil, "eth-bulk", rw))
	result := <-second
	require.NoError(t, result.err)
	require.Same(t, rw, result.rw)
	_, open := <-second
	require.False(t, open)
	got, err := session.waitChannel(t.Context(), nil, "eth-bulk")
	require.NoError(t, err)
	require.Same(t, rw, got)
	for channel := range bulkChannels {
		require.NoError(t, session.storeChannel(nil, channel, &closeTrackingRW{closed: make(chan struct{})}))
	}
	require.ErrorIs(t, session.storeChannel(nil, "extra", rw), errBulkChannelLimit)
	require.Len(t, session.channels, maxBulkChannelsPerSession)
}

type bulkCloseErrorRW struct{ closeTrackingRW }

func (*bulkCloseErrorRW) Close() error { return errors.New("close failed") }

func TestBulkSessionReplacementCloseError(t *testing.T) {
	session := newHelloTestSession()
	session.channels["eth-bulk"] = &bulkCloseErrorRW{}
	next := &closeTrackingRW{}
	require.NoError(t, session.storeChannel(nil, "eth-bulk", next))
	got, ok := session.getChannel("eth-bulk")
	require.True(t, ok)
	require.Same(t, next, got)
}
