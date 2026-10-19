// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package eth

import (
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/eth/fetcher"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

// A stale local view cannot establish whether a peer had useful data to relay.
const (
	contributionHeadMaxAge = 2 * time.Minute
	// Bor's early announcements permit validated heads ahead of the local clock.
	contributionHeadFutureTolerance = 30 * time.Second
)

func (h *handler) initPeerPolicy(enabled bool) {
	if !enabled {
		return
	}
	h.peerPolicy = peerpolicy.New(mclock.System{})
	h.peerPolicy.SetClassResolver(h.peerPolicyClass)
	if metrics.Enabled() {
		if err := h.peerPolicy.RegisterPeerMetrics(metrics.DefaultRegistry); err != nil {
			log.Error("Failed to register peer reputation gauges", "err", err)
		}
	}
	h.downloader.SetFailureObserver(func(id string) { h.observePeer(id, peerpolicy.DownloaderFailure) })
	h.txFetcher.SetValidationObserver(h.observeTransactionDelivery)
	h.updateContributionReadiness()
}

// The tracker holds its mutex during this lookup, then briefly takes the peerset
// read lock. Peerset-locked code must not call the tracker. Trusted/Static read
// atomic connection flags without calling Peer.Info or taking server locks.
func (h *handler) peerPolicyClass(id string) (exempt, connected bool) {
	if h.peers != nil {
		if peer := h.peers.peer(id); peer != nil {
			return peer.Trusted() || peer.Static(), true
		}
	}
	return false, false
}

func (h *handler) observePeer(id string, reason peerpolicy.Reason) {
	if h.peerPolicy != nil {
		h.observePeerEvidence(id, peerpolicy.Evidence{Reason: reason})
	}
}

func (h *handler) observeProtocolPeer(peer *eth.Peer) {
	if h.peerPolicy == nil {
		return
	}
	startedAt := h.peerPolicy.Connected(peer.ID())
	h.peerPolicy.SetExempt(peer.ID(), peer.Trusted() || peer.Static())
	peer.SetObserver(func(e peerpolicy.Evidence) {
		if !h.peerPolicy.ObserveSession(peer.ID(), peer.Trusted() || peer.Static(), startedAt, e) {
			e.Activity.StartedAt = startedAt
			h.observePeerEvidence(peer.ID(), e)
		}
	})
}

func (h *handler) dropFetcherPeer(id string) {
	h.observePeer(id, peerpolicy.FetcherDrop)
	h.removePeer(id)
}

func (h *handler) observePeerEvidence(id string, event peerpolicy.Evidence) {
	if h.peers != nil {
		if peer := h.peers.peer(id); peer != nil {
			h.peerPolicy.ObserveWithClass(id, peer.Trusted() || peer.Static(), event)
			return
		}
	}
	h.peerPolicy.Observe(id, event)
}

func (h *handler) observeTransactionDelivery(id string, provenance fetcher.DeliveryProvenance, items, bytes uint64, invalid bool, accepted uint64, complete bool, receivedAt mclock.AbsTime) {
	e := peerpolicy.Evidence{Items: items, Bytes: bytes}
	e.Activity.StartedAt = receivedAt
	if complete && provenance != fetcher.UnknownDelivery {
		e.Activity.AcceptedTransactions = accepted
	} else {
		e.Activity.UnclassifiedMessages = 1
	}
	if provenance == fetcher.UnknownDelivery {
		h.observePeer(id, peerpolicy.UnknownTransactionDelivery)
	}
	// Missing provenance is neutral, but the received bytes still consume capacity.
	if provenance != fetcher.RequestedDelivery {
		e.Family = peerpolicy.Transactions
	}
	if invalid {
		e.Reason = peerpolicy.InvalidTransaction
	}
	h.observePeerEvidence(id, e)
}

// Readiness gates negative contribution classifications; it never gates traffic.
func (h *handler) updateContributionReadiness() {
	if h.peerPolicy == nil {
		return
	}
	h.policyMu.Lock()
	defer h.policyMu.Unlock()
	h.updateContributionReadinessLocked(time.Now().Unix())
}

func (h *handler) updateContributionReadinessLocked(now int64) {
	ready := h.synced.Load() && !h.snapSync.Load() && !h.statelessSync.Load() && !h.disableTxPropagation
	if h.policySyncing || h.chain == nil {
		ready = false
	} else {
		head := h.chain.CurrentBlock()
		if head == nil || !contributionHeadFresh(head.Time, now) {
			ready = false
		}
	}
	h.peerPolicy.SetReady(ready)
}

func contributionHeadFresh(headTime uint64, now int64) bool {
	if now < 0 {
		return false
	}
	if headTime > uint64(now) {
		return headTime-uint64(now) <= uint64(contributionHeadFutureTolerance/time.Second)
	}
	return uint64(now)-headTime <= uint64(contributionHeadMaxAge/time.Second)
}

func (h *handler) setContributionSyncing(syncing bool) {
	if h.peerPolicy == nil {
		return
	}
	h.policyMu.Lock()
	defer h.policyMu.Unlock()
	h.policySyncing = syncing
	h.updateContributionReadinessLocked(time.Now().Unix())
}
