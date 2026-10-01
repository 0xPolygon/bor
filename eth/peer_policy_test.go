package eth

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
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
		t.Fatal("observation enabled by default")
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
		config.peerReputation = true
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
	if info.Reputation == nil || info.Reputation.Risk != 60 {
		t.Fatalf("missing peer information: %+v", info)
	}
	items := metrics.GetOrRegisterCounter("eth/peerpolicy/items/transactions", nil)
	before := items.Snapshot().Count()
	tx := types.NewTx(&types.LegacyTx{})
	if err := h.handler.txFetcher.Enqueue("sender", []*types.Transaction{tx}, false, 0); err != nil {
		t.Fatal(err)
	}
	if got := h.handler.peerPolicy.Snapshot("sender"); got.Risk != 60 {
		t.Fatalf("missing validation evidence: %+v", got)
	}
	if items.Snapshot().Count()-before != 1 {
		t.Fatal("unsolicited transaction not counted")
	}
	h.handler.dropFetcherPeer("missing")
	if got := h.handler.peerPolicy.Snapshot("missing"); got.Risk != 0 || got.Windows[peerpolicy.FetcherDrop.String()] != 1 {
		t.Fatalf("wrong drop evidence: %+v", got)
	}
}
