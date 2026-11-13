package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newStartedBulkServer(t *testing.T) *Server {
	t.Helper()
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: 2, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", BulkListenAddr: "127.0.0.1:0", EnableBulkSidecar: true,
		Protocols: []Protocol{{Name: "eth", Version: 69, Length: 17, Run: func(p *Peer, _ MsgReadWriter) error {
			<-p.Done()
			return nil
		}}},
	}}
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)
	return srv
}

func TestServerStartAcceptsBulkConnections(t *testing.T) {
	left, right := newStartedBulkServer(t), newStartedBulkServer(t)
	require.NoError(t, left.LocalNode().Database().UpdateNode(right.Self()))
	require.NoError(t, right.LocalNode().Database().UpdateNode(left.Self()))
	require.True(t, syncAddPeer(left, right.Self()))
	require.Eventually(t, func() bool { return right.Peer(left.Self().ID()) != nil }, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	sent := make(chan error, 1)
	go func() {
		rw, err := left.BulkSidecar().OpenChannelContext(ctx, left.Peer(right.Self().ID()), "eth-bulk")
		if err == nil {
			err = SendItems(rw, 1, uint64(42))
		}
		sent <- err
	}()
	rw, err := right.BulkSidecar().OpenChannelContext(ctx, right.Peer(left.Self().ID()), "eth-bulk")
	require.NoError(t, err)
	require.NoError(t, <-sent)
	require.NoError(t, ExpectMsg(rw, 1, []uint64{42}))
}
