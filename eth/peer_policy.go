// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package eth

import (
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/eth/fetcher"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func (h *handler) initPeerPolicy(enabled bool) {
	if !enabled {
		return
	}
	h.peerPolicy = peerpolicy.New(mclock.System{})
	h.downloader.SetFailureObserver(func(id string) { h.observePeer(id, peerpolicy.DownloaderFailure) })
	h.txFetcher.SetValidationObserver(func(id string, provenance fetcher.DeliveryProvenance, items, bytes uint64, invalid bool) {
		e := peerpolicy.Evidence{Items: items, Bytes: bytes}
		if provenance == fetcher.UnknownDelivery {
			h.observePeer(id, peerpolicy.UnknownTransactionDelivery)
		}
		if provenance == fetcher.UnsolicitedDelivery {
			e.Family = peerpolicy.Transactions
		}
		if invalid {
			e.Reason = peerpolicy.InvalidTransaction
		}
		h.observePeerEvidence(id, e)
	})
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
	h.peerPolicy.SetExempt(peer.ID(), peer.Trusted() || peer.Static())
	peer.SetObserver(func(e peerpolicy.Evidence) {
		h.peerPolicy.SetExempt(peer.ID(), peer.Trusted() || peer.Static())
		h.peerPolicy.Observe(peer.ID(), e)
	})
}

func (h *handler) dropFetcherPeer(id string) {
	h.observePeer(id, peerpolicy.FetcherDrop)
	h.removePeer(id)
}

func (h *handler) observePeerEvidence(id string, event peerpolicy.Evidence) {
	if h.peers != nil {
		if peer := h.peers.peer(id); peer != nil {
			h.peerPolicy.SetExempt(id, peer.Trusted() || peer.Static())
		}
	}
	h.peerPolicy.Observe(id, event)
}
