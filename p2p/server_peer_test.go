package p2p

import (
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/stretchr/testify/require"
)

func TestServerPeerInactive(t *testing.T) {
	for _, state := range []string{"unstarted", "stop before start", "failed start", "stopped"} {
		t.Run(state, func(t *testing.T) {
			srv := &Server{}
			t.Cleanup(srv.Stop)
			switch state {
			case "stop before start":
				srv.Stop()
			case "failed start":
				require.Error(t, srv.Start())
			case "stopped":
				srv.PrivateKey, srv.NoDiscovery, srv.NoDial = newkey(), true, true
				require.NoError(t, srv.Start())
				srv.Stop()
			}
			result := make(chan *Peer, 1)
			go func() { result <- srv.Peer(enode.ID{}) }()
			select {
			case peer := <-result:
				require.Nil(t, peer)
			case <-time.After(time.Second):
				t.Fatal("peer lookup blocked on an inactive server")
			}
		})
	}
}

func TestServerPeerConnected(t *testing.T) {
	left, right := newStartedBulkServer(t), newStartedBulkServer(t)
	require.True(t, syncAddPeer(left, right.Self()))
	peer := left.Peer(right.Self().ID())
	require.NotNil(t, peer)
	require.Equal(t, right.Self().ID(), peer.ID())
	require.Nil(t, left.Peer(left.Self().ID()))
	left.RemovePeer(right.Self())
	require.Nil(t, left.Peer(right.Self().ID()))
}

func TestServerPeerConcurrentLifecycle(t *testing.T) {
	srv := &Server{Config: Config{NoDiscovery: true, NoDial: true}}
	t.Cleanup(srv.Stop)
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					srv.Peer(enode.ID{})
				}
			}
		})
	}
	defer func() {
		close(stop)
		readers.Wait()
	}()
	for range 3 {
		require.Error(t, srv.Start())
	}
	srv.PrivateKey = newkey()
	require.NoError(t, srv.Start())
	srv.Stop()
}
