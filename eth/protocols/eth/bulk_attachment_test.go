package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/stretchr/testify/require"
)

func TestPeerAttachSharedBulkRW(t *testing.T) {
	primary, primaryRemote := p2p.MsgPipe()
	bulk, bulkRemote := p2p.MsgPipe()
	for _, pipe := range []*p2p.MsgPipeRW{primary, primaryRemote, bulk, bulkRemote} {
		t.Cleanup(func() { require.NoError(t, pipe.Close()) })
	}
	peer := NewPeer(ETH69, p2p.NewPeer(enode.ID{}, "bulk-test", nil), primary, nil)
	t.Cleanup(peer.Close)
	original := peer.rw
	require.False(t, peer.HasBulkRW())
	peer.AttachBulkRW(bulk)
	require.True(t, peer.HasBulkRW())
	require.Same(t, original, peer.rw)
	for _, channel := range []string{ethControlChannel, ethBlocksChannel, ethTxChannel, ethTxFetchChannel, ethBulkChannel} {
		require.True(t, peer.rw.(interface{ HasBulkChannel(string) bool }).HasBulkChannel(channel))
	}
	bare := &Peer{rw: primary}
	bare.AttachBulkRW(bulk)
	bare.AttachBulkChannelRW(ethBulkChannel, bulk)
	require.False(t, bare.HasBulkRW())
}
