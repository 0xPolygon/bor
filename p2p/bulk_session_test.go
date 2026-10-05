package p2p

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestBulkSessionRetiresClosedConnection(t *testing.T) {
	for _, action := range []string{"live", "get", "wait", "store", "clear"} {
		t.Run(action, func(t *testing.T) {
			session := newHelloTestSession()
			conn := new(quic.Conn)
			closed := make(chan struct{})
			session.conn, session.connClosed = conn, closed
			session.channels["eth-bulk"] = &closeTrackingRW{}
			waiter := make(chan bulkChannelResult, 1)
			session.waiters["snap-trie"] = []chan bulkChannelResult{waiter}
			close(closed)
			switch action {
			case "live":
				session.lock.Lock()
				live := session.liveConnLocked()
				session.lock.Unlock()
				require.Nil(t, live)
			case "get":
				rw, ok := session.getChannel("eth-bulk")
				require.False(t, ok)
				require.Nil(t, rw)
			case "wait":
				rw, err := session.waitChannel(context.Background(), conn, "eth-bulk")
				require.ErrorIs(t, err, io.EOF)
				require.Nil(t, rw)
			case "store":
				require.ErrorIs(t, session.storeChannel(conn, "wit-bulk", &closeTrackingRW{}), io.EOF)
			case "clear":
				session.clearConn(conn)
			}
			require.Nil(t, session.conn)
			require.Nil(t, session.connClosed)
			require.Empty(t, session.channels)
			require.Empty(t, session.waiters)
			select {
			case result := <-waiter:
				require.ErrorIs(t, result.err, io.EOF)
				require.Nil(t, result.rw)
			default:
				t.Fatal("connection cleanup did not release the channel waiter")
			}
			_, ok := <-waiter
			require.False(t, ok)
			session.clearConn(conn)
		})
	}
}

func TestBulkSessionStaleCleanupPreservesReplacement(t *testing.T) {
	session := newHelloTestSession()
	old, current := new(quic.Conn), new(quic.Conn)
	session.conn = current
	session.connClosed = make(chan struct{})
	rw := &closeTrackingRW{}
	session.channels["eth-bulk"] = rw
	waiter := make(chan bulkChannelResult, 1)
	session.waiters["snap-trie"] = []chan bulkChannelResult{waiter}
	session.clearConn(old)
	require.Same(t, current, session.conn)
	got, ok := session.getChannel("eth-bulk")
	require.True(t, ok)
	require.Same(t, rw, got)
	_, err := session.waitChannel(context.Background(), old, "eth-bulk")
	require.ErrorIs(t, err, io.EOF)
	require.Len(t, session.waiters["snap-trie"], 1)
	select {
	case <-waiter:
		t.Fatal("stale cleanup released a replacement connection's waiter")
	default:
	}
}

func TestBulkSessionClosedConnectionDropsCachedChannels(t *testing.T) {
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
	old, err := left.bulk.OpenChannel(lp, "eth-bulk")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	remoteSession := right.bulk.peerSession(rp.Node(), rp)
	remoteConn, err := remoteSession.waitIncomingConn(ctx)
	require.NoError(t, err)
	session := left.bulk.peerSession(lp.Node(), lp)
	session.lock.Lock()
	conn := session.conn
	err = conn.CloseWithError(0, "test complete")
	// Detect closure before runConn can perform its deferred cleanup.
	live := session.liveConnLocked()
	session.lock.Unlock()
	require.NoError(t, err)
	require.Nil(t, live)
	session.clearConn(conn)
	failed, stop := context.WithCancel(context.Background())
	stop()
	_, err = session.ensureConn(failed)
	require.Error(t, err)
	select {
	case <-remoteConn.Context().Done():
	case <-ctx.Done():
		t.Fatal("remote connection did not close")
	}
	got, err := session.openChannel(ctx, "eth-bulk")
	require.NoError(t, err)
	require.NotSame(t, old, got)
}
