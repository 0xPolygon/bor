package p2p

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestServerStartRollback(t *testing.T) {
	baseline := goleak.IgnoreCurrent()
	// LevelDB's memory-pool cleanup has a one-second grace period after Close.
	defer func() {
		require.Eventually(t, func() bool { return goleak.Find(baseline) == nil }, 5*time.Second, 50*time.Millisecond)
	}()
	for _, phase := range []string{"key", "database", "tcp", "bulk address", "bulk port", "discovery address", "discovery v4", "discovery v5", "discovery shared"} {
		t.Run(phase, func(t *testing.T) { testServerStartRollback(t, phase) })
	}
}

func testServerStartRollback(t *testing.T, phase string) {
	config := Config{
		PrivateKey: newkey(), MaxPeers: 1, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", EnableBulkSidecar: true, BulkListenAddr: "127.0.0.1:0",
		NodeDatabase: filepath.Join(t.TempDir(), "nodes"),
	}
	srv := &Server{Config: config}
	t.Cleanup(srv.Stop)
	var listener net.Listener
	srv.listenFunc = func(network, address string) (net.Listener, error) {
		var err error
		listener, err = net.Listen(network, address)
		return listener, err
	}
	configureStartFailure(t, srv, phase)
	listenAddr := srv.ListenAddr
	require.Error(t, srv.Start())
	require.False(t, srv.running)
	require.Nil(t, srv.listener)
	require.Nil(t, srv.BulkSidecar())
	require.Nil(t, srv.nodedb)
	require.Nil(t, srv.localnode)
	require.Nil(t, srv.discmix)
	require.Nil(t, srv.DiscoveryV4())
	require.Nil(t, srv.DiscoveryV5())
	require.Equal(t, listenAddr, srv.ListenAddr)
	require.Nil(t, srv.Peer(enode.ID{}))
	if listener != nil {
		probe, err := net.Listen("tcp", listener.Addr().String())
		require.NoError(t, err, "TCP socket must be reusable after startup failure")
		require.NoError(t, probe.Close())
	}
	if srv.Config.DiscoveryV4 || srv.Config.DiscoveryV5 {
		probe, err := net.ListenPacket("udp", srv.DiscAddr)
		require.NoError(t, err, "discovery socket must be reusable after startup failure")
		require.NoError(t, probe.Close())
	}
	srv.Stop()
	require.Error(t, srv.Start(), "repeated failed startup must remain safe")
	require.Zero(t, srv.PeerCount())
	srv.Config = config
	require.NoError(t, srv.Start(), "failed startup must release the database and permit retry")
	require.NotNil(t, srv.BulkSidecar())
	require.ErrorContains(t, srv.Start(), "already running")
	require.True(t, srv.running)
	srv.Stop()
}

func configureStartFailure(t *testing.T, srv *Server, phase string) {
	t.Helper()
	switch phase {
	case "key":
		srv.PrivateKey = nil
	case "database":
		file := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(file, nil, 0600))
		srv.NodeDatabase = filepath.Join(file, "nodes")
	case "tcp":
		srv.ListenAddr = "127.0.0.1:invalid-port"
	case "bulk address":
		srv.BulkListenAddr = "127.0.0.1:invalid-port"
	case "bulk port":
		occupied, err := net.ListenPacket("udp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, occupied.Close()) })
		srv.BulkListenAddr = occupied.LocalAddr().String()
	case "discovery address":
		srv.NoDiscovery = false
		srv.DiscAddr = "127.0.0.1:invalid-port"
	case "discovery v4", "discovery v5", "discovery shared":
		srv.NoDiscovery = false
		srv.Config.DiscoveryV4 = phase != "discovery v5"
		srv.Config.DiscoveryV5 = phase != "discovery v4"
		probe, err := net.ListenPacket("udp", "127.0.0.1:0")
		require.NoError(t, err)
		srv.DiscAddr = probe.LocalAddr().String()
		require.NoError(t, probe.Close())
		invalid := []*enode.Node{enode.NewV4(&newkey().PublicKey, net.IPv4(127, 0, 0, 1), 30303, 0)}
		if phase == "discovery v4" {
			srv.BootstrapNodes = invalid
		} else {
			srv.BootstrapNodesV5 = invalid
		}
	}
}
