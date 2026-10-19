package eth

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/stretchr/testify/require"
)

func TestPeerContributionAdminProtectionAfterEviction(t *testing.T) {
	for _, mode := range []string{"ordinary", "trusted", "static"} {
		t.Run(mode, func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated)), peers: newPeerSet()}
			h.peerPolicy.SetClassResolver(h.peerPolicyClass)
			peer := ethproto.NewPeer(ethproto.ETH68, policyTestConnection(t, mode), policyFaultRW{}, nil)
			t.Cleanup(peer.Close)
			require.NoError(t, h.peers.registerPeer(peer, nil, nil))
			h.observeProtocolPeer(peer)
			for i := range 1024 {
				h.peerPolicy.Connected(fmt.Sprintf("filler-%d", i))
			}
			require.False(t, h.peerPolicy.Snapshot(peer.ID()).Contribution.ConnectionKnown)

			for range 3 {
				info := (*ethHandler)(h).PeerInfo(peer.Peer.ID()).(*ethPeerInfo)
				require.Equal(t, mode != "ordinary", info.Reputation.Contribution.Protected)
				require.False(t, info.Reputation.Contribution.ConnectionKnown)
				require.Equal(t, "unknown", info.Reputation.Contribution.Status)
				require.Zero(t, info.Reputation.Risk)
			}
			// Reading admin metadata must not reinsert the evicted peer and displace history.
			require.True(t, h.peerPolicy.Snapshot("filler-0").Contribution.ConnectionKnown)
			require.False(t, h.peerPolicy.Snapshot(peer.ID()).Contribution.Protected)
		})
	}
}
