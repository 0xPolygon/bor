package eth

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/stretchr/testify/require"
)

func deferPolicyWitness(t *testing.T, h *testHandler, session *policyWitnessSession, number int64) *types.Header {
	t.Helper()
	header := &types.Header{Number: big.NewInt(number)}
	ann := wit.SignedWitnessAnnouncement{BlockHash: header.Hash(), BlockNumber: uint64(number + 1), WitnessSize: 1024}
	signTestAnnouncement(t, &ann)
	require.NoError(t, p2p.Send(session.witRW, wit.SignedNewWitnessHashesMsg, &wit.SignedNewWitnessHashesPacket{
		Announcements: []wit.SignedWitnessAnnouncement{ann},
	}))
	require.NoError(t, policyAdmissionResult(t, session.handled))
	require.True(t, h.handler.deferredAnnounces.has(header.Hash()))
	h.handler.drainDeferredAnnouncesFor(header.Hash())
	require.True(t, h.handler.deferredAnnounces.has(header.Hash()), "unknown header must requeue the same source")
	return header
}

func TestPeerPolicyDeferredWitnessUsesOriginatingAdmission(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "later-admitted"}[accepted], func(t *testing.T) {
			h := newPolicyAdmissionHandler(t, new(mclock.Simulated), 10)
			id := enode.ID{1}
			h.handler.peerPolicy.Connected(id.String())
			h.handler.observePeer(id.String(), peerpolicy.RateLimited)
			before := h.handler.peerPolicy.Snapshot(id.String()).Windows
			session := newPolicyWitnessSession(t, h, id)
			var headers []*types.Header
			for i := range wit2MisbehaviorStrikeLimit {
				headers = append(headers, deferPolicyWitness(t, h, session, int64(100+i)))
			}
			network := uint64(2)
			if accepted {
				network = 1
			}
			err := session.remote.Handshake(network, h.chain, h.handler.blockRange.currentRange())
			if accepted {
				require.NoError(t, err)
				select {
				case <-session.admitted:
				case <-time.After(5 * time.Second):
					t.Fatal("ETH admission did not finish")
				}
			} else {
				require.Error(t, err)
			}
			for _, header := range headers {
				rawdb.WriteHeader(h.chain.DB(), header)
				h.handler.drainDeferredAnnouncesFor(header.Hash())
			}
			got := h.handler.peerPolicy.Snapshot(id.String())
			require.Equal(t, uint64(20), got.Risk)
			require.Equal(t, before, got.Windows, "a retained admitted identity must not authorize an unadmitted source session")
		})
	}
}

func TestPeerPolicyAdmittedDeferredWitnessAfterDisconnect(t *testing.T) {
	h := newPolicyAdmissionHandler(t, new(mclock.Simulated), 10)
	session := newPolicyWitnessSession(t, h, enode.ID{1})
	require.NoError(t, session.remote.Handshake(1, h.chain, h.handler.blockRange.currentRange()))
	select {
	case <-session.admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("ETH admission did not finish")
	}
	var headers []*types.Header
	for i := range wit2MisbehaviorStrikeLimit {
		headers = append(headers, deferPolicyWitness(t, h, session, int64(100+i)))
	}
	h.handler.unregisterPeer(session.peer.ID())
	for i := range 1024 {
		h.handler.observePeer(fmt.Sprint(i), peerpolicy.RateLimited)
	}
	require.False(t, h.handler.peerPolicy.Snapshot(session.peer.ID()).Contribution.ConnectionKnown)
	for _, header := range headers {
		rawdb.WriteHeader(h.chain.DB(), header)
		h.handler.drainDeferredAnnouncesFor(header.Hash())
	}
	got := h.handler.peerPolicy.Snapshot(session.peer.ID())
	require.Equal(t, uint64(60), got.Risk)
	require.Equal(t, uint64(1), got.Windows[peerpolicy.InvalidWitnessAnnouncement.String()])
	require.Equal(t, uint64(1), got.Windows[peerpolicy.LegacyJail.String()])
}

func TestPeerPolicyDeferredWitnessRefreshReplacesObserver(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(fmt.Sprint(admitted), func(t *testing.T) {
			cache := newDeferredAnnounceCache(10)
			ann := wit.SignedWitnessAnnouncement{}
			var calls int
			observe := func(peerpolicy.Reason) { calls++ }
			previous, next := observe, (func(peerpolicy.Reason))(nil)
			if admitted {
				previous, next = next, previous
			}
			cache.putObserved(ann, "peer", previous)
			cache.putObserved(ann, "peer", next)
			entries, ok := cache.take(ann.BlockHash)
			require.True(t, ok)
			require.Len(t, entries, 1)
			if admitted {
				require.NotNil(t, entries[0].observe)
				entries[0].observe(peerpolicy.InvalidWitnessAnnouncement)
				require.Equal(t, 1, calls)
			} else {
				require.Nil(t, entries[0].observe)
			}
		})
	}
}
