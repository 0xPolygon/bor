package p2p

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestBulkConnectionDirection(t *testing.T) {
	for _, higher := range []bool{false, true} {
		for _, flag := range []connFlag{0, inboundConn, dynDialedConn, staticDialedConn} {
			t.Run(fmt.Sprintf("higher=%v/flags=%d", higher, flag), func(t *testing.T) {
				session := &bulkSession{sidecar: &BulkSidecar{localID: enode.ID{1}}, remoteID: enode.ID{2}}
				if higher {
					session.sidecar.localID, session.remoteID = session.remoteID, session.sidecar.localID
				}
				peer := &Peer{rw: &conn{flags: flag}}
				want := flag == dynDialedConn || flag == staticDialedConn || flag == 0 && !higher
				require.Equal(t, want, session.shouldDial(peer))
				require.Equal(t, !higher, session.shouldDial(nil))
				require.Equal(t, !higher, session.shouldDial(&Peer{}))
			})
		}
	}
	session := &bulkSession{sidecar: &BulkSidecar{localID: enode.ID{1}}, remoteID: enode.ID{1}}
	require.False(t, session.shouldDial(nil), "an identical node ID cannot initiate a sidecar connection")
}

func TestBulkChannelsFollowRLPxDirection(t *testing.T) {
	for _, higher := range []bool{false, true} {
		for _, cached := range []bool{false, true} {
			for _, flag := range []connFlag{dynDialedConn, staticDialedConn} {
				t.Run(fmt.Sprintf("higher=%v/cached=%v/flags=%d", higher, cached, flag), func(t *testing.T) {
					testBulkChannelsFollowRLPxDirection(t, higher, cached, flag)
				})
			}
		}
	}
}

func testBulkChannelsFollowRLPxDirection(t *testing.T, higher, cached bool, flag connFlag) {
	t.Helper()
	dialer, acceptor, _, _ := channelReadyTestServers(t)
	if higher {
		dialer, acceptor = acceptor, dialer
	}
	outbound := newTestTrackedPeer(acceptor.localnode.Node())
	outbound.rw.flags = flag
	node := dialer.localnode.Node()
	inbound := newTestTrackedPeer(enode.NewV4(node.Pubkey(), node.IP(), 40000, 40000))
	inbound.rw.flags = inboundConn
	if cached {
		require.NoError(t, acceptor.db.UpdateNode(node))
	}
	dialer.setPeer(outbound)
	acceptor.setPeer(inbound)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	opened := make(chan error, 2*len(bulkChannels))
	for channel := range bulkChannels {
		go func() {
			_, err := dialer.bulk.OpenChannelContext(ctx, outbound, channel)
			opened <- err
		}()
		go func() {
			_, err := acceptor.bulk.OpenChannelContext(ctx, inbound, channel)
			opened <- err
		}()
	}
	for range cap(opened) {
		require.NoError(t, <-opened)
	}
	for _, server := range []*testBulkServer{dialer, acceptor} {
		status := server.bulk.Status()
		require.Equal(t, 1, status.ActiveSessions)
		require.Equal(t, len(bulkChannels), status.ActiveChannels)
	}
	assertBulkDirectionTraffic(t, dialer.bulk.peerSession(outbound.Node(), outbound), acceptor.bulk.peerSession(inbound.Node(), inbound))
}

func assertBulkDirectionTraffic(t *testing.T, dialer, acceptor *bulkSession) {
	t.Helper()
	for channel := range bulkChannels {
		left, ok := dialer.getChannel(channel)
		require.True(t, ok)
		right, ok := acceptor.getChannel(channel)
		require.True(t, ok)
		for _, pair := range [][2]MsgReadWriter{{left, right}, {right, left}} {
			sent := make(chan error, 1)
			go func() { sent <- SendItems(pair[0], 1, uint64(42)) }()
			msg := readRoutedTestMessage(t, pair[1])
			require.False(t, msg.Time().IsZero())
			var payload []uint64
			require.NoError(t, msg.Decode(&payload))
			require.Equal(t, []uint64{42}, payload)
			require.NoError(t, <-sent)
		}
	}
}
