package p2p

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlogwriter"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
)

func TestBulkConnectionMissingEndpointWaitsForFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newHelloTestSession()
		session.sidecar.closeCh = make(chan struct{})
		session.sidecar.localID = enode.ID{} // lower ID: this node is the primary
		session.remote = enode.NewV4(&newkey().PublicKey, net.IPv4(127, 0, 0, 1), 30303, 30303)
		require.True(t, session.shouldDial(session.peer))

		result := make(chan error, 1)
		var got *quic.Conn
		go func() {
			conn, err := session.ensureConn(t.Context())
			got = conn
			result <- err
		}()
		// The dial fails immediately (no endpoint), yet setup keeps waiting.
		synctest.Wait()
		select {
		case <-result:
			t.Fatal("missing-endpoint primary must wait for the fallback, not fail")
		default:
		}
		require.False(t, session.dialing, "the failed dial must not stay in flight")

		// The peer's fallback dial arrives and is committed by the accept worker.
		want := new(quic.Conn)
		session.lock.Lock()
		session.conn, session.connClosed = want, make(chan struct{})
		session.signalConnLocked()
		session.lock.Unlock()
		require.NoError(t, <-result)
		require.Equal(t, want, got)
	})
}

func TestBulkConnectionSetupDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newHelloTestSession()
		started := time.Now()
		conn, err := session.ensureConn(t.Context())
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Nil(t, conn)
		require.Equal(t, bulkConnectionSetupTimeout, time.Since(started))
	})
}

func TestBulkChannelDeadlineStartsAfterConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newHelloTestSession()
		session.remote = enode.SignNull(new(enr.Record), session.remoteID)
		session.peer = newTestTrackedPeer(session.remote)
		session.sidecar.sessions = map[enode.ID]*bulkSession{session.remoteID: session}
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
		defer cancel()
		go func() {
			time.Sleep(38 * time.Second)
			session.lock.Lock()
			session.conn, session.connClosed = new(quic.Conn), make(chan struct{})
			session.signalConnLocked()
			session.lock.Unlock()
		}()
		started := time.Now()
		rw, err := session.sidecar.OpenChannelContext(ctx, session.peer, "eth-control")
		require.ErrorIs(t, err, errBulkChannelTimeout)
		require.Nil(t, rw)
		require.Equal(t, 43*time.Second, time.Since(started))
		require.Empty(t, session.waiters)
	})
}

func TestBulkConnectionRetryCancellation(t *testing.T) {
	for _, reason := range []string{"caller", "peer", "sidecar", "connected", "already closed"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				session := newHelloTestSession()
				session.sidecar.closeCh = make(chan struct{})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				session.closed = reason == "already closed"
				result := make(chan error, 1)
				go func() { result <- session.waitConnectionRetry(ctx) }()
				synctest.Wait()
				switch reason {
				case "caller":
					cancel()
				case "peer":
					session.close()
				case "sidecar":
					close(session.sidecar.closeCh)
				case "connected":
					session.lock.Lock()
					session.signalConnLocked()
					session.lock.Unlock()
				}
				started := time.Now()
				err := <-result
				require.Zero(t, time.Since(started), "cancellation must not wait for the retry timer")
				switch reason {
				case "caller":
					require.ErrorIs(t, err, context.Canceled)
				case "sidecar", "already closed":
					require.ErrorIs(t, err, io.EOF)
				default:
					require.NoError(t, err)
				}
			})
		})
	}
}

func TestBulkConnectionCancelledCaller(t *testing.T) {
	session := newHelloTestSession()
	session.conn, session.connClosed = new(quic.Conn), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	conn, err := session.ensureConn(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, conn)
}

func TestBulkConnectionDialCallerCancellation(t *testing.T) {
	left, right, lp, _ := channelReadyTestServers(t)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var attempts atomic.Int32
	startBulkDialTestListener(t, right, func(*quic.ClientInfo) (*quic.Config, error) {
		if attempts.Add(1) == 1 {
			close(entered)
			<-release
			return nil, errors.New("first caller cancelled")
		}
		return nil, nil
	})
	t.Cleanup(unblock)
	require.NoError(t, left.db.UpdateNode(right.localnode.Node()))
	session := left.bulk.peerSession(lp.Node(), lp)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := session.ensureConn(ctx); first <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first dial did not start")
	}
	results := make(chan error, 9)
	for range cap(results) {
		go func() { _, err := session.ensureConn(t.Context()); results <- err }()
	}
	cancel()
	select {
	case err := <-first:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancelled caller kept waiting for the shared dial")
	}
	unblock()
	for range cap(results) {
		select {
		case err := <-results:
			require.NoError(t, err, "cancelling one caller must preserve other callers' setup budgets")
		case <-time.After(2 * time.Second):
			t.Fatal("remaining callers did not establish a connection")
		}
	}
	require.Equal(t, 1, left.bulk.Status().ActiveSessions)
}

func TestBulkConnectionShutdownDuringRetry(t *testing.T) {
	for _, peerDrop := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			session := newHelloTestSession()
			session.sidecar.localID = enode.ID{}
			session.sidecar.closeCh = make(chan struct{})
			session.remote = enode.NewV4(&newkey().PublicKey, net.IPv4(127, 0, 0, 1), 30303, 30303)
			result := make(chan error, 1)
			go func() { _, err := session.ensureConn(t.Context()); result <- err }()
			synctest.Wait()
			started := time.Now()
			if peerDrop {
				session.close()
			} else {
				session.sidecar.Close()
			}
			require.ErrorIs(t, <-result, io.EOF)
			require.Zero(t, time.Since(started))
		})
	}
}

func TestBulkConnectionRejectedAttempts(t *testing.T) {
	left, right, lp, _ := channelReadyTestServers(t)
	var attempts atomic.Int32
	// A refused Initial can reach the server repeatedly; count client connections.
	config := left.bulk.config.Clone()
	trace := config.Tracer
	config.Tracer = func(ctx context.Context, client bool, id quic.ConnectionID) qlogwriter.Trace {
		attempts.Add(1)
		return trace(ctx, client, id)
	}
	left.bulk.config = config
	startBulkDialTestListener(t, right, func(*quic.ClientInfo) (*quic.Config, error) {
		return nil, errors.New("unavailable")
	})
	require.NoError(t, left.db.UpdateNode(right.localnode.Node()))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var rw MsgReadWriter
	result := make(chan error, 1)
	go func() {
		var err error
		rw, err = left.bulk.OpenChannelContext(ctx, lp, "eth-control")
		result <- err
	}()
	require.Eventually(t, func() bool { return attempts.Load() == 2 }, 2*time.Second, time.Millisecond)
	require.Never(t, func() bool { return attempts.Load() > 2 }, 2*bulkConnectionRetryDelay, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	require.Nil(t, rw)
	require.Equal(t, int32(2), attempts.Load(), "a caller must stop after its second rejected attempt")
	require.Zero(t, left.bulk.Status().ActiveSessions)
	require.Zero(t, left.bulk.Status().ActiveChannels)
}
