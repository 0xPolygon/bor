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
)

const (
	// witnessRequestTimeout defines how long to wait for an in-flight witness computation.
	witnessRequestTimeout          = 5 * time.Second
	PageSize                       = 15 * 1024 * 1024  // 15 MB
	MaximumCachedWitnessOnARequest = 200 * 1024 * 1024 // 200 MB, the maximum amount of memory a request can demand while getting witness
	MaximumResponseSize            = 16 * 1024 * 1024  // 16 MB, helps to fast fail check
	MaxWitnessMetadataServe        = 1024              // maximum hashes a single GetWitnessMetadata request may carry
	MaxWitnessPagesServe           = 1024              // maximum {hash,page} entries a single GetWitness request may carry
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

	// WIT2: verify against the BP-signed witnessHash on file, then cache the
	// encoded body so this node can serve it pre-import. We only expose the
	// cache for serving when bytes match — otherwise an upstream that lied
	// about the bytes would make us serve garbage and get dropped by
	// downstream peers as liars, even though we just relayed what we received.
	if signed, hasSigned := (*handler)(h).signedWitnesses.get(hash); hasSigned {
		var buf bytes.Buffer
		if err := witness.EncodeRLP(&buf); err != nil {
			// Can't re-encode → can't check the signed commitment. Treat as
			// unverifiable rather than letting unchecked bytes through.
			peer.Log().Warn("wit2: failed to encode received witness", "hash", hash, "err", err)
			return nil
		}
		bodyBytes := buf.Bytes()
		bodyHash := stateless.WitnessCommitHash(bodyBytes)
		if signed.WitnessHash != bodyHash {
			// Upstream sent bytes that don't match the BP-signed commitment.
			// Don't cache, don't mark the sender as a body-holder, don't
			// inject: the broadcast path must not be a bypass of the byte
			// verification the paged-fetch path performs. No disconnect — the
			// sender may itself have been fed bad bytes upstream.
			wit2BroadcastByteMismatchMeter.Mark(1)
			peer.Log().Warn("wit2: broadcast bytes do not match signed witnessHash; dropping",
				"blockHash", hash, "expected", signed.WitnessHash, "actual", bodyHash)
			return nil
		}
		peer.AddKnownWitness(hash)
		(*handler)(h).pendingWitnessBodies.put(hash, bodyBytes, bodyHash)
		// We now hold servable bytes — push to any peer that asked us
		// for this body before we had it.
		(*handler)(h).pushWitnessToWaiters(hash, witness, len(bodyBytes))
	} else {
		// No signed announcement on file: WIT1 fallback. The only binding we
		// can check is that the header belongs to a block we actually know —
		// without it, an unsolicited 16MB body for an arbitrary hash would be
		// decoded and cached purely on the sender's word. Drop silently: a
		// peer racing ahead of our import is early, not malicious.
		if h.Chain().GetHeaderByHash(hash) == nil {
			wit2BroadcastUnknownHeaderDropMeter.Mark(1)
			peer.Log().Debug("dropping witness broadcast for unknown header", "blockHash", hash)
			return nil
		}
		peer.AddKnownWitness(hash)
		// Header is known, but we cannot prove byte-correctness to downstream
		// WIT2 peers — don't expose for pre-import serving. The body still
		// flows into the import path below.
		wit2BroadcastUnverifiedSkippedMeter.Mark(1)
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
// On verification failure (bad signature, unknown signer) the sender is
// **not** dropped at this layer — they may simply be relaying a bad upstream
// announcement. Drops are reserved for byte-correctness failures at fetch
// time. We do, however, count invalid announcements via metrics to surface
// misbehaving relayers.
func (h *witHandler) handleSignedWitnessAnnouncements(peer *wit.Peer, anns []wit.SignedWitnessAnnouncement) error {
	wit2RelayInMeter.Mark(int64(len(anns)))

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
	if len(req.WitnessPages) > MaxWitnessPagesServe {
		return nil, fmt.Errorf("witness request exceeds %d page limit: got %d", MaxWitnessPagesServe, len(req.WitnessPages))
	}

	witnessCache, witnessSize := h.resolveWitnessBytes(req.WitnessPages)

	var response wit.WitnessPacketResponse
	totalResponsePayloadDataAmount := 0 // fast fail check
	totalCached := 0                    // protection against heavy memory requests

	for _, witnessPage := range req.WitnessPages {
		totalPages := (witnessSize[witnessPage.Hash] + PageSize - 1) / PageSize // ceil(witnessSize/PageSize)
		pageResponse := wit.WitnessPageResponse{
			Page:       witnessPage.Page,
			Hash:       witnessPage.Hash,
			TotalPages: totalPages,
		}

		// Body absent (neither in-flight cache nor chain storage) but a BP
		// signed its hash, so the witness exists and is in flight: remember
		// this peer as waiting so we push the body the moment we obtain it,
		// instead of leaving it to re-poll us with empty GetWitness. This is
		// what keeps WIT2 stateless consumers in lockstep at hop>=2 (see
		// witnessWaiterRegistry).
		if totalPages == 0 {
			if _, hasSigned := (*handler)(h).signedWitnesses.get(witnessPage.Hash); hasSigned {
				(*handler)(h).witnessWaiters.record(witnessPage.Hash, peer)
			}
		}

		if witnessPage.Page < totalPages {
			witnessBytes, ok := witnessCache[witnessPage.Hash]
			if !ok {
				// Post-import fallback: fetch from chain storage on demand.
				// If both this and the in-flight cache missed during resolveWitnessBytes,
				// witnessSize[hash] would be 0 and we wouldn't reach this branch.
				witnessBytes = h.Chain().GetWitness(witnessPage.Hash)
				witnessCache[witnessPage.Hash] = witnessBytes
				totalCached += len(witnessBytes)
			}

			start := PageSize * witnessPage.Page
			end := start + PageSize
			if end > uint64(len(witnessBytes)) {
				end = uint64(len(witnessBytes))
			}
			pageResponse.Data = witnessBytes[start:end]
			totalResponsePayloadDataAmount += len(pageResponse.Data)
		}
		response = append(response, pageResponse)

		if totalCached >= MaximumCachedWitnessOnARequest {
			return nil, errors.New("request demands a huge amount of memory")
		}
		if totalResponsePayloadDataAmount >= MaximumResponseSize {
			return nil, errors.New("response exceeds maximum p2p payload size")
		}
	}

	log.Debug("handleGetWitness returning witnesses pages", "peer", peer.ID(), "reqID", req.RequestId, "count", len(response))
	return response, nil
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
	for blockHash := range seen {
		if cached, _, ok := (*handler)(h).pendingWitnessBodies.get(blockHash); ok {
			bytesByHash[blockHash] = cached
			sizeByHash[blockHash] = uint64(len(cached))
			continue
		}
		if size := rawdb.ReadWitnessSize(h.Chain().DB(), blockHash); size != nil {
			sizeByHash[blockHash] = *size
		}
	}
	return bytesByHash, sizeByHash
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
