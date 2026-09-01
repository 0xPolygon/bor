package eth

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/wit2test"
)

const (
	// witnessRequestTimeout defines how long to wait for an in-flight witness computation.
	witnessRequestTimeout          = 5 * time.Second
	PageSize                       = 15 * 1024 * 1024  // 15 MB
	MaximumCachedWitnessOnARequest = 200 * 1024 * 1024 // 200 MB, the maximum amount of memory a request can demand while getting witness
	MaximumResponseSize            = 16 * 1024 * 1024  // 16 MB, helps to fast fail check
	MaxWitnessMetadataServe        = wit.MaxWitnessMetadataServe
	MaxWitnessServe                = wit.MaxWitnessServe
)

// witHandler implements the eth.Backend interface to handle the various network
// packets that are sent as replies or broadcasts.
type witHandler handler

func (h *witHandler) Chain() *core.BlockChain { return h.chain }

// RunPeer is invoked when a peer joins on the `wit` protocol.
func (h *witHandler) RunPeer(peer *wit.Peer, hand wit.Handler) error {
	return (*handler)(h).runWitExtension(peer, hand)
}

// PeerInfo retrieves all known `wit` information about a peer.
func (h *witHandler) PeerInfo(id enode.ID) interface{} {
	if p := h.peers.peer(id.String()); p != nil {
		if p.witPeer != nil {
			return p.witPeer.info()
		}
	}

	return nil
}

// Handle is invoked from a peer's message handler when it receives a new remote
// message that the handler couldn't consume and serve itself.
func (h *witHandler) Handle(peer *wit.Peer, packet wit.Packet) error {
	log.Debug("witHandler Handle", "packet", packet)
	// Consume any broadcasts and announces, forwarding the rest to the downloader
	switch packet := packet.(type) {
	case *wit.NewWitnessPacket:
		return h.handleWitnessBroadcast(peer, packet.Witness)
	case *wit.NewWitnessHashesPacket:
		return h.handleWitnessHashesAnnounce(peer, packet.Hashes, packet.Numbers)
	case *wit.SignedNewWitnessHashesPacket:
		return h.handleSignedWitnessAnnouncements(peer, packet.Announcements)
	case *wit.GetWitnessPacket:
		// Call handleGetWitness which returns the raw RLP data
		response, err := h.handleGetWitness(peer, packet)
		if err != nil {
			return fmt.Errorf("failed to handle GetWitnessPacket: %w", err)
		}
		// Reply using the retrieved RLP data
		return peer.ReplyWitness(packet.RequestId, &response)

	case *wit.GetWitnessMetadataPacket:
		// Call handleGetWitnessMetadata which returns only metadata (page count)
		response, err := h.handleGetWitnessMetadata(peer, packet)
		if err != nil {
			return fmt.Errorf("failed to handle GetWitnessMetadataPacket: %w", err)
		}
		// Reply with metadata
		return peer.ReplyWitnessMetadata(packet.RequestId, response)

	default:
		return fmt.Errorf("unknown wit packet type %T", packet)
	}
}

// handleWitnessBroadcast handles a witness broadcast from a peer. A broadcast
// witness is only accepted — sender marked as a body-holder, bytes cached,
// witness injected for import — when we can bind it to something we already
// trust: a BP-signed announcement whose witnessHash matches the received
// bytes (WIT2), or a locally known block header (WIT1 fallback). Anything
// else is dropped: bytes contradicting a BP-signed commitment are provably
// wrong and must not bypass the verification the paged-fetch path enforces,
// and an unsigned witness for an unknown header is unverifiable on the
// sender's say-so alone.
func (h *witHandler) handleWitnessBroadcast(peer *wit.Peer, witness *stateless.Witness) error {
	hash := witness.Header().Hash()

	var accepted bool
	if signed, hasSigned := (*handler)(h).signedWitnesses.get(hash); hasSigned {
		accepted = h.acceptSignedBroadcast(peer, witness, hash, signed.WitnessHash)
	} else if (*handler)(h).deferredAnnounces.has(hash) {
		accepted = h.acceptDeferredBroadcast(peer, witness, hash)
	} else {
		accepted = h.acceptUnsignedBroadcast(peer, hash)
	}

	if !accepted {
		return nil
	}

	// Inject the witness into the block fetcher's cache
	if h.blockFetcher != nil {
		log.Debug("Injecting witness into block fetcher", "hash", hash, "peer", peer.ID(), "number", witness.Header().Number)

		if err := h.blockFetcher.InjectWitness(peer.ID(), witness); err != nil {
			peer.Log().Warn("Failed to inject broadcast witness into fetcher", "hash", hash, "err", err)
			// Don't return error, just log, as block might still be importable via other means
		}
	} else {
		// This shouldn't happen in normal operation, but log if it does
		peer.Log().Warn("Block fetcher nil in witHandler, cannot inject witness")
	}

	return nil
}

// encodedBroadcastBytes canonically re-encodes a broadcast witness so its
// bytes can be checked against a signed commitment. Returns ok=false when the
// witness cannot be re-encoded — the caller must treat the broadcast as
// unverifiable rather than letting unchecked bytes through.
func encodedBroadcastBytes(peer *wit.Peer, witness *stateless.Witness, hash common.Hash) ([]byte, bool) {
	var buf bytes.Buffer
	if err := witness.EncodeRLP(&buf); err != nil {
		peer.Log().Warn("wit2: failed to encode received witness", "hash", hash, "err", err)
		return nil, false
	}
	return buf.Bytes(), true
}

// acceptSignedBroadcast is the WIT2 accept path of the witness broadcast:
// verify against the BP-signed witnessHash on file, then cache the encoded
// body so this node can serve it pre-import. We only expose the cache for
// serving when bytes match — otherwise an upstream that lied about the bytes
// would make us serve garbage and get dropped by downstream peers as liars,
// even though we just relayed what we received. On mismatch nothing is
// cached, the sender is not marked as a body-holder, and the witness is not
// injected: the broadcast path must not be a bypass of the byte verification
// the paged-fetch path performs. No disconnect — the sender may itself have
// been fed bad bytes upstream.
func (h *witHandler) acceptSignedBroadcast(peer *wit.Peer, witness *stateless.Witness, hash common.Hash, signedHash common.Hash) bool {
	bodyBytes, ok := encodedBroadcastBytes(peer, witness, hash)
	if !ok {
		return false
	}
	bodyHash := stateless.WitnessCommitHash(bodyBytes)
	if signedHash != bodyHash {
		wit2BroadcastByteMismatchMeter.Mark(1)
		peer.Log().Warn("wit2: broadcast bytes do not match signed witnessHash; dropping",
			"blockHash", hash, "expected", signedHash, "actual", bodyHash)
		return false
	}
	peer.AddKnownWitness(hash)
	(*handler)(h).pendingWitnessBodies.put(hash, bodyBytes, bodyHash)
	// We now hold servable bytes — push to any peer that asked us
	// for this body before we had it.
	(*handler)(h).pushWitnessToWaiters(hash, witness, len(bodyBytes))
	return true
}

// acceptDeferredBroadcast handles a broadcast whose signed announcement is on
// file but still deferred: its producer-binding needs the block header, which
// a stateless node at the tip does not have yet — that is exactly the
// consumer-side state when a waiter push delivers the body for a block
// pending import. Bind the pushed bytes to the deferred commitment and, on
// match, accept for IMPORT ONLY: the witness flows to the block fetcher so
// the pending block can import (import re-verifies everything via stateless
// execution + state-root check). We do NOT cache for serving, do NOT promote
// into signedWitnesses, and do NOT relay — those carry the verified-announce
// trust property, and a deferred entry's producer is unverified until the
// post-import drain checks it against the chain-validated header. Verifying
// against the header embedded in the pushed witness instead would let a peer
// self-seal a fabricated header and pass its own announce as the producer's.
func (h *witHandler) acceptDeferredBroadcast(peer *wit.Peer, witness *stateless.Witness, hash common.Hash) bool {
	bodyBytes, ok := encodedBroadcastBytes(peer, witness, hash)
	if !ok {
		return false
	}
	// Bind against any deferred candidate's commitment: with multiple candidates
	// on file we accept the body if its hash matches one of them. The drain still
	// arbitrates which signer is the real producer at import time.
	bodyHash := stateless.WitnessCommitHash(bodyBytes)
	if !(*handler)(h).deferredAnnounces.hasWitnessHash(hash, bodyHash) {
		wit2BroadcastByteMismatchMeter.Mark(1)
		peer.Log().Warn("wit2: broadcast bytes do not match any deferred announce witnessHash; dropping",
			"blockHash", hash, "actual", bodyHash)
		return false
	}
	peer.AddKnownWitness(hash)
	wit2BroadcastDeferredImportMeter.Mark(1)
	return true
}

// acceptUnsignedBroadcast is the WIT1 fallback with no signed announcement on
// file. The only binding we can check is that the header belongs to a block
// we actually know — without it, an unsolicited 16MB body for an arbitrary
// hash would be decoded and cached purely on the sender's word. Unknown
// headers are dropped silently: a peer racing ahead of our import is early,
// not malicious. For known headers we cannot prove byte-correctness to
// downstream WIT2 peers — the body is not exposed for pre-import serving but
// still flows into the import path.
func (h *witHandler) acceptUnsignedBroadcast(peer *wit.Peer, hash common.Hash) bool {
	if h.Chain().GetHeaderByHash(hash) == nil {
		wit2BroadcastUnknownHeaderDropMeter.Mark(1)
		peer.Log().Debug("dropping witness broadcast for unknown header", "blockHash", hash)
		return false
	}
	peer.AddKnownWitness(hash)
	wit2BroadcastUnverifiedSkippedMeter.Mark(1)
	return true
}

// handleWitnessHashesAnnounce handles a witness hashes broadcast from a peer.
func (h *witHandler) handleWitnessHashesAnnounce(peer *wit.Peer, hashes []common.Hash, numbers []uint64) error {
	for _, hash := range hashes {
		peer.AddKnownWitness(hash)
	}
	return nil
}

// handleSignedWitnessAnnouncements verifies BP signatures on incoming WIT2
// announcements and relays valid ones to peers that have not seen them.
// Body fetches are driven elsewhere (the block fetcher's witness manager
// kicks them off when an announcement materialises). Each announcement is
// processed independently so a single bad entry does not poison a batch.
//
// Failure policy (enforced in acceptSignedAnnouncement): a header-unknown
// announce is deferred silently — no strike, no relay — because it may simply
// be racing ahead of its block. Confirmed misbehavior against a known header
// (bad signature, or signer ≠ scheduled producer) is struck, and a peer that
// reaches wit2MisbehaviorStrikeLimit strikes within the decay window is
// disconnected. Byte-correctness failures at fetch time are handled separately
// in the witness manager. All invalid announcements are also metered.
func (h *witHandler) handleSignedWitnessAnnouncements(peer *wit.Peer, anns []wit.SignedWitnessAnnouncement) error {
	wit2RelayInMeter.Mark(int64(len(anns)))

	if wit2test.Enabled() {
		for _, ann := range anns {
			wit2test.Stamp("RX_SIGNED_ANNOUNCE",
				"peer", peer.ID(),
				"block_hash", ann.BlockHash.Hex(),
				"block_number", ann.BlockNumber,
				"witness_hash", ann.WitnessHash.Hex(),
			)
		}
	}

	// Per-peer rate limit: every announcement consumes one token. Rejected
	// packets are dropped wholesale to keep accounting simple — an honest
	// peer should never trip this in practice.
	if !(*handler)(h).wit2PeerTracker.allow(peer.ID(), len(anns)) {
		wit2RateLimitDropMeter.Mark(int64(len(anns)))
		peer.Log().Debug("wit2: rate-limited signed announcements", "count", len(anns))
		return nil
	}

	for _, ann := range anns {
		if !h.acceptSignedAnnouncement(peer, ann) {
			// Verification failed (bad signature, signer ≠ producer, or
			// header not yet local). MUST NOT mark the sender as
			// announce-known: doing so would (a) suppress our own later
			// re-relay back to this peer if we receive a valid version of
			// the same hash from someone else, and (b) leave us no path
			// to recover from a header-arrival race once a re-gossip for
			// the same hash arrives. Recovery on this branch relies on
			// re-receipt, which the empty knownAnnounces set permits.
			continue
		}

		// Sender produced a valid announcement; suppress relay back to them.
		// Do NOT mark them as a body-holder — they may be relaying without
		// bytes. Body fetches are gated on knownWitnesses, set elsewhere.
		peer.AddKnownAnnounce(ann.BlockHash)

		// Cache + dedup. Skip relay if we've already relayed this hash recently.
		if !h.signedWitnesses.putIfNewer(ann) {
			wit2DuplicateMeter.Mark(1)
			continue
		}

		// Relay to every WIT2 peer that doesn't already have this witness,
		// excluding the sender we received it from.
		(*handler)(h).relaySignedAnnouncement(peer.ID(), ann)
	}

	return nil
}

// acceptSignedAnnouncement runs signature recovery and producer-binding for a
// single announcement. Returns true when the announcement is verified and the
// caller should proceed to cache + relay; false when the caller should skip
// it. Strikes are issued only on confirmed misbehavior (bad signature or
// signer ≠ scheduled producer for a known header). Pre-import deferral
// (header not yet local) is silent: no strike, no relay. The announcement is
// stashed in the deferred queue so the chain-head loop can re-evaluate it
// once the block arrives — without that, an announce that races ahead of its
// block is lost permanently and subsequent witness fetches silently skip
// byte-verification.
func (h *witHandler) acceptSignedAnnouncement(peer *wit.Peer, ann wit.SignedWitnessAnnouncement) bool {
	signer, err := verifySignedAnnouncement(ann)
	if err != nil {
		wit2InvalidSigMeter.Mark(1)
		peer.Log().Debug("wit2: invalid signed announcement", "blockHash", ann.BlockHash, "err", err)
		(*handler)(h).strikeWit2Peer(peer)
		return false
	}

	ok, headerAvailable := (*handler)(h).isScheduledProducer(signer, ann.BlockNumber, ann.BlockHash)
	if ok {
		return true
	}
	if !headerAvailable {
		peer.Log().Debug("wit2: header not yet local for announced block; deferring announce",
			"blockHash", ann.BlockHash, "blockNumber", ann.BlockNumber)
		(*handler)(h).deferredAnnounces.put(ann, peer.ID())
		return false
	}
	wit2NotValidatorMeter.Mark(1)
	peer.Log().Debug("wit2: signer is not the scheduled producer for this block",
		"blockHash", ann.BlockHash, "blockNumber", ann.BlockNumber, "signer", signer)
	(*handler)(h).strikeWit2Peer(peer)
	return false
}

// relaySignedAnnouncement forwards a verified signed announcement to all WIT2
// peers in `peersWithoutWitness` excluding the original sender. WIT0/WIT1
// peers are skipped — they don't speak the signed wire format. Their slow
// path remains: they'll learn about the witness through the existing post-
// import unsigned announce path on adjacent WIT2 nodes when those nodes
// finish importing.
func (h *handler) relaySignedAnnouncement(senderID string, ann wit.SignedWitnessAnnouncement) {
	recipients := h.peers.peersWithoutSignedAnnounce(ann.BlockHash)
	relayed := 0
	for _, peer := range recipients {
		if peer.Peer.ID() == senderID {
			continue
		}
		if peer.Peer.Version() < wit.WIT2 {
			continue
		}
		peer.Peer.AsyncSendSignedWitnessAnnouncement(ann)
		relayed++
	}
	if relayed > 0 {
		wit2RelayOutMeter.Mark(int64(relayed))
	}
}

// handleGetWitness retrieves witnesses for the requested block hashes and returns them as raw RLP data.
//
// WIT2: per-block lookup consults the in-flight body cache before falling back
// to chain storage. This lets nodes serve witnesses they have received from
// the network but not yet imported. Byte-correctness blame attaches to the
// server only on hash mismatch (the requester verifies bytes against the BP-
// signed WitnessHash); content-correctness failures during execution attach
// to the BP, so this server is not at additional risk by serving early.
func (h *witHandler) handleGetWitness(peer *wit.Peer, req *wit.GetWitnessPacket) (wit.WitnessPacketResponse, error) {
	log.Debug("handleGetWitness processing request", "peer", peer.ID(), "reqID", req.RequestId, "witnessPages", len(req.WitnessPages))

	// Cap the page-entry count up front, mirroring the metadata handler's
	// MaxWitnessMetadataServe guard. The in-loop byte guards below only count
	// data bytes, and only on the needToQuery branch — a request packed with
	// unknown hashes or out-of-range pages accumulates zero bytes and trips
	// neither guard, while still forcing one DB size lookup per distinct hash
	// (resolveWitnessBytes) and one response entry per page. Bounding the entry
	// count closes that CPU/IO/alloc amplification. Legitimate requests carry a
	// single page, so this limit is never approached in practice.
	if len(req.WitnessPages) > MaxWitnessServe {
		return nil, fmt.Errorf("witness request exceeds %d page limit: got %d", MaxWitnessServe, len(req.WitnessPages))
	}

	witnessCache, witnessSize := h.resolveWitnessBytes(req.WitnessPages)

	var response wit.WitnessPacketResponse
	responseElementsSize := uint64(0) // framing-aware response-size guard
	totalLoaded := 0                  // protection against heavy memory requests

	for _, witnessPage := range req.WitnessPages {
		size := witnessSize[witnessPage.Hash]
		totalPages := (size + PageSize - 1) / PageSize // integer trick for: ceil(witnessSize/PageSize)
		var witnessPageResponse wit.WitnessPageResponse
		witnessPageResponse.Page = witnessPage.Page
		witnessPageResponse.Hash = witnessPage.Hash
		witnessPageResponse.TotalPages = totalPages

		// Body absent (neither in-flight cache nor chain storage) but a BP
		// provably signed (or deferred-announced) this hash: record the peer so
		// we push the body the moment we obtain it, keeping WIT2 stateless
		// consumers in lockstep at hop>=2 (see witnessWaiterRegistry).
		if totalPages == 0 {
			h.recordWitnessWaiter(witnessPage.Hash, peer)
		}

		data, err := h.loadWitnessPageData(witnessPage, totalPages, size, witnessCache, &totalLoaded)
		if err != nil {
			return nil, err
		}
		witnessPageResponse.Data = data

		// backstop: bound total witness bytes loaded in case the stored witness is
		// larger than its size index advertised.
		if totalLoaded >= MaximumCachedWitnessOnARequest {
			return nil, errors.New("request demands too much memory")
		}
		// response protection: bound the encoded packet size (RLP framing included)
		responseElementsSize += witnessPageResponseEncodedSize(uint64(len(witnessPageResponse.Data)), witnessPage.Page, totalPages)
		if witnessPacketResponseEncodedSize(req.RequestId, responseElementsSize) > MaximumResponseSize {
			return nil, errors.New("response exceeds maximum p2p payload size")
		}

		response = append(response, witnessPageResponse)
	}

	log.Debug("handleGetWitness returning witnesses pages", "peer", peer.ID(), "reqID", req.RequestId, "count", len(response))
	return response, nil
}

// recordWitnessWaiter registers peer as waiting for a witness this node does not
// have stored yet but that provably exists — either a BP signed its hash, or a
// structurally valid signed announce is deferred pending its block — so the body
// is pushed the instant it is obtained instead of leaving the peer to re-poll us
// with empty GetWitness under backoff. Recording only schedules a future push of
// bytes we actually hold; it does not promote, cache, or relay the unverified
// announce.
func (h *witHandler) recordWitnessWaiter(blockHash common.Hash, peer *wit.Peer) {
	hh := (*handler)(h)
	if signed, hasSigned := hh.signedWitnesses.get(blockHash); hasSigned {
		hh.witnessWaiters.record(blockHash, peer)
		// We ourselves don't hold this body either. Without produce_witness
		// or sync_with_witness, nothing else in this node ever asks for it —
		// go get it, purely on the strength of the signature we already
		// verified, so recordWitnessWaiter's promise ("we push it the moment
		// we obtain it") has a path to actually come true for a pure relay.
		//
		// Gated on a per-requesting-peer budget: the requester is still
		// registered as a waiter above (so an honest retry, or another
		// peer's request for the same hash, can still satisfy them), but a
		// single peer repeatedly asking for many distinct not-yet-fetched
		// hashes cannot use us to spawn unbounded concurrent upstream
		// fetches — each allowed fetch costs a real round trip against
		// another peer, unlike cheap local announce ingestion.
		if hh.wit2FetchTriggerTracker == nil || hh.wit2FetchTriggerTracker.allow(peer.ID(), 1) {
			hh.triggerRelayFetch(blockHash, signed.WitnessHash, peer.ID())
		} else {
			wit2FetchTriggerRateLimitDropMeter.Mark(1)
			peer.Log().Debug("wit2: rate-limited relay-fetch trigger", "hash", blockHash)
		}
	} else if hh.deferredAnnounces.has(blockHash) {
		hh.witnessWaiters.record(blockHash, peer)
	}
}

// triggerRelayFetch asks some other peer for the body of a hash whose BP
// signature we already verified, purely so this node can serve/relay it —
// it does not need the bytes for its own import. Trust boundary matches
// acceptSignedBroadcast: we verify the fetched bytes against the signed
// witnessHash before caching or pushing anything. At most one fetch runs
// per hash at a time; excludePeer is skipped as a candidate since it's the
// one who just asked us (and therefore doesn't have it either).
func (h *handler) triggerRelayFetch(blockHash, wantHash common.Hash, excludePeer string) {
	if _, loaded := h.relayFetchInFlight.LoadOrStore(blockHash, struct{}{}); loaded {
		return
	}

	// Global concurrency cap, independent of the per-peer rate limiter above.
	// That limiter only bounds how often a SINGLE requesting peer can cause a
	// new trigger; it does nothing to stop N different peers from each
	// independently triggering their own burst at the same time. A relay
	// node with several downstream peers (the exact multi-hop topology this
	// fetch exists for) can otherwise rack up dozens of concurrent fetch
	// goroutines, each opening real network connections to candidate peers —
	// enough of a burst to transiently exhaust file descriptors on the node
	// (observed directly: peer connections and the heimdall HTTP client both
	// failed with "too many open files" during such a spike, and never
	// recovered even after the fd pressure passed). Acquiring non-blockingly
	// here means an over-cap trigger is simply dropped — the waiter stays
	// registered either way, so a later request for the same hash (from this
	// peer once its own rate limit refills, or from any other peer) gets a
	// fresh chance once capacity frees up.
	select {
	case h.relayFetchSem <- struct{}{}:
	default:
		h.relayFetchInFlight.Delete(blockHash)
		wit2RelayFetchConcurrencyDropMeter.Mark(1)
		log.Debug("wit2: relay fetch dropped, global concurrency cap reached", "hash", blockHash)
		return
	}

	wit2RelayFetchTriggeredMeter.Mark(1)
	go func() {
		defer func() {
			h.relayFetchInFlight.Delete(blockHash)
			<-h.relayFetchSem
		}()

		var candidates []namedWitnessPeer
		for _, src := range h.peers.peersWithWitnessCandidates(blockHash) {
			if src.witPeer == nil || src.ID() == excludePeer {
				continue
			}
			candidates = append(candidates, namedWitnessPeer{id: src.ID(), peer: src.witPeer.Peer})
		}

		data, witness, servedBy, ok := fetchAndVerifyWitness(candidates, blockHash, wantHash)
		if !ok {
			log.Info("wit2: relay fetch exhausted all candidates without success", "hash", blockHash)
			return
		}
		h.pendingWitnessBodies.put(blockHash, data, wantHash)
		h.pushWitnessToWaiters(blockHash, witness, len(data))
		log.Info("wit2: relay fetch succeeded, pushed to waiters", "hash", blockHash, "servedBy", servedBy, "bytes", len(data))
	}()
}

// namedWitnessPeer pairs a WitnessPeer with the log-friendly ID that only
// ethPeer (not the interface) exposes, so fetchAndVerifyWitness can log
// without depending on the concrete peerSet/ethPeer types — keeping it
// testable with plain fakes.
type namedWitnessPeer struct {
	id   string
	peer WitnessPeer
}

// fetchAndVerifyWitness tries each candidate in order, verifying fetched
// bytes against the already-verified signed hash before trusting them.
// Byte mismatch or decode failure on one candidate does not abort the
// whole attempt — a different candidate might have the real bytes; only a
// candidate serving bytes that don't match the signed commitment is
// distrusted, individually, exactly like acceptSignedBroadcast's model.
func fetchAndVerifyWitness(candidates []namedWitnessPeer, blockHash, wantHash common.Hash) ([]byte, *stateless.Witness, string, bool) {
	for _, c := range candidates {
		log.Info("wit2: relay fetch attempt", "hash", blockHash, "upstream", c.id)

		data, ok := fetchWitnessPages(c.peer, blockHash)
		if !ok {
			continue // this candidate came up empty/errored; try the next one
		}
		if got := stateless.WitnessCommitHash(data); got != wantHash {
			log.Warn("wit2: relay fetch byte mismatch against signed hash; dropping",
				"hash", blockHash, "peer", c.id, "expected", wantHash, "actual", got)
			continue // this peer served bad bytes; a different candidate might not
		}
		var witness stateless.Witness
		if err := rlp.DecodeBytes(data, &witness); err != nil {
			log.Debug("wit2: relay fetch failed to decode witness", "hash", blockHash, "err", err)
			continue
		}
		return data, &witness, c.id, true
	}
	return nil, nil, "", false
}

// fetchWitnessPages pulls every page of hash's witness from peer and returns
// the concatenated bytes. Mirrors ethPeer.RequestWitnessPageCount's WIT1
// metadata request (falling back to the WIT0 page-0-probe for older peers),
// then walks pages 1..totalPages-1 the same way page 0 was fetched. A
// same-hash relay fetch this session may span more than one page — the
// PageSize cap is 15MB, and a witness can exceed that — so treating page 0
// as the whole body silently truncates anything bigger, which then fails the
// signed-hash check below and reads as a false "malicious peer" case.
func fetchWitnessPages(peer WitnessPeer, hash common.Hash) ([]byte, bool) {
	firstPage, totalPages, ok := requestWitnessPage(peer, hash, 0)
	if !ok || totalPages == 0 {
		return nil, false
	}
	if totalPages == 1 {
		return firstPage, true
	}
	all := make([]byte, 0, len(firstPage)*int(totalPages))
	all = append(all, firstPage...)
	for page := uint64(1); page < totalPages; page++ {
		data, pages, ok := requestWitnessPage(peer, hash, page)
		if !ok || pages != totalPages {
			return nil, false
		}
		all = append(all, data...)
	}
	return all, true
}

// relayFetchPageTimeout bounds how long requestWitnessPage waits for a
// single page's response before giving up on this candidate. A candidate
// that disconnects mid-fetch (after a request was dispatched but before the
// reply arrives) never signals failure on resCh — the dispatcher just stops
// — so this timeout, not an error return, is what lets fetchAndVerifyWitness
// move on to the next candidate. A package var (not an inline literal) so
// tests can shrink it instead of eating the full wait.
var relayFetchPageTimeout = 5 * time.Second

// requestWitnessPage fetches a single page and returns its data alongside
// the TotalPages the server reported for this hash (so the caller can detect
// a server that changes its story mid-fetch).
func requestWitnessPage(peer WitnessPeer, hash common.Hash, page uint64) ([]byte, uint64, bool) {
	resCh := make(chan *wit.Response, 1)
	req, err := peer.RequestWitness([]wit.WitnessPageRequest{{Hash: hash, Page: page}}, resCh)
	if err != nil {
		return nil, 0, false
	}
	defer req.Close()

	select {
	case res := <-resCh:
		if res == nil {
			return nil, 0, false
		}
		// The wit dispatcher (see dispatchResponse in wit/dispatcher.go)
		// blocks the peer's response-handling goroutine on res.Done until
		// the recipient signals receipt. That goroutine is the same one
		// draining the peer's per-protocol read channel — leaving it
		// blocked here head-of-line-blocks the peer's whole connection
		// (every subprotocol is demuxed off one shared TCP stream), not
		// just this witness fetch. Matches the existing correct pattern in
		// awaitWitnessResponse (eth/peer.go).
		if res.Done != nil {
			res.Done <- nil
		}
		packet, ok := res.Res.(*wit.WitnessPacketRLPPacket)
		if !ok || len(packet.WitnessPacketResponse) == 0 {
			return nil, 0, false
		}
		entry := packet.WitnessPacketResponse[0]
		if len(entry.Data) == 0 {
			return nil, 0, false // upstream doesn't have it either
		}
		return entry.Data, entry.TotalPages, true
	case <-time.After(relayFetchPageTimeout):
		return nil, 0, false
	}
}

// loadWitnessPageData returns the bytes for one requested witness page, or nil
// when the page is out of range. witnessCache is pre-populated by
// resolveWitnessBytes with WIT2 in-flight bodies (pre-import serving), so a hit
// serves those bytes and skips the chain read; a miss falls back to uncached
// chain storage (so peer-serving traffic does not evict witnesses the import path
// relies on) after enforcing the per-request memory budget. totalLoaded is
// updated in place with the bytes actually read.
func (h *witHandler) loadWitnessPageData(witnessPage wit.WitnessPageRequest, totalPages, size uint64, witnessCache map[common.Hash][]byte, totalLoaded *int) ([]byte, error) {
	if witnessPage.Page >= totalPages {
		return nil, nil
	}
	witnessBytes, exists := witnessCache[witnessPage.Hash]
	if !exists {
		// Reject before reading if loading this witness would cross the
		// per-request memory budget, so a rejected request never allocates a full
		// witness past the bound.
		if *totalLoaded+int(size) >= MaximumCachedWitnessOnARequest {
			return nil, errors.New("request demands too much memory")
		}
		witnessBytes = h.Chain().GetWitnessUncached(witnessPage.Hash)
		witnessCache[witnessPage.Hash] = witnessBytes
		*totalLoaded += len(witnessBytes)
	}

	// Clamp both bounds: the size index and the stored witness can disagree under
	// a concurrent delete, so never slice past the bytes actually read.
	witnessLen := uint64(len(witnessBytes))
	start := PageSize * witnessPage.Page
	end := start + PageSize
	if start > witnessLen {
		start = witnessLen
	}
	if end > witnessLen {
		end = witnessLen
	}
	if start == end {
		// Metadata advertised this page but the stored witness is missing or
		// truncated; fail rather than serving a misleading empty page.
		return nil, errors.New("witness page unavailable")
	}
	return witnessBytes[start:end], nil
}

// resolveWitnessBytes resolves witness bytes and sizes for each unique block
// hash referenced by the request. Prefers the in-flight body cache (WIT2
// pre-import serving) and falls back to chain-storage size lookup. Bytes for
// the chain-storage path are read lazily during page serving; only sizes are
// resolved up front so the response can carry accurate TotalPages even for
// pages this peer cannot fulfil.
func (h *witHandler) resolveWitnessBytes(pages []wit.WitnessPageRequest) (map[common.Hash][]byte, map[common.Hash]uint64) {
	seen := make(map[common.Hash]struct{}, len(pages))
	for _, p := range pages {
		seen[p.Hash] = struct{}{}
	}
	bytesByHash := make(map[common.Hash][]byte, len(seen))
	sizeByHash := make(map[common.Hash]uint64, len(seen))
	prefetchedBytes := uint64(0)
	for blockHash := range seen {
		if cached, _, ok := (*handler)(h).pendingWitnessBodies.get(blockHash); ok {
			bytesByHash[blockHash] = cached
			sizeByHash[blockHash] = uint64(len(cached))
			wit2test.Stamp("SERVED_FROM_CACHE", "block_hash", blockHash.Hex(), "size", len(cached))
			continue
		}
		if size := rawdb.ReadWitnessSize(h.Chain().DB(), blockHash); size != nil {
			sizeByHash[blockHash] = *size
			wit2test.Stamp("SERVED_FROM_CHAIN", "block_hash", blockHash.Hex(), "size", *size)
			continue
		}
		// No persisted size and no BP-signed body on file. The header check
		// guards against a peer DoSing us into a 2s pipelined-SRC wait per
		// nonexistent hash; a known header may still be mid-computation by
		// this node's own SRC goroutine, so wait for it rather than falling
		// through to the WIT2 waiter path, which only tracks network-received
		// (signed/deferred) hashes and would never fire for a locally-produced
		// block. Prefetch bytes are capped at MaximumResponseSize — nothing
		// beyond that can be served in this response anyway.
		if h.Chain().GetHeaderByHash(blockHash) == nil {
			continue
		}
		if w := h.Chain().GetWitnessUncachedWait(blockHash); len(w) > 0 {
			sizeByHash[blockHash] = uint64(len(w))
			wit2test.Stamp("SERVED_FROM_SRC_WAIT", "block_hash", blockHash.Hex(), "size", len(w))
			if prefetchedBytes+uint64(len(w)) <= MaximumResponseSize {
				bytesByHash[blockHash] = w
				prefetchedBytes += uint64(len(w))
			}
		}
	}
	return bytesByHash, sizeByHash
}

func witnessPacketResponseEncodedSize(requestID uint64, responseElementsSize uint64) uint64 {
	responseListSize := rlpListEncodedSize(responseElementsSize)
	return rlpListEncodedSize(rlpUintEncodedSize(requestID) + responseListSize)
}

func witnessPageResponseEncodedSize(dataSize uint64, page uint64, totalPages uint64) uint64 {
	const hashEncodedSize = 1 + common.HashLength
	payloadSize := rlpBytesEncodedSizeUpperBound(dataSize) + hashEncodedSize + rlpUintEncodedSize(page) + rlpUintEncodedSize(totalPages)
	return rlpListEncodedSize(payloadSize)
}

func rlpBytesEncodedSizeUpperBound(size uint64) uint64 {
	if size == 1 {
		return 2
	}
	if size < 56 {
		return 1 + size
	}
	return 1 + uint64ByteLen(size) + size
}

func rlpListEncodedSize(payloadSize uint64) uint64 {
	if payloadSize < 56 {
		return 1 + payloadSize
	}
	return 1 + uint64ByteLen(payloadSize) + payloadSize
}

func rlpUintEncodedSize(n uint64) uint64 {
	if n < 128 {
		return 1
	}
	return 1 + uint64ByteLen(n)
}

func uint64ByteLen(n uint64) uint64 {
	var size uint64
	for n > 0 {
		size++
		n >>= 8
	}
	return size
}

// handleGetWitnessMetadata retrieves only the metadata (page count, size, block number) for the requested witness hashes.
// This is efficient for verification purposes where we don't need the actual witness data.
func (h *witHandler) handleGetWitnessMetadata(peer *wit.Peer, req *wit.GetWitnessMetadataPacket) ([]wit.WitnessMetadataResponse, error) {
	log.Debug("handleGetWitnessMetadata processing request", "peer", peer.ID(), "reqID", req.RequestId, "hashes", len(req.Hashes))

	if len(req.Hashes) > MaxWitnessMetadataServe {
		return nil, fmt.Errorf("witness metadata request exceeds %d hash limit: got %d", MaxWitnessMetadataServe, len(req.Hashes))
	}

	var response []wit.WitnessMetadataResponse

	for _, hash := range req.Hashes {
		var (
			witnessSize uint64
			available   bool
		)

		// Prefer in-flight body cache (WIT2 fast path).
		if cached, _, ok := (*handler)(h).pendingWitnessBodies.get(hash); ok {
			witnessSize = uint64(len(cached))
			available = true
		} else if size := rawdb.ReadWitnessSize(h.Chain().DB(), hash); size != nil {
			witnessSize = *size
			available = true
		}

		// Calculate total pages
		totalPages := (witnessSize + PageSize - 1) / PageSize // ceil(witnessSize/PageSize)

		// Get block number from header. Pre-import we may not yet have the
		// header, so fall back to the announcement-cached number if a signed
		// announcement is on file.
		blockNumber := uint64(0)
		if header := h.Chain().GetHeaderByHash(hash); header != nil {
			blockNumber = header.Number.Uint64()
		} else if ann, ok := (*handler)(h).signedWitnesses.get(hash); ok {
			blockNumber = ann.BlockNumber
		}

		response = append(response, wit.WitnessMetadataResponse{
			Hash:        hash,
			TotalPages:  totalPages,
			WitnessSize: witnessSize,
			BlockNumber: blockNumber,
			Available:   available,
		})
	}

	log.Debug("handleGetWitnessMetadata returning metadata", "peer", peer.ID(), "reqID", req.RequestId, "count", len(response))
	return response, nil
}
