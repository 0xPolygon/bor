package eth

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/eth/downloader"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

func newPolicyAdmissionHandler(t *testing.T, clock mclock.Clock, maxPeers int) *testHandler {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	chain, err := core.NewBlockChain(db, &core.Genesis{Config: params.TestChainConfig}, ethash.NewFaker(), nil)
	require.NoError(t, err)
	pool := newTestTxPool()
	backend, err := newHandler(&handlerConfig{Database: db, Chain: chain, TxPool: pool,
		Network: 1, Sync: downloader.FullSync, BloomCache: 1})
	require.NoError(t, err)
	backend.peerPolicy = peerpolicy.New(clock)
	backend.peerPolicy.SetClassResolver(backend.peerPolicyClass)
	h := &testHandler{db: db, chain: chain, txpool: pool, handler: backend}
	backend.Start(maxPeers)
	t.Cleanup(h.close)
	return h
}

func policyAdmissionPeers(t *testing.T, h *testHandler, version uint, id enode.ID) (*ethproto.Peer, *ethproto.Peer, *p2p.MsgPipeRW) {
	t.Helper()
	remoteRW, localRW := p2p.MsgPipe()
	t.Cleanup(func() { require.NoError(t, remoteRW.Close()) })
	remote := ethproto.NewPeer(version, p2p.NewPeerPipe(enode.ID{255}, "", nil, remoteRW), remoteRW, h.txpool)
	local := ethproto.NewPeer(version, p2p.NewPeerPipe(id, "", nil, localRW), localRW, h.txpool)
	t.Cleanup(remote.Close)
	t.Cleanup(local.Close)
	return remote, local, remoteRW
}

func policyAdmissionResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("ETH admission did not finish")
		return nil
	}
}

func TestPeerPolicyRejectedAdmissionPreservesHistory(t *testing.T) {
	for _, version := range []uint{ethproto.ETH68, ethproto.ETH69} {
		for _, rejection := range []string{"handshake", "capacity"} {
			t.Run(fmt.Sprintf("eth%d/%s", version, rejection), func(t *testing.T) {
				h := newPolicyAdmissionHandler(t, new(mclock.Simulated), 1)
				if rejection == "capacity" {
					occupant := ethproto.NewPeer(version, p2p.NewPeer(enode.ID{254}, "", nil), policyFaultRW{}, nil)
					t.Cleanup(occupant.Close)
					require.NoError(t, h.handler.peers.registerPeer(occupant, nil, nil))
				}
				for i := range 1024 {
					h.handler.peerPolicy.Observe(fmt.Sprintf("retained-%d", i), peerpolicy.Evidence{Reason: peerpolicy.RateLimited})
				}
				for attempt := range 3 {
					remote, local, _ := policyAdmissionPeers(t, h, version, enode.ID{byte(attempt + 1)})
					result := make(chan error, 1)
					go func() {
						result <- h.handler.runEthPeer(local, func(*ethproto.Peer) error {
							t.Error("rejected peer entered the message handler")
							return nil
						})
					}()
					networkID := uint64(1)
					if rejection == "handshake" {
						networkID++
					}
					err := remote.Handshake(networkID, h.chain, h.handler.blockRange.currentRange())
					if rejection == "handshake" {
						require.Error(t, err)
						require.ErrorContains(t, policyAdmissionResult(t, result), "network ID mismatch")
					} else {
						require.NoError(t, err)
						require.ErrorIs(t, policyAdmissionResult(t, result), p2p.DiscTooManyPeers)
					}
					require.False(t, h.handler.peerPolicy.Snapshot(local.ID()).Contribution.ConnectionKnown)
					require.Equal(t, uint64(20), h.handler.peerPolicy.Snapshot("retained-0").Risk,
						"rejected connection churn must not evict retained evidence")
				}
			})
		}
	}
}

func TestPeerPolicyRejectedReconnectPreservesContribution(t *testing.T) {
	clock := new(mclock.Simulated)
	h := newPolicyAdmissionHandler(t, clock, 10)
	remote, local, _ := policyAdmissionPeers(t, h, ethproto.ETH69, enode.ID{1})
	h.handler.peerPolicy.Connected(local.ID())
	h.handler.peerPolicy.Observe(local.ID(), peerpolicy.Evidence{
		Reason: peerpolicy.RateLimited, Activity: peerpolicy.ActivityEvent{ReceivedMessages: 7, ServedResponses: 2},
	})
	clock.Run(10 * time.Second)
	before := h.handler.peerPolicy.Snapshot(local.ID())
	result := make(chan error, 1)
	go func() {
		result <- h.handler.runEthPeer(local, func(*ethproto.Peer) error {
			t.Error("rejected reconnect entered the message handler")
			return nil
		})
	}()
	require.Error(t, remote.Handshake(2, h.chain, h.handler.blockRange.currentRange()))
	require.Error(t, policyAdmissionResult(t, result))
	require.Equal(t, before, h.handler.peerPolicy.Snapshot(local.ID()), "failed reconnect must not reset an earlier accepted session")
}

func TestPeerPolicyAcceptedAdmissionObservesFirstMessage(t *testing.T) {
	for _, version := range []uint{ethproto.ETH68, ethproto.ETH69} {
		t.Run(fmt.Sprintf("eth%d", version), func(t *testing.T) {
			clock := new(mclock.Simulated)
			h := newPolicyAdmissionHandler(t, clock, 10)
			remote, local, remoteRW := policyAdmissionPeers(t, h, version, enode.ID{1})
			h.handler.peerPolicy.Connected(local.ID())
			h.handler.peerPolicy.Observe(local.ID(), peerpolicy.Evidence{
				Reason: peerpolicy.RateLimited, Activity: peerpolicy.ActivityEvent{ReceivedMessages: 7},
			})
			clock.Run(10 * time.Second)
			admitted := make(chan peerpolicy.Snapshot, 1)
			result := make(chan error, 1)
			go func() {
				result <- h.handler.runEthPeer(local, func(peer *ethproto.Peer) error {
					admitted <- h.handler.peerPolicy.Snapshot(peer.ID())
					return ethproto.Handle((*ethHandler)(h.handler), peer)
				})
			}()
			require.NoError(t, remote.Handshake(1, h.chain, h.handler.blockRange.currentRange()))
			select {
			case got := <-admitted:
				require.True(t, got.Contribution.ConnectionKnown)
				require.Zero(t, got.Contribution.ReceivedMessages, "accepted reconnect starts a fresh contribution session")
				require.Equal(t, uint64(20), got.Risk, "accepted reconnect retains misconduct history")
			case <-time.After(5 * time.Second):
				t.Fatal("accepted peer did not enter the message handler")
			}
			require.NoError(t, p2p.Send(remoteRW, 255, []byte{}))
			require.Error(t, policyAdmissionResult(t, result))
			got := h.handler.peerPolicy.Snapshot(local.ID())
			require.Equal(t, uint64(80), got.Risk)
			require.Equal(t, uint64(1), got.Contribution.ReceivedMessages)
			require.Equal(t, uint64(1), got.Windows[peerpolicy.InvalidEncoding.String()])
		})
	}
}
