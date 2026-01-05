package eth

import (
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

var (
	compactWitnessCacheHitMeter  = metrics.NewRegisteredMeter("eth/witness/compactcache/hit", nil)
	compactWitnessCacheMissMeter = metrics.NewRegisteredMeter("eth/witness/compactcache/miss", nil)
)

const (
	// witnessRequestTimeout defines how long to wait for an in-flight witness computation.
	witnessRequestTimeout          = 5 * time.Second
	PageSize                       = 15 * 1024 * 1024  // 15 MB
	MaximumCachedWitnessOnARequest = 200 * 1024 * 1024 // 200 MB, the maximum amount of memory a request can demand while getting witness
	MaximumResponseSize            = 16 * 1024 * 1024  // 16 MB, helps to fast fail check
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
	case *wit.GetWitnessPacket:
		// Handle regular witness request
		response, err := h.handleGetWitness(peer, packet)
		if err != nil {
			return fmt.Errorf("failed to handle GetWitnessPacket: %w", err)
		}
		// Reply with regular witness
		return peer.ReplyWitness(packet.RequestId, &response)

	case *wit.GetCompactWitnessPacket:
		// Handle compact witness request with filtering
		// Convert to regular GetWitnessPacket for internal handling
		regularPacket := &wit.GetWitnessPacket{
			RequestId:         packet.RequestId,
			GetWitnessRequest: packet.GetWitnessRequest,
		}
		response, err := h.handleGetCompactWitness(peer, regularPacket)
		if err != nil {
			return fmt.Errorf("failed to handle GetCompactWitnessPacket: %w", err)
		}
		// Reply with compact witness (different message code)
		return peer.ReplyCompactWitness(packet.RequestId, &response)

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

// handleWitnessBroadcast handles a witness broadcast from a peer.
func (h *witHandler) handleWitnessBroadcast(peer *wit.Peer, witness *stateless.Witness) error {
	peer.AddKnownWitness(witness.Header().Hash())
	hash := witness.Header().Hash()

	// Inject the witness into the block fetcher's cache
	if h.blockFetcher != nil {
		log.Debug("Injecting witness into block fetcher", "hash", hash, "peer", peer.ID())
		// Verify witness header matches a known block hash
		blockHash := witness.Header().Hash()
		log.Debug("Witness details", "blockHash", blockHash, "header", witness.Header().Number)

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

// handleGetWitness retrieves witnesses for the requested block hashes and returns them as raw RLP data.
// It now returns the data and error, rather than sending the reply directly.
// The returned data is [][]byte, as rlp.RawValue is essentially []byte.
func (h *witHandler) handleGetWitness(peer *wit.Peer, req *wit.GetWitnessPacket) (wit.WitnessPacketResponse, error) {
	log.Debug("handleGetWitness processing request", "peer", peer.ID(), "reqID", req.RequestId, "witnessPages", len(req.WitnessPages))
	// list different witnesses to query
	seen := make(map[common.Hash]struct{}, len(req.WitnessPages))
	for _, witnessPage := range req.WitnessPages {
		seen[witnessPage.Hash] = struct{}{}
	}

	// witness sizes query
	witnessSize := make(map[common.Hash]uint64, len(seen))
	for witnessBlockHash := range seen {
		size := rawdb.ReadWitnessSize(h.Chain().DB(), witnessBlockHash)
		if size == nil {
			witnessSize[witnessBlockHash] = 0
		} else {
			witnessSize[witnessBlockHash] = *size
		}
	}

	// query witnesses by demand
	var response wit.WitnessPacketResponse
	witnessCache := make(map[common.Hash][]byte, len(seen))

	totalResponsePayloadDataAmount := 0 // fast fail check
	totalCached := 0                    // protection against heavy memory requests

	for _, witnessPage := range req.WitnessPages {
		totalPages := (witnessSize[witnessPage.Hash] + PageSize - 1) / PageSize // integer trick for: ceil(witnessSize/PageSize)
		var witnessPageResponse wit.WitnessPageResponse
		witnessPageResponse.Page = witnessPage.Page
		witnessPageResponse.Hash = witnessPage.Hash
		witnessPageResponse.TotalPages = totalPages

		needToQuery := witnessPage.Page < totalPages
		if needToQuery {
			var witnessBytes []byte
			if cachedRLPBytes, exists := witnessCache[witnessPage.Hash]; exists {
				witnessBytes = cachedRLPBytes
			} else {
				queriedBytes := rawdb.ReadWitness(h.Chain().DB(), witnessPage.Hash)
				witnessCache[witnessPage.Hash] = queriedBytes
				witnessBytes = queriedBytes
				totalCached += len(queriedBytes)
			}

			start := PageSize * witnessPage.Page
			end := start + PageSize
			if end > uint64(len(witnessBytes)) {
				end = uint64(len(witnessBytes))
			}
			witnessPageResponse.Data = witnessBytes[start:end]
			totalResponsePayloadDataAmount += len(witnessPageResponse.Data)
		}
		response = append(response, witnessPageResponse)

		// fast fail check
		if totalCached >= MaximumCachedWitnessOnARequest {
			return nil, errors.New("requests demans huge amount of memory")
		}
		// memory protection check
		if totalResponsePayloadDataAmount >= MaximumResponseSize {
			return nil, errors.New("response exceeds maximum p2p payload size")
		}
	}

	// Return the collected RLP data
	log.Debug("handleGetWitness returning witnesses pages", "peer", peer.ID(), "reqID", req.RequestId, "count", len(response))
	return response, nil
}

// handleGetCompactWitness retrieves witnesses and filters them using the sliding window cache.
// This reduces bandwidth by omitting state nodes that the receiver should already have cached.
func (h *witHandler) handleGetCompactWitness(peer *wit.Peer, req *wit.GetWitnessPacket) (wit.WitnessPacketResponse, error) {
	log.Debug("handleGetCompactWitness processing request", "peer", peer.ID(), "reqID", req.RequestId, "witnessPages", len(req.WitnessPages))

	// First, get the full witness data (same as regular handleGetWitness)
	fullResponse, err := h.handleGetWitness(peer, req)
	if err != nil {
		return nil, err
	}

	// If parallel import is enabled on this node, cache isn't maintained
	// Return full witness instead of trying to filter
	if h.chain.IsParallelStatelessImportEnabled() {
		log.Debug("Parallel import enabled, returning full witness instead of compact",
			"peer", peer.ID(),
			"reqID", req.RequestId)
		return fullResponse, nil
	}

	seen := make(map[common.Hash]struct{}, len(req.WitnessPages))
	for _, witnessPage := range req.WitnessPages {
		seen[witnessPage.Hash] = struct{}{}
	}

	witnessSize := make(map[common.Hash]uint64, len(seen))
	for witnessBlockHash := range seen {
		size := rawdb.ReadWitnessSize(h.Chain().DB(), witnessBlockHash)
		if size == nil {
			witnessSize[witnessBlockHash] = 0
		} else {
			witnessSize[witnessBlockHash] = *size
		}
	}

	type compactDiskEntry struct {
		windowStart uint64
		data        []byte
		size        uint64
	}

	blockNumbers := make(map[common.Hash]uint64, len(seen))
	compactDisk := make(map[common.Hash]*compactDiskEntry, len(seen))
	for hash := range seen {
		if header := h.chain.GetHeaderByHash(hash); header != nil {
			blockNumbers[hash] = header.Number.Uint64()
		}
		if windowStart, data := rawdb.ReadCompactWitness(h.chain.DB(), hash); len(data) > 0 {
			compactDisk[hash] = &compactDiskEntry{
				windowStart: windowStart,
				data:        data,
				size:        uint64(len(data)),
			}
		}
	}

	totalResponsePayloadDataAmount := 0

	// Now filter each witness page by removing cached states
	var filteredResponse wit.WitnessPacketResponse

	for _, witnessPage := range fullResponse {
		var witnessPageResponse wit.WitnessPageResponse
		witnessPageResponse.Page = witnessPage.Page
		witnessPageResponse.Hash = witnessPage.Hash
		totalPages := (witnessSize[witnessPage.Hash] + PageSize - 1) / PageSize
		witnessPageResponse.TotalPages = totalPages

		if len(witnessPage.Data) == 0 {
			// Empty page, just pass through
			filteredResponse = append(filteredResponse, witnessPage)
			continue
		}

		blockNum, haveBlockNum := blockNumbers[witnessPage.Hash]
		var expectedWindowStart uint64
		if haveBlockNum {
			expectedWindowStart = h.chain.CalculateCacheWindowStart(blockNum)
		}

		if entry, ok := compactDisk[witnessPage.Hash]; ok && haveBlockNum && entry.windowStart == expectedWindowStart {
			totalPages = (entry.size + PageSize - 1) / PageSize
			witnessPageResponse.TotalPages = totalPages
			if witnessPage.Page < totalPages {
				start := PageSize * witnessPage.Page
				end := start + PageSize
				if end > entry.size {
					end = entry.size
				}
				witnessPageResponse.Data = entry.data[start:end]
				totalResponsePayloadDataAmount += len(witnessPageResponse.Data)
			}
			filteredResponse = append(filteredResponse, witnessPageResponse)
			if totalResponsePayloadDataAmount >= MaximumResponseSize {
				return nil, errors.New("response exceeds maximum p2p payload size")
			}
			continue
		}

		compactWitnessCacheMissMeter.Mark(1)

		if totalPages == 0 {
			filteredResponse = append(filteredResponse, witnessPageResponse)
			continue
		}

		cacheKey := compactWitnessCacheKey{
			hash:        witnessPage.Hash,
			windowStart: expectedWindowStart,
		}

		(*handler)(h).compactWitnessCacheLock.RLock()
		cachedFiltered, cacheHit := (*handler)(h).compactWitnessCache.Get(cacheKey)
		(*handler)(h).compactWitnessCacheLock.RUnlock()

		if cacheHit {
			compactWitnessCacheHitMeter.Mark(1)

			witnessPageResponse.Data = cachedFiltered
			filteredResponse = append(filteredResponse, witnessPageResponse)
			totalResponsePayloadDataAmount += len(cachedFiltered)
			if totalResponsePayloadDataAmount >= MaximumResponseSize {
				return nil, errors.New("response exceeds maximum p2p payload size")
			}
			continue
		}

		// No stored compact witness found (neither on disk nor in memory cache).
		// Return full witness instead of filtering on-the-fly to avoid inconsistencies.
		// On-the-fly filtering might produce different results than what was stored during import.
		log.Debug("PSP - No stored compact witness found, returning full witness",
			"hash", witnessPage.Hash,
			"blockNum", blockNum,
			"windowStart", expectedWindowStart)
		filteredResponse = append(filteredResponse, witnessPage)
		totalResponsePayloadDataAmount += len(witnessPage.Data)
		if totalResponsePayloadDataAmount >= MaximumResponseSize {
			return nil, errors.New("response exceeds maximum p2p payload size")
		}
	}

	log.Debug("handleGetCompactWitness returning filtered witnesses", "peer", peer.ID(), "reqID", req.RequestId, "count", len(filteredResponse))
	return filteredResponse, nil
}

// ClearStaleCompactWitnessCache removes cache entries that are no longer valid
// due to sliding window movement. Should be called when cache window slides.
func (h *witHandler) ClearStaleCompactWitnessCache(currentWindowStart uint64) {
	(*handler)(h).compactWitnessCacheLock.Lock()
	defer (*handler)(h).compactWitnessCacheLock.Unlock()

	// Get all keys and remove those with old windowStart
	keys := (*handler)(h).compactWitnessCache.Keys()
	removed := 0
	for _, key := range keys {
		if key.windowStart < currentWindowStart {
			(*handler)(h).compactWitnessCache.Remove(key)
			removed++
		}
	}

	if removed > 0 {
		log.Debug("Cleared stale compact witness cache entries",
			"removed", removed,
			"currentWindowStart", currentWindowStart)
	}
}

// handleGetWitnessMetadata retrieves only the metadata (page count, size, block number) for the requested witness hashes.
// This is efficient for verification purposes where we don't need the actual witness data.
func (h *witHandler) handleGetWitnessMetadata(peer *wit.Peer, req *wit.GetWitnessMetadataPacket) ([]wit.WitnessMetadataResponse, error) {
	log.Debug("handleGetWitnessMetadata processing request", "peer", peer.ID(), "reqID", req.RequestId, "hashes", len(req.Hashes))

	var response []wit.WitnessMetadataResponse

	for _, hash := range req.Hashes {
		// Get witness size from database
		size := rawdb.ReadWitnessSize(h.Chain().DB(), hash)
		witnessSize := uint64(0)
		available := false

		if size != nil {
			witnessSize = *size
			available = true
		}

		// Calculate total pages
		totalPages := (witnessSize + PageSize - 1) / PageSize // ceil(witnessSize/PageSize)

		// Get block number from header
		blockNumber := uint64(0)
		header := h.Chain().GetHeaderByHash(hash)
		if header != nil {
			blockNumber = header.Number.Uint64()
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
