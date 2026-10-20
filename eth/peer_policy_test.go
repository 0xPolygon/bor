// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package eth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/metrics/prometheus"
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
		h := newContributionTestHandler(t, new(mclock.Simulated), false)
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
	h := newContributionTestHandler(t, new(mclock.Simulated), false)
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

type witStrikeLogCapture struct {
	slog.Handler
	records chan slog.Record
}

func (c *witStrikeLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *witStrikeLogCapture) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelWarn {
		c.records <- record.Clone()
	}
	return nil
}

func TestPeerPolicyWitStrikeWarning(t *testing.T) {
	for _, reason := range []peerpolicy.Reason{peerpolicy.InvalidWitnessAnnouncement, peerpolicy.InvalidWitnessBody} {
		t.Run(reason.String(), func(t *testing.T) {
			capture := &witStrikeLogCapture{Handler: log.DiscardHandler(), records: make(chan slog.Record, 16)}
			previous := log.Root()
			log.SetDefault(log.NewLogger(capture))
			defer log.SetDefault(previous)
			h := &handler{peers: newPeerSet(), wit2PeerTracker: newPeerWit2Tracker(), peerPolicy: peerpolicy.New(new(mclock.Simulated))}
			strike := h.strikeWit2PeerByID
			if reason == peerpolicy.InvalidWitnessAnnouncement {
				strike = h.strikeWit2AnnouncementByID
			}
			for range wit2MisbehaviorStrikeLimit - 1 {
				strike("peer")
			}
			if len(capture.records) != 0 || h.peerPolicy.Snapshot("peer").Risk != 0 {
				t.Fatal("below-threshold strikes must remain neutral and quiet")
			}
			strike("peer")
			if len(capture.records) != 1 || h.peerPolicy.Snapshot("peer").Risk != 60 {
				t.Fatal("qualified strike must emit exactly one warning and its evidence")
			}
			record := <-capture.records
			attrs := make(map[string]string)
			record.Attrs(func(attr slog.Attr) bool { attrs[attr.Key] = attr.Value.String(); return true })
			if record.Level != slog.LevelWarn || attrs["peer"] != "peer" || attrs["reason"] != reason.String() {
				t.Fatalf("warning lacks qualified peer reason: %v", attrs)
			}
			strike("peer")
			if len(capture.records) != 0 {
				t.Fatal("next below-threshold strike emitted an extra warning")
			}
		})
	}
}

func TestPeerPolicyInvalidHeaderClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want uint64
	}{
		{"invalid block number", consensus.ErrInvalidNumber, 60},
		{"wrapped invalid block number", fmt.Errorf("verify header: %w", consensus.ErrInvalidNumber), 60},

		{"no error", nil, 0},
		{"future block", consensus.ErrFutureBlock, 0},
		{"unknown ancestor", consensus.ErrUnknownAncestor, 0},
		{"pruned ancestor", consensus.ErrPrunedAncestor, 0},
		{"unexpected withdrawals", consensus.ErrUnexpectedWithdrawals, 0},
		{"unknown validators", errors.New("unknown validators"), 0},
		{"invalid validator list", errors.New("invalid validator list on sprint end block"), 0},
		{"cancelled context", context.Canceled, 0},
		{"wrapped cancelled context", fmt.Errorf("snapshot: %w", context.Canceled), 0},
		{"local insertion failure", errors.New("leveldb: closed"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
			h.observeInvalidHeader("peer", tc.err)

			if got := h.peerPolicy.Snapshot("peer"); got.Risk != tc.want {
				t.Fatalf("risk %d, want %d: %+v", got.Risk, tc.want, got)
			}
		})
	}
}

// Error text alone must not decide anything: the classifier compares sentinels.
func TestPeerPolicyInvalidHeaderIgnoresLookalikeText(t *testing.T) {
	h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
	h.observeInvalidHeader("peer", errors.New(consensus.ErrInvalidNumber.Error()))

	if got := h.peerPolicy.Snapshot("peer"); got.Risk != 0 {
		t.Fatalf("a message resembling a sentinel was scored: %+v", got)
	}
}

func TestPeerPolicyGaugeLifecycle(t *testing.T) {
	metrics.Enable()
	for _, enabled := range []bool{false, true} {
		h := newTestHandlerWithConfig(func(config *handlerConfig) *handlerConfig {
			config.peerReputation = enabled
			return config
		})
		gauge, ok := metrics.DefaultRegistry.Get("eth/peerpolicy/peer_risk").(*metrics.GaugeVec)
		if ok != enabled {
			h.close()
			t.Fatalf("enabled=%v registered=%v", enabled, ok)
		}
		if enabled {
			h.handler.observePeer("test-peer", peerpolicy.InvalidBlock)
			if gauge.Snapshot()["test-peer"] != 60 {
				h.close()
				t.Fatal("handler evidence not exported")
			}
		}
		h.close()
		if metrics.DefaultRegistry.Get("eth/peerpolicy/peer_risk") != nil {
			t.Fatal("shutdown retained the collector")
		}
	}
}

func TestPeerPolicyGaugeCollectionExpiry(t *testing.T) {
	clock := new(mclock.Simulated)
	h := &handler{peerPolicy: peerpolicy.New(clock)}
	registry := metrics.NewRegistry()
	if err := h.peerPolicy.RegisterPeerMetrics(registry); err != nil {
		t.Fatal(err)
	}
	defer h.peerPolicy.UnregisterPeerMetrics()
	h.observePeer("risky-peer", peerpolicy.InvalidBlock)
	h.observePeer("clean-peer", peerpolicy.FetcherDrop)
	h.peerPolicy.SetExempt("trusted-peer", true)
	h.observePeer("trusted-peer", peerpolicy.InvalidBlock)
	for _, scrape := range []func() string{
		func() string {
			recorder := httptest.NewRecorder()
			prometheus.Handler(registry).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
			return recorder.Body.String()
		},
		func() string {
			encoded, err := json.Marshal(registry)
			if err != nil {
				t.Fatal(err)
			}
			return string(encoded)
		},
	} {
		h.observePeer("risky-peer", peerpolicy.InvalidBlock)
		got := scrape()
		if !strings.Contains(got, "risky-peer") || strings.Contains(got, "clean-peer") || strings.Contains(got, "trusted-peer") {
			t.Fatalf("unexpected exported identities: %s", got)
		}
		clock.Run(time.Minute)
		if got := scrape(); strings.Contains(got, "risky-peer") {
			t.Fatalf("expired identity still exported: %s", got)
		}
	}
}

type peerMetricLogCapture struct {
	slog.Handler
	failures atomic.Int64
}

func (h *peerMetricLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *peerMetricLogCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "Failed to register peer reputation gauges" {
		h.failures.Add(1)
	}
	return nil
}

func TestPeerPolicyGaugeRegistrationDiagnostic(t *testing.T) {
	metrics.Enable()
	capture := &peerMetricLogCapture{Handler: log.DiscardHandler()}
	previous := log.Root()
	log.SetDefault(log.NewLogger(capture))
	defer log.SetDefault(previous)
	config := func(c *handlerConfig) *handlerConfig { c.peerReputation = true; return c }
	first := newTestHandlerWithConfig(config)
	defer first.close()
	if capture.failures.Load() != 0 {
		t.Fatal("successful registration logged a failure")
	}
	second := newTestHandlerWithConfig(config)
	second.close()
	if capture.failures.Load() != 1 {
		t.Fatal("registration conflict not reported")
	}
	if metrics.DefaultRegistry.Get("eth/peerpolicy/peer_risk") == nil {
		t.Fatal("conflicting handler removed the first collector")
	}
}
