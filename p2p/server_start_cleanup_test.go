package p2p

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

func TestServerStartReportsCleanupError(t *testing.T) {
	for _, alreadyClosed := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "closed"}[alreadyClosed], func(t *testing.T) {
			var output bytes.Buffer
			srv := &Server{Config: Config{
				PrivateKey: newkey(), NoDiscovery: true, NoDial: true, ListenAddr: "127.0.0.1:0",
				EnableBulkSidecar: true, BulkListenAddr: "127.0.0.1:invalid-port",
				Logger: log.NewLogger(log.NewTerminalHandlerWithLevel(&output, log.LevelDebug, false)),
			}}
			t.Cleanup(srv.Stop)
			srv.listenFunc = func(network, address string) (net.Listener, error) {
				listener, err := net.Listen(network, address)
				if err == nil && alreadyClosed {
					err = listener.Close()
				}
				return listener, err
			}
			require.ErrorContains(t, srv.Start(), "invalid-port")
			require.Equal(t, alreadyClosed, strings.Contains(output.String(), "Listener cleanup failed"))
			require.False(t, srv.running)
			require.Nil(t, srv.listener)
		})
	}
}

type startupWaitNAT struct {
	mockNAT
	entered chan struct{}
	release chan struct{}
}

func (n *startupWaitNAT) ExternalIP() (net.IP, error) {
	close(n.entered)
	<-n.release
	return net.IPv4(192, 0, 2, 1), nil
}

func TestServerStartWaitsForPortMapping(t *testing.T) {
	nat := &startupWaitNAT{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(nat.release) }) }
	srv := &Server{Config: Config{
		PrivateKey: newkey(), NoDiscovery: true, NoDial: true, ListenAddr: "127.0.0.1:0", NAT: nat,
	}}
	t.Cleanup(srv.Stop)
	t.Cleanup(release)
	failure := errors.New("listener unavailable")
	srv.listenFunc = func(string, string) (net.Listener, error) {
		<-nat.entered
		return nil, failure
	}
	finished := make(chan error, 1)
	go func() { finished <- srv.Start() }()
	select {
	case <-nat.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("port mapping did not start")
	}
	select {
	case err := <-finished:
		t.Fatalf("startup returned before port mapping stopped: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, failure)
	case <-time.After(2 * time.Second):
		t.Fatal("startup did not finish after port mapping stopped")
	}
	require.Nil(t, srv.localnode)
	require.Nil(t, srv.nodedb)
}
