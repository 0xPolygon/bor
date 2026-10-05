package eth

import (
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
