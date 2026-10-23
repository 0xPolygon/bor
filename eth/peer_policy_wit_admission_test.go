package eth

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/stretchr/testify/require"
)

type policyWitnessBackend struct {
	*witHandler
	handled chan error
}

func (b *policyWitnessBackend) Handle(peer *wit.Peer, packet wit.Packet) error {
	err := b.witHandler.Handle(peer, packet)
	b.handled <- err
	return err
}

type policyWitnessSession struct {
	peer         *wit.Peer
	remote       *ethproto.Peer
	ethRW, witRW *p2p.MsgPipeRW
	handled      <-chan error
	admitted     chan struct{}
}

func newPolicyWitnessSession(t *testing.T, h *testHandler, id enode.ID) *policyWitnessSession {
	t.Helper()
	ethRemote, ethLocal := p2p.MsgPipe()
	witRemote, witLocal := p2p.MsgPipe()
	caps := []p2p.Cap{{Name: "eth", Version: ethproto.ETH69}, {Name: "wit", Version: wit.WIT2}}
	connection := p2p.NewPeerPipe(id, "pending", caps, ethLocal)
	ep := ethproto.NewPeer(ethproto.ETH69, connection, ethLocal, h.txpool)
	wp := wit.NewPeer(wit.WIT2, connection, witLocal, log.New())
	remote := ethproto.NewPeer(ethproto.ETH69, p2p.NewPeerPipe(enode.ID{255}, "remote", nil, ethRemote), ethRemote, h.txpool)
	backend := &policyWitnessBackend{witHandler: (*witHandler)(h.handler), handled: make(chan error, 1)}
	session := &policyWitnessSession{wp, remote, ethRemote, witRemote, backend.handled, make(chan struct{})}
	witResult, ethResult := make(chan error, 1), make(chan error, 1)
	stop := make(chan struct{})
	go func() {
		witResult <- h.handler.runWitExtension(wp, func(p *wit.Peer) error { return wit.Handle(backend, p) })
	}()
	go func() {
		ethResult <- h.handler.runEthPeer(ep, func(*ethproto.Peer) error { close(session.admitted); <-stop; return nil })
	}()
	t.Cleanup(func() {
		close(stop)
		require.NoError(t, witRemote.Close())
		require.NoError(t, ethRemote.Close())
		require.Error(t, policyAdmissionResult(t, witResult))
		select {
		case <-session.admitted:
			require.NoError(t, policyAdmissionResult(t, ethResult))
		default:
			require.Error(t, policyAdmissionResult(t, ethResult))
		}
		wp.Close()
		ep.Close()
		remote.Close()
	})
	return session
}

func pendingPolicyWitness(t *testing.T, h *testHandler, id enode.ID) (*wit.Peer, *p2p.MsgPipeRW, <-chan error) {
	t.Helper()
	session := newPolicyWitnessSession(t, h, id)
	msg, err := session.ethRW.ReadMsg()
	require.NoError(t, err)
	require.Equal(t, uint64(ethproto.StatusMsg), msg.Code)
	require.NoError(t, msg.Discard())
	return session.peer, session.witRW, session.handled
}

func TestPeerPolicyWitnessBeforeAdmissionPreservesHistory(t *testing.T) {
	h := newPolicyAdmissionHandler(t, new(mclock.Simulated), 10)
	for i := range 1024 {
		h.handler.peerPolicy.Observe(fmt.Sprintf("retained-%d", i), peerpolicy.Evidence{Reason: peerpolicy.RateLimited})
	}
	for attempt := range 3 {
		wp, remote, handled := pendingPolicyWitness(t, h, enode.ID{byte(attempt + 1)})
		before := h.handler.peerPolicy.Snapshot(wp.ID())
		for range wit2MisbehaviorStrikeLimit {
			require.NoError(t, p2p.Send(remote, wit.SignedNewWitnessHashesMsg, &wit.SignedNewWitnessHashesPacket{
				Announcements: []wit.SignedWitnessAnnouncement{{Signature: make([]byte, wit.SignatureLength)}},
			}))
			require.NoError(t, policyAdmissionResult(t, handled))
			require.Equal(t, before, h.handler.peerPolicy.Snapshot(wp.ID()), "unadmitted WIT must not create evidence")
			require.Equal(t, uint64(20), h.handler.peerPolicy.Snapshot("retained-0").Risk, "retained history was evicted")
		}
	}
}

func TestPeerPolicyWitnessPendingReconnectPreservesHistory(t *testing.T) {
	h := newPolicyAdmissionHandler(t, new(mclock.Simulated), 10)
	id := enode.ID{1}
	h.handler.peerPolicy.Connected(id.String())
	h.handler.observePeer(id.String(), peerpolicy.RateLimited)
	before := h.handler.peerPolicy.Snapshot(id.String())
	wp, remote, handled := pendingPolicyWitness(t, h, id)
	require.NoError(t, p2p.Send(remote, wit.SignedNewWitnessHashesMsg, &wit.SignedNewWitnessHashesPacket{
		Announcements: []wit.SignedWitnessAnnouncement{{Signature: make([]byte, wit.SignatureLength)}},
	}))
	require.NoError(t, policyAdmissionResult(t, handled))
	require.Equal(t, before, h.handler.peerPolicy.Snapshot(wp.ID()))
}

func TestPeerPolicyAdmittedWitnessRecordsFault(t *testing.T) {
	h := newPolicyAdmissionHandler(t, new(mclock.Simulated), 10)
	session := newPolicyWitnessSession(t, h, enode.ID{1})
	require.NoError(t, session.remote.Handshake(1, h.chain, h.handler.blockRange.currentRange()))
	select {
	case <-session.admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("ETH admission did not finish")
	}
	for range wit2MisbehaviorStrikeLimit {
		require.NoError(t, p2p.Send(session.witRW, wit.SignedNewWitnessHashesMsg, &wit.SignedNewWitnessHashesPacket{
			Announcements: []wit.SignedWitnessAnnouncement{{Signature: make([]byte, wit.SignatureLength)}},
		}))
		require.NoError(t, policyAdmissionResult(t, session.handled))
	}
	got := h.handler.peerPolicy.Snapshot(session.peer.ID())
	require.Equal(t, uint64(60), got.Risk)
	require.Equal(t, uint64(1), got.Windows[peerpolicy.InvalidWitnessAnnouncement.String()])
	require.Equal(t, uint64(1), got.Windows[peerpolicy.LegacyJail.String()])
}

func TestPeerPolicyWitnessDelayedEvidence(t *testing.T) {
	for _, path := range []string{"announcement", "body", "deferred-announcement", "evicted-announcement"} {
		t.Run(path, func(t *testing.T) {
			h := newContributionTestHandler(t, new(mclock.Simulated), false)
			wp, cleanup := registerEthWitPeer(t, h, wit.WIT2)
			cleanup()
			if path == "evicted-announcement" {
				for i := range 1024 {
					h.handler.observePeer(fmt.Sprint(i), peerpolicy.RateLimited)
				}
				require.False(t, h.handler.peerPolicy.Snapshot(wp.ID()).Contribution.ConnectionKnown)
			}
			reason := peerpolicy.InvalidWitnessAnnouncement
			for range wit2MisbehaviorStrikeLimit {
				switch path {
				case "body":
					h.handler.strikeWit2PeerByID(wp.ID())
					reason = peerpolicy.InvalidWitnessBody
				case "deferred-announcement":
					h.handler.strikeWit2AnnouncementByID(wp.ID())
				default:
					h.handler.strikeWit2Peer(wp)
				}
			}
			got := h.handler.peerPolicy.Snapshot(wp.ID())
			require.Equal(t, uint64(60), got.Risk)
			require.Equal(t, uint64(1), got.Windows[reason.String()])
			require.Equal(t, uint64(1), got.Windows[peerpolicy.LegacyJail.String()])
		})
	}
}

func TestPeerPolicyUnknownWitnessOutcomes(t *testing.T) {
	h := &handler{peers: newPeerSet(), wit2PeerTracker: newPeerWit2Tracker(), peerPolicy: peerpolicy.New(new(mclock.Simulated))}
	for _, strike := range []func(string){h.strikeWit2PeerByID, h.strikeWit2AnnouncementByID} {
		for range wit2MisbehaviorStrikeLimit {
			strike("unknown")
		}
		require.Empty(t, h.peerPolicy.Snapshot("unknown").Windows)
	}
	h.observeWitnessPeer(nil)
	(&handler{}).observeWitnessPeer(nil)
}

func TestPeerPolicyLegacyJailStillEnforces(t *testing.T) {
	setup := setupJailPeerTest(t, true)
	h := &handler{p2pServer: setup.p2pServer, peerPolicy: peerpolicy.New(new(mclock.Simulated))}
	id := enode.ID{1}
	h.jailPeer(id.String())
	require.True(t, wit2PeerIsJailed(t, setup.p2pServer, id))
	require.Equal(t, uint64(1), h.peerPolicy.Snapshot(id.String()).Windows[peerpolicy.LegacyJail.String()])
}
