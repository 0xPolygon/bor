package p2p

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestBulkCloseWaitsForIncomingAuth(t *testing.T) {
	bulk, srv := newBulkAuthWaitServer(t)
	conn, err := quic.DialAddr(t.Context(), bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.CloseWithError(0, "test complete")) })
	stream, err := conn.OpenStreamSync(t.Context())
	require.NoError(t, err)
	require.NoError(t, writeBulkControl(stream, bulkAuthHello{Version: bulkSidecarVersion, From: enode.ID{1}, To: bulk.localID}))
	select {
	case <-srv.peerOp:
	case <-time.After(time.Second):
		t.Fatal("authentication did not reach the peer lookup")
	}
	release := sync.OnceFunc(func() { close(srv.peerOpDone) })
	t.Cleanup(release)
	stopped := make(chan struct{})
	go func() { bulk.Close(); close(stopped) }()
	<-bulk.closeCh
	select {
	case <-stopped:
		t.Fatal("sidecar closed before authentication finished")
	case <-time.After(25 * time.Millisecond):
	}
	release()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("sidecar did not close after authentication finished")
	}
}

func TestServerStopDrainsBulkWorkWithoutServerLock(t *testing.T) {
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: 1, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", EnableBulkSidecar: true, BulkListenAddr: "127.0.0.1:0",
	}}
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)
	bulk := srv.BulkSidecar()
	lookedUp, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	bulk.startWorker(func() {
		<-bulk.closeCh
		srv.Peer(enode.ID{})
		close(lookedUp)
		<-release
	})
	stopped := make(chan struct{})
	go func() { srv.Stop(); close(stopped) }()
	select {
	case <-lookedUp:
	case <-time.After(time.Second):
		t.Fatal("shutdown held the server lock while waiting for bulk work")
	}
	select {
	case <-stopped:
		t.Fatal("server stopped before bulk work finished")
	case <-srv.quit:
		t.Fatal("server main loop stopped before bulk work finished")
	default:
	}
	unblock()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("server did not stop after bulk work finished")
	}
}

func TestBulkCloseCancelsQueuedChannelOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bulk := &BulkSidecar{closeCh: make(chan struct{}), sessions: make(map[enode.ID]*bulkSession)}
		peer := newTestTrackedPeer(enode.NewV4(&newkey().PublicKey, net.IPv4(127, 0, 0, 1), 30303, 30303))
		session := bulk.peerSession(peer.Node(), peer)
		session.channelOpenGate() <- struct{}{}
		result := make(chan error, 1)
		go func() {
			_, err := bulk.OpenChannelContext(t.Context(), peer, "eth-bulk")
			result <- err
		}()
		synctest.Wait()
		started := time.Now()
		bulk.Close()
		require.Zero(t, time.Since(started), "shutdown waited for the channel-open timeout")
		require.ErrorIs(t, <-result, io.EOF)
		_, err := bulk.OpenChannel(peer, "eth-bulk")
		if !errors.Is(err, io.EOF) {
			t.Fatalf("channel open after shutdown: %v", err)
		}
		bulk.startWorker(func() { t.Error("work started after shutdown") })
	})
}

func newBulkAuthWaitServer(t *testing.T) (*BulkSidecar, *Server) {
	t.Helper()
	db, err := enode.OpenDB("")
	require.NoError(t, err)
	t.Cleanup(db.Close)
	srv := &Server{
		Config: Config{PrivateKey: newkey(), MaxPeers: 1}, running: true,
		log: log.New(), quit: make(chan struct{}),
		peerOp: make(chan peerOpFunc), peerOpDone: make(chan struct{}),
	}
	srv.localnode = enode.NewLocalNode(db, srv.PrivateKey)
	srv.localnode.SetFallbackIP(net.IPv4(127, 0, 0, 1))
	bulk, err := newBulkSidecar(srv, "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(bulk.Close)
	go bulk.run()
	return bulk, srv
}
