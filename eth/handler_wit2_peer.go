package eth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
)

// This file holds the WIT2-specific handler methods split out of handler.go to
// keep that file focused on the generic eth-handler wiring. The methods are
// unchanged relocations; behaviour is identical to their previous inline
// definitions (Go resolves methods per package, not per file).

// strikeWit2Peer records a wit2 misbehavior strike (bad sig, wrong producer)
// and disconnects the peer once the strike threshold is exceeded inside the
// decay window. Single bad announcements are tolerated to allow for stray
// pre-fork content; sustained misbehavior is not.
func (h *handler) strikeWit2Peer(peer *wit.Peer) {
	if h.wit2PeerTracker == nil {
		return
	}
	if !h.wit2PeerTracker.strike(peer.ID()) {
		return
	}
	wit2StrikeDisconnectMeter.Mark(1)

	if h.shedWit2StrikeWithoutJail(peer.ID(), peer.Log()) {
		return
	}

	peer.Log().Warn("wit2: disconnecting and jailing peer for repeated invalid signed announcements")
	// Jail before disconnecting: removePeer's forget() wipes the strike ledger,
	// so without a jail the peer could re-dial immediately with a clean slate and
	// resume. Jail (enode-keyed, like the byte-verification violation path) holds
	// it off for the jail period. Key rotation still bypasses, but this closes the
	// trivial same-identity reconnect loop.
	h.jailPeer(peer.ID())
	h.removePeer(peer.ID())
}

// shedWit2StrikeWithoutJail disconnects a trusted peer that crossed the strike
// threshold but leaves it out of the jail, reporting whether it handled the
// peer. Trusted peers are our own infrastructure, and the strike conditions
// that reach here are not all self-incriminating: "signer is not the scheduled
// producer" is a judgement about our view of the chain, and the strike lands on
// whoever relayed the announcement rather than whoever signed it. Jailing on
// that can remove a sentry from our own mesh, and it costs more than the jail
// period suggests because nothing re-arms the dial scheduler when a jail lapses
// (dialScheduler.updateStaticPool is only called on a dial completing, a peer
// disconnecting, or dial history expiring). Disconnecting still sheds a
// genuinely broken node: it reconnects, and if it really is misbehaving it
// crosses the threshold again and shows up in the meter.
func (h *handler) shedWit2StrikeWithoutJail(id string, logger log.Logger) bool {
	if h.peerTrusted == nil || !h.peerTrusted(id) {
		return false
	}

	wit2StrikeTrustedShedMeter.Mark(1)
	logger.Warn("wit2: trusted peer crossed the strike threshold, disconnecting without jail", "peer", id)
	h.removePeer(id)

	return true
}

// peerSetTrusted resolves trust through the live peer set. It is the default
// wiring for handler.peerTrusted.
func (h *handler) peerSetTrusted(id string) bool {
	peer := h.peers.peer(id)

	return peer != nil && peer.Peer.Peer.Trusted()
}

// strikeWit2PeerByID records a WIT2 byte-serving strike against the peer with the
// given id and disconnects+jails it once the threshold is crossed. The witness
// manager detects byte-mismatches by peer id (not *wit.Peer), so this is its
// entry point into the same strike budget strikeWit2Peer uses for bad announces:
// sustained misbehavior across either surface disconnects the peer.
func (h *handler) strikeWit2PeerByID(id string) {
	if h.wit2PeerTracker == nil {
		return
	}
	if !h.wit2PeerTracker.strike(id) {
		return
	}
	wit2StrikeDisconnectMeter.Mark(1)

	if h.shedWit2StrikeWithoutJail(id, log.Root()) {
		return
	}

	h.jailPeer(id)
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
}
