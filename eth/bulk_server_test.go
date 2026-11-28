package eth

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/snap"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/stretchr/testify/require"
)

type bulkTestPeers struct {
	eth  *eth.Peer
	snap *snap.Peer
	wit  *wit.Peer
}

func newBulkProtocolServer(t *testing.T) (*p2p.Server, <-chan interface{}) {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	peers := make(chan interface{}, 3)
	server := &p2p.Server{Config: p2p.Config{
		PrivateKey: key, MaxPeers: 5, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", BulkListenAddr: "127.0.0.1:0", EnableBulkSidecar: true,
		Protocols: []p2p.Protocol{
			{Name: "eth", Version: eth.ETH69, Length: 32, Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
				peer := eth.NewPeer(eth.ETH69, p, rw, nil)
				defer peer.Close()
				peers <- peer
				<-p.Done()
				return nil
			}},
			{Name: "snap", Version: snap.SNAP1, Length: 8, Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
				peers <- snap.NewPeer(snap.SNAP1, p, rw)
				<-p.Done()
				return nil
			}},
			{Name: "wit", Version: wit.WIT2, Length: 8, Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
				peer := wit.NewPeer(wit.WIT2, p, rw, log.New())
				defer peer.Close()
				peers <- peer
				<-p.Done()
				return nil
			}},
		},
	}}
	require.NoError(t, server.Start())
	t.Cleanup(server.Stop)
	return server, peers
}

func receiveBulkTestPeers(t *testing.T, peers <-chan interface{}) bulkTestPeers {
	t.Helper()
	var result bulkTestPeers
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for range 3 {
		select {
		case peer := <-peers:
			switch peer := peer.(type) {
			case *eth.Peer:
				result.eth = peer
			case *snap.Peer:
				result.snap = peer
			case *wit.Peer:
				result.wit = peer
			}
		case <-timer.C:
			t.Fatal("protocol negotiation timed out")
		}
	}
	require.NotNil(t, result.eth)
	require.NotNil(t, result.snap)
	require.NotNil(t, result.wit)
	return result
}

func TestHandlerBulkSidecarAttachment(t *testing.T) {
	left, leftPeers := newBulkProtocolServer(t)
	right, rightPeers := newBulkProtocolServer(t)
	// Discovery is disabled, so supply the signed endpoint records it would cache.
	require.NoError(t, left.LocalNode().Database().UpdateNode(right.Self()))
	require.NoError(t, right.LocalNode().Database().UpdateNode(left.Self()))
	left.AddPeer(right.Self())
	a, b := receiveBulkTestPeers(t, leftPeers), receiveBulkTestPeers(t, rightPeers)
	(&handler{p2pServer: left}).attachBulkSidecar(a.eth, a.snap, a.wit)
	(&handler{p2pServer: right}).attachBulkSidecar(b.eth, b.snap, b.wit)
	require.Eventually(t, func() bool {
		return left.BulkSidecar().Status().ActiveChannels == 10 && right.BulkSidecar().Status().ActiveChannels == 10 &&
			a.eth.HasBulkRW() && a.snap.HasBulkRW() && a.wit.HasBulkRW() &&
			b.eth.HasBulkRW() && b.snap.HasBulkRW() && b.wit.HasBulkRW()
	}, 5*time.Second, 10*time.Millisecond)
	for _, server := range []*p2p.Server{left, right} {
		status, err := NewAdminAPI(&Ethereum{p2pServer: server}).BulkSidecarStatus()
		require.NoError(t, err)
		require.True(t, status.Enabled)
		require.Equal(t, 1, status.ActiveSessions)
		require.Equal(t, 10, status.ActiveChannels)
	}
	status, err := NewAdminAPI(&Ethereum{p2pServer: &p2p.Server{}}).BulkSidecarStatus()
	require.NoError(t, err)
	require.Equal(t, &p2p.BulkSidecarStatus{}, status)
}

func TestHandlerBulkSidecarMissedLane(t *testing.T) {
	left, leftPeers := newBulkProtocolServer(t)
	right, rightPeers := newBulkProtocolServer(t)
	require.NoError(t, left.LocalNode().Database().UpdateNode(right.Self()))
	require.NoError(t, right.LocalNode().Database().UpdateNode(left.Self()))
	left.AddPeer(right.Self())
	a, b := receiveBulkTestPeers(t, leftPeers), receiveBulkTestPeers(t, rightPeers)
	if left.Self().ID().String() > right.Self().ID().String() {
		left, right, a, b = right, left, b, a
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	missed, done := make(chan struct{}), make(chan struct{})
	open := bulkTestOpener(func(ctx context.Context, peer *p2p.Peer, channel string) (p2p.MsgReadWriter, error) {
		if channel == "eth-control" {
			// The receiver has already stopped waiting when this hello arrives.
			close(missed)
			return nil, context.DeadlineExceeded
		}
		return right.BulkSidecar().OpenChannelContext(ctx, peer, channel)
	})
	go func() {
		defer close(done)
		attachBulkProtocols(ctx, open, b.eth, b.snap, b.wit)
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-missed:
	case <-time.After(time.Second):
		t.Fatal("receiver did not abandon control lane")
	}
	(&handler{p2pServer: left}).attachBulkSidecar(a.eth, a.snap, a.wit)
	require.Eventually(t, func() bool {
		return left.BulkSidecar().Status().ActiveChannels == 9 && right.BulkSidecar().Status().ActiveChannels == 10 &&
			a.eth.HasBulkRW() && a.snap.HasBulkRW() && a.wit.HasBulkRW() &&
			b.eth.HasBulkRW() && b.snap.HasBulkRW() && b.wit.HasBulkRW()
	}, 3*time.Second, 10*time.Millisecond, "healthy lanes must attach before the unclaimed hello times out")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("healthy receiver lanes did not finish attaching")
	}
	require.Eventually(t, func() bool {
		return right.BulkSidecar().Status().ActiveChannels == 9
	}, 6*time.Second, 10*time.Millisecond)
	want := []string{"eth-blocks", "eth-tx", "eth-tx-fetch", "eth-bulk", "snap-accounts", "snap-storage", "snap-code", "snap-trie", "wit-bulk"}
	for _, server := range []*p2p.Server{left, right} {
		status := server.BulkSidecar().Status()
		require.Equal(t, 1, status.ActiveSessions)
		require.Len(t, status.Peers, 1)
		require.ElementsMatch(t, want, status.Peers[0].Channels)
		require.Equal(t, 1, server.PeerCount())
	}
}
