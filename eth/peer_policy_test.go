package eth

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/rawdb"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyObservationDoesNotEnforce(t *testing.T) {
	clock := new(mclock.Simulated)
	h := &handler{peerPolicy: peerpolicy.New(clock)}
	h.observePeer("peer", peerpolicy.InvalidWitnessAnnouncement)
	clock.Run(10 * time.Second)
	h.observePeer("peer", peerpolicy.InvalidWitnessBody)
	// No peerset or P2P server is installed: calling either would fail this test.
	got := h.peerPolicy.Snapshot("peer")
	if got.Risk != 100 || got.Action != "jail" {
		t.Fatalf("missing shadow jail: %+v", got)
	}
}

func TestPeerPolicyDisabled(t *testing.T) {
	h := &handler{}
	h.initPeerPolicy(false)
	h.observeProtocolPeer(nil)
	h.observePeer("peer", peerpolicy.InvalidBlock)
	if h.peerPolicy != nil {
		t.Fatal("explicit opt-out ignored")
	}
}

type rejectingPolicyPool struct{ *testTxPool }

func (p *rejectingPolicyPool) Add(txs []*types.Transaction, _ bool) []error {
	result := make([]error, len(txs))
	for i := range result {
		result[i] = txpool.ErrInvalidSender
	}
	return result
}

func TestPeerPolicyHandlerIntegration(t *testing.T) {
	h := newTestHandlerWithConfig(func(config *handlerConfig) *handlerConfig {
		config.peerReputation = ethconfig.Defaults.PeerReputation
		config.TxPool = &rejectingPolicyPool{newTestTxPool()}
		return config
	})
	defer h.close()
	if h.handler.peerPolicy == nil {
		t.Fatal("observer not initialized")
	}
	peer, cleanup := registerEthWitPeer(t, h, wit.WIT2)
	defer cleanup()
	h.handler.strikeWit2Peer(peer)
	info := (*ethHandler)(h.handler).PeerInfo(peer.Peer.ID()).(*ethPeerInfo)
	if info.Reputation == nil || info.Reputation.Risk != 0 || info.Reputation.Windows[peerpolicy.WitnessAnnouncementStrike.String()] != 1 {
		t.Fatalf("missing peer information: %+v", info)
	}
	items := metrics.GetOrRegisterCounter("eth/peerpolicy/items/transactions", nil)
	before := items.Snapshot().Count()
	tx := types.NewTx(&types.LegacyTx{})
	if err := h.handler.txFetcher.Enqueue("sender", []*types.Transaction{tx}, false, 0); err != nil {
		t.Fatal(err)
	}
	require.Eventually(t, func() bool {
		return h.handler.peerPolicy.Snapshot("sender").Risk == 60
	}, time.Second, time.Millisecond)
	if items.Snapshot().Count()-before != 1 {
		t.Fatal("unsolicited transaction not counted")
	}
	h.handler.dropFetcherPeer("missing")
	if got := h.handler.peerPolicy.Snapshot("missing"); got.Risk != 0 || got.Windows[peerpolicy.FetcherDrop.String()] != 1 {
		t.Fatalf("wrong drop evidence: %+v", got)
	}
}

func TestPeerPolicyTimedJailEvidence(t *testing.T) {
	h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
	h.jailPeerFor("peer", time.Minute)
	got := h.peerPolicy.Snapshot("peer")
	if got.Risk != 0 || got.Windows[peerpolicy.LegacyJail.String()] != 1 {
		t.Fatalf("missing diagnostic: %+v", got)
	}
}

func TestPeerPolicyWitStrikeThreshold(t *testing.T) {
	for _, announcement := range []bool{false, true} {
		h := newTestHandler()
		defer h.close()
		h.handler.peerPolicy = peerpolicy.New(new(mclock.Simulated))
		peer, cleanup := registerEthWitPeer(t, h, wit.WIT2)
		defer cleanup()
		strike := h.handler.strikeWit2PeerByID
		reason := peerpolicy.InvalidWitnessBody
		if announcement {
			strike = h.handler.strikeWit2AnnouncementByID
			reason = peerpolicy.InvalidWitnessAnnouncement
		}
		for range 4 {
			strike(peer.ID())
		}
		if got := h.handler.peerPolicy.Snapshot(peer.ID()); got.Risk != 0 {
			t.Fatalf("tolerated strikes scored: %+v", got)
		}
		strike(peer.ID())
		got := h.handler.peerPolicy.Snapshot(peer.ID())
		if got.Risk != 60 || got.Windows[reason.String()] != 1 {
			t.Fatalf("qualified strike: %+v", got)
		}
	}
}

func TestPeerPolicyUnknownTransactionEvidence(t *testing.T) {
	h := newTestHandlerWithConfig(func(config *handlerConfig) *handlerConfig { config.peerReputation = true; return config })
	defer h.close()
	tx := types.NewTx(&types.LegacyTx{})
	if err := h.handler.txFetcher.Enqueue("unknown", []*types.Transaction{tx}, true, 7); err != nil {
		t.Fatal(err)
	}
	require.Eventually(t, func() bool {
		return h.handler.peerPolicy.Snapshot("unknown").Windows[peerpolicy.UnknownTransactionDelivery.String()] == 1
	}, time.Second, time.Millisecond)
	got := h.handler.peerPolicy.Snapshot("unknown")
	if got.Risk != 0 || got.Windows[peerpolicy.TransactionVolume.String()] != 0 {
		t.Fatalf("unknown reply penalized: %+v", got)
	}
}

func TestPeerPolicyDeferredAnnouncementEvidence(t *testing.T) {
	h := newTestHandler()
	defer h.close()
	h.handler.peerPolicy = peerpolicy.New(new(mclock.Simulated))
	header := &types.Header{Number: big.NewInt(3333)}
	rawdb.WriteHeader(h.chain.DB(), header)
	ann := wit.SignedWitnessAnnouncement{BlockHash: header.Hash(), BlockNumber: 3334}
	signTestAnnouncement(t, &ann)
	for range wit2MisbehaviorStrikeLimit {
		h.handler.deferredAnnounces.put(ann, "relayer")
		h.handler.drainDeferredAnnouncesFor(ann.BlockHash)
	}
	got := h.handler.peerPolicy.Snapshot("relayer")
	if got.Risk != 60 || got.Windows[peerpolicy.InvalidWitnessAnnouncement.String()] != 1 || got.Windows[peerpolicy.InvalidWitnessBody.String()] != 0 {
		t.Fatalf("wrong deferred evidence: %+v", got)
	}
}
