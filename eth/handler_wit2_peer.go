package eth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

// WIT evidence follows the existing shared strike threshold. Below-threshold
// observations remain diagnostic and do not bypass the legacy tolerance.
func (h *handler) strikeWit2Peer(peer *wit.Peer) {
	h.strikeWit2Evidence(peer.ID(), peerpolicy.WitnessAnnouncementStrike, peerpolicy.InvalidWitnessAnnouncement, peer.Observe)
}

func (h *handler) strikeWit2PeerByID(id string) {
	h.strikeWit2Evidence(id, peerpolicy.WitnessBodyStrike, peerpolicy.InvalidWitnessBody, func(reason peerpolicy.Reason) {
		if h.peerPolicy != nil {
			h.peerPolicy.ObserveAdmitted(id, reason)
		}
	})
}

func (h *handler) strikeWit2Announcement(id string, observe func(peerpolicy.Reason)) {
	h.strikeWit2Evidence(id, peerpolicy.WitnessAnnouncementStrike, peerpolicy.InvalidWitnessAnnouncement, observe)
}

func (h *handler) strikeWit2Evidence(id string, diagnostic, qualified peerpolicy.Reason, observe func(peerpolicy.Reason)) {
	if observe == nil {
		observe = func(peerpolicy.Reason) {}
	}
	observe(diagnostic)
	if h.wit2PeerTracker == nil || !h.wit2PeerTracker.strike(id) {
		return
	}
	observe(qualified)
	wit2StrikeDisconnectMeter.Mark(1)
	log.Warn("Disconnecting WIT2 peer after strike threshold", "peer", id, "reason", qualified.String())
	observe(peerpolicy.LegacyJail)
	h.jailPeerNative(id)
	h.removePeer(id)
}

// deferredAnnouncesLoop re-evaluates any deferred WIT2 announcements whose
// matching block has just been imported. Exits cleanly when the chain-head
// subscription returns (chain stop) or quitSync is closed.
func (h *handler) deferredAnnouncesLoop() {
	defer h.wg.Done()
	defer h.wit2HeadSub.Unsubscribe()

	for {
		select {
		case ev, ok := <-h.wit2HeadCh:
			if !ok {
				return
			}
			if ev.Header != nil {
				h.onBlockImported(ev.Header.Hash())
				// A batched insertChain (downloader catch-up) fires a single
				// accumulated ChainHeadEvent for the batch's last block, so
				// draining only the head hash would strand deferred announces for
				// the batch's intermediate blocks until their TTL — silently
				// downgrading those blocks to WIT1 byte-handling. Sweep any
				// deferred hash whose header is now local.
				h.drainResolvedDeferredAnnounces()
			}
		case <-h.wit2HeadSub.Err():
			return
		case <-h.quitSync:
			return
		}
	}
}

// drainResolvedDeferredAnnounces drains every deferred announce whose block
// header has become local. ChainHeadEvent fires once per insertChain batch (for
// the batch's last block), so draining only the head hash would leave announces
// for the batch's intermediate blocks deferred until their TTL. The deferred
// set is bounded (deferredAnnounceCapacity), so this per-event scan is cheap.
func (h *handler) drainResolvedDeferredAnnounces() {
	if h.deferredAnnounces == nil {
		return
	}
	for _, hash := range h.deferredAnnounces.hashes() {
		if h.chain.GetHeaderByHash(hash) == nil {
			continue
		}
		h.onBlockImported(hash)
	}
}

// onBlockImported runs the WIT2 per-imported-block housekeeping for a block whose
// header is now local: promote its deferred signed announce, push the witness to
// any peer that asked for it before we held it, and release the now-redundant
// pre-import body cache entry (the bytes are in chain storage, so GetWitness
// serves them from rawdb). Invoked from the chain-head loop, once per imported
// block. Dropping here rather than on TTL/capacity eviction keeps the body cache
// bounded to genuinely in-flight bodies, as its doc comment promises.
func (h *handler) onBlockImported(blockHash common.Hash) {
	h.drainDeferredAnnouncesFor(blockHash)
	h.flushWitnessWaitersForImported(blockHash)
	if h.pendingWitnessBodies != nil {
		h.pendingWitnessBodies.drop(blockHash)
	}
	if h.witnessSourceExclusions != nil {
		h.witnessSourceExclusions.drop(blockHash)
	}
}
