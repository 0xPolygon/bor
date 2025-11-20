// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/snap"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	DefaultPagesRequestPerWitness     = 1
	DefaultConcurrentRequestsPerPeer  = 5
	DefaultConcurrentResponsesHandled = 10
	DefaultMaxPagesRequestRetries     = 2
)

// ethPeerInfo represents a short summary of the `eth` sub-protocol metadata known
// about a connected peer.
type ethPeerInfo struct {
	Version uint `json:"version"` // Ethereum protocol version negotiated
	*peerBlockRange
}

type peerBlockRange struct {
	Earliest   uint64      `json:"earliestBlock"`
	Latest     uint64      `json:"latestBlock"`
	LatestHash common.Hash `json:"latestBlockHash"`
}

// ethPeer is a wrapper around eth.Peer to maintain a few extra metadata.
type ethPeer struct {
	*eth.Peer
	snapExt *snapPeer // Satellite `snap` connection
	witPeer *witPeer
}

// info gathers and returns some `eth` protocol metadata known about a peer.
// nolint:typecheck
func (p *ethPeer) info() *ethPeerInfo {
	info := &ethPeerInfo{Version: p.Version()}
	if br := p.BlockRange(); br != nil {
		info.peerBlockRange = &peerBlockRange{
			Earliest:   br.EarliestBlock,
			Latest:     br.LatestBlock,
			LatestHash: br.LatestBlockHash,
		}
	}
	return info
}

// snapPeerInfo represents a short summary of the `snap` sub-protocol metadata known
// about a connected peer.
type snapPeerInfo struct {
	Version uint `json:"version"` // Snapshot protocol version negotiated
}

// snapPeer is a wrapper around snap.Peer to maintain a few extra metadata.
type snapPeer struct {
	*snap.Peer
}

// info gathers and returns some `snap` protocol metadata known about a peer.
func (p *snapPeer) info() *snapPeerInfo {
	return &snapPeerInfo{
		Version: p.Version(),
	}
}

type witPeerInfo struct {
	Version uint `json:"version"` // Witness protocol version negotiated
}

type WitnessPeer interface {
	// the method ethPeer.RequestWitnesses invokes
	AsyncSendNewWitness(witness *stateless.Witness)
	AsyncSendNewWitnessHash(hash common.Hash, number uint64)
	RequestWitness(witnessPages []wit.WitnessPageRequest, sink chan *wit.Response) (*wit.Request, error)
	RequestCompactWitness(witnessPages []wit.WitnessPageRequest, sink chan *wit.Response) (*wit.Request, error)
	RequestWitnessMetadata(hashes []common.Hash, sink chan *wit.Response) (*wit.Request, error)
	Close()
	ID() string
	Version() uint
	Log() log.Logger
	KnownWitnesses() *wit.KnownCache
	AddKnownWitness(hash common.Hash)
	KnownWitnessesCount() int
	KnownWitnessesContains(witness *stateless.Witness) bool
	KnownWitnessContainsHash(hash common.Hash) bool
	ReplyWitness(requestID uint64, response *wit.WitnessPacketResponse) error
	ReplyCompactWitness(requestID uint64, response *wit.WitnessPacketResponse) error
}

// witPeer is wrapper around wit.Peer to maintain a few extra metadata and test compatibility.
type witPeer struct {
	// *wit.Peer
	Peer WitnessPeer
}

// info gathers and returns some `wit` protocol metadata known about a peer.
func (p *witPeer) info() *witPeerInfo {
	return &witPeerInfo{
		Version: p.Peer.Version(),
	}
}

// wrapper to associate a request to it's equivalent response
type witReqRes struct {
	Response *wit.Response
	Request  []wit.WitnessPageRequest
}

type witReqRetryCount struct {
	FailCount        int
	ShouldRetryAgain bool
}

// ethWitRequest wraps an eth.Request and holds the underlying wit.Request (which can be multiple).
// This allows the downloader to track the request lifecycle via the eth.Request
// while allowing cancellation to be passed to all wit.Request.
type ethWitRequest struct {
	*eth.Request                // Embedded eth.Request (must be non-nil)
	witReqs      []*wit.Request // The actual witness protocol request
}

// Close overrides the embedded eth.Request's Close to also close the wit.Request
// and signal cancellation via the embedded request's cancel channel.
func (r *ethWitRequest) Close() error {
	// Signal cancellation on the embedded request's channel first.
	// Note: This assumes r.Request and r.Request.cancel are non-nil.
	// The channel is initialized in RequestWitnesses.
	close(r.Request.Cancel)

	// Then close the underlying witnesses requests.
	// The eth.Request shim doesn't need explicit closing here,
	// as it's not registered with the eth dispatcher.
	for _, witReq := range r.witReqs {
		err := witReq.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// SupportsWitness implements downloader.Peer.
// It returns true if the peer supports the witness protocol.
func (p *ethPeer) SupportsWitness() bool {
	return p.witPeer != nil
}

// RequestWitnesses implements downloader.Peer.
// It requests witnesses using the wit protocol for the given block hashes.
func (p *ethPeer) RequestWitnesses(hashes []common.Hash, dlResCh chan *eth.Response) (*eth.Request, error) {
	return p.RequestWitnessesWithVerification(hashes, dlResCh, nil, nil, false) // Default to full witness
}

// RequestWitnessPageCount requests only the page count for a witness using the new metadata protocol.
// This is efficient for verification purposes where we only need metadata, not the actual witness data.
func (p *ethPeer) RequestWitnessPageCount(hash common.Hash) (uint64, error) {
	if p.witPeer == nil {
		return 0, errors.New("witness peer not found")
	}

	// Check if peer supports WIT2 protocol with metadata message
	if p.witPeer.Peer.Version() < wit.WIT2 {
		// Fallback to old method for WIT1 peers: request page 0
		return p.requestWitnessPageCountLegacy(hash)
	}

	p.witPeer.Peer.Log().Trace("RequestWitnessPageCount called", "peer", p.ID(), "hash", hash)

	// Use the new efficient metadata request (WIT2)
	witResCh := make(chan *wit.Response, 1)

	witReq, err := p.witPeer.Peer.RequestWitnessMetadata([]common.Hash{hash}, witResCh)
	if err != nil {
		p.witPeer.Peer.Log().Error("Error requesting witness metadata", "peer", p.ID(), "err", err)
		return 0, err
	}
	defer witReq.Close()

	// Wait for metadata response with timeout
	select {
	case witRes := <-witResCh:
		if witRes == nil {
			return 0, errors.New("nil witness metadata response")
		}

		// Extract WitnessMetadataPacket from the response
		metadataPacket, ok := witRes.Res.(*wit.WitnessMetadataPacket)
		if !ok {
			return 0, fmt.Errorf("unexpected witness metadata response type: %T", witRes.Res)
		}

		// Extract metadata
		if len(metadataPacket.Metadata) == 0 {
			return 0, errors.New("empty witness metadata response")
		}

		metadata := metadataPacket.Metadata[0]

		// Validate that witness is available
		if !metadata.Available {
			return 0, fmt.Errorf("witness not available on peer %s for hash %s", p.ID(), hash)
		}

		p.witPeer.Peer.Log().Debug("Received witness metadata",
			"peer", p.ID(),
			"hash", hash,
			"totalPages", metadata.TotalPages,
			"witnessSize", metadata.WitnessSize,
			"blockNumber", metadata.BlockNumber,
			"available", metadata.Available)

		return metadata.TotalPages, nil

	case <-time.After(5 * time.Second):
		return 0, fmt.Errorf("timeout waiting for witness metadata from peer %s", p.ID())
	}
}

// requestWitnessPageCountLegacy is the fallback method for WIT1 peers that don't support metadata requests.
// It requests page 0 to get the TotalPages field.
func (p *ethPeer) requestWitnessPageCountLegacy(hash common.Hash) (uint64, error) {
	p.witPeer.Peer.Log().Trace("RequestWitnessPageCount (legacy) called", "peer", p.ID(), "hash", hash)

	// Request only the first page (page 0) to get TotalPages metadata
	witResCh := make(chan *wit.Response, 1)
	request := []wit.WitnessPageRequest{{Hash: hash, Page: 0}}

	witReq, err := p.witPeer.Peer.RequestWitness(request, witResCh)
	if err != nil {
		p.witPeer.Peer.Log().Error("Error requesting witness page count (legacy)", "peer", p.ID(), "err", err)
		return 0, err
	}
	defer witReq.Close()

	// Wait for the first page response with timeout
	select {
	case witRes := <-witResCh:
		if witRes == nil {
			return 0, errors.New("nil witness response")
		}

		// Extract WitnessPacketRLPPacket from the response
		witPacket, ok := witRes.Res.(*wit.WitnessPacketRLPPacket)
		if !ok {
			return 0, fmt.Errorf("unexpected witness response type: %T", witRes.Res)
		}

		// Extract TotalPages from the first page
		if len(witPacket.WitnessPacketResponse) == 0 {
			return 0, errors.New("empty witness packet response")
		}

		totalPages := witPacket.WitnessPacketResponse[0].TotalPages
		p.witPeer.Peer.Log().Debug("Received witness page count (legacy)", "peer", p.ID(), "hash", hash, "totalPages", totalPages)

		return totalPages, nil

	case <-time.After(5 * time.Second):
		return 0, fmt.Errorf("timeout waiting for witness page count from peer %s", p.ID())
	}
}

// RequestWitnessesWithVerification requests witnesses with optional page count verification.
// useCompact is a slice where useCompact[i] determines whether to request compact witness for hashes[i].
// If useCompact is nil or shorter than hashes, the fallback useCompactDefault is used for missing entries.
func (p *ethPeer) RequestWitnessesWithVerification(hashes []common.Hash, dlResCh chan *eth.Response, verifyPageCount func(common.Hash, uint64, string) bool, useCompact []bool, useCompactDefault bool) (*eth.Request, error) {
	if p.witPeer == nil {
		return nil, errors.New("witness peer not found")
	}
	p.witPeer.Peer.Log().Trace("RequestWitnesses called", "peer", p.ID(), "hashes", len(hashes))

	witReqResCh := make(chan *witReqRes, DefaultConcurrentResponsesHandled)
	witReqSem := make(chan int, DefaultConcurrentRequestsPerPeer) // semaphore to limit concurrent requests
	var witReqs []*wit.Request
	var witReqsWg sync.WaitGroup
	witTotalPages := make(map[common.Hash]uint64)   // witness hash and its total pages required
	witTotalRequest := make(map[common.Hash]uint64) // witness hash and its total requests
	failedRequests := make(map[common.Hash]map[uint64]witReqRetryCount)
	downloadPaused := make(map[common.Hash]bool) // Track if download is paused for verification
	var mapsMu sync.RWMutex
	var buildRequestMu sync.RWMutex

	// Build all the initial requests synchronously.
	buildWitReqErr := p.buildWitnessRequests(hashes, &witReqs, &witReqsWg, witTotalPages, witTotalRequest, witReqResCh, witReqSem, &mapsMu, &buildRequestMu, failedRequests, useCompact, useCompactDefault)
	if buildWitReqErr != nil {
		p.witPeer.Peer.Log().Error("Error in building witness requests", "peer", p.ID(), "err", buildWitReqErr)
		return nil, buildWitReqErr
	}

	// Close the witResCh after all the requests are done.
	go func() {
		defer close(witReqResCh)
		witReqsWg.Wait()
	}()

	// Create the wrapper request. Embed a minimal eth.Request shim.
	// Its primary purpose is type compatibility for the return value.
	// The ethWitRequest's Close method handles actual cancellation via witReq.
	// *** Crucially, set the Peer field so the concurrent fetcher can find the peer ***
	ethReqShim := &eth.Request{
		Peer:   p.ID(),              // Set the Peer ID here
		Cancel: make(chan struct{}), // Initialize the cancel channel
	}
	wrapperReq := &ethWitRequest{
		Request: ethReqShim,
		witReqs: witReqs,
	}

	// Start a goroutine to adapt responses from the wit channel to the eth channel.
	go func() {
		p.witPeer.Peer.Log().Trace("RequestWitnesses adapter goroutine started", "peer", p.ID())
		defer p.witPeer.Peer.Log().Trace("RequestWitnesses adapter goroutine finished", "peer", p.ID())

		receivedWitPages := make(map[common.Hash][]wit.WitnessPageResponse)
		reconstructedWitness := make(map[common.Hash]*stateless.Witness)
		var lastWitRes *wit.Response
		for witRes := range witReqResCh {
			p.receiveWitnessPage(witRes, receivedWitPages, reconstructedWitness, hashes, &witReqs, &witReqsWg, witTotalPages, witTotalRequest, witReqResCh, witReqSem, &mapsMu, &buildRequestMu, failedRequests, downloadPaused, verifyPageCount, useCompact, useCompactDefault)

			<-witReqSem
			// Check if the Response is nil before accessing the Done channel.
			if witRes.Response != nil && witRes.Response.Done != nil {
				witRes.Response.Done <- nil
				lastWitRes = witRes.Response
			}
			witReqsWg.Done()
		}
		p.witPeer.Peer.Log().Trace("RequestWitnesses adapter finished receiving responses", "peer", p.ID())

		// Check if we successfully received and processed witness data from the peer.
		// This prevents nil pointer dereference by checking multiple failure scenarios.
		// - len(receivedWitPages) == 0: No witness pages received at all.
		// - len(reconstructedWitness) == 0: No witness data reconstructed from the received pages.
		// - lastWitRes == nil: Same as len(reconstructedWitness) == 0, because if we have even one valid witness, lastWitRes will not be nil.
		log.Info("PSP - debug: RequestWitnesses finished receiving pages", "peer", p.ID(), "receivedPages", len(receivedWitPages), "reconstructedWitnesses", len(reconstructedWitness), "lastWitRes", lastWitRes != nil)
		if len(receivedWitPages) == 0 || len(reconstructedWitness) == 0 || lastWitRes == nil {
			log.Warn("PSP - debug: Empty response received for witnesses", "peer", p.ID(), "requestedHashes", len(hashes), "receivedPages", len(receivedWitPages), "reconstructedWitnesses", len(reconstructedWitness))
			p.witPeer.Peer.Log().Warn("Empty response received for witnesses requested from peer", "peer", p.ID(), "requestedHashes", hashes)

			doneCh := make(chan error)
			go func() {
				<-doneCh
			}()

			emptyWitnesses := make([]*stateless.Witness, 0)
			emptyRes := &eth.Response{
				Req:  wrapperReq.Request,
				Res:  emptyWitnesses,
				Meta: nil,
				Time: 0,
				Done: doneCh,
			}

			select {
			case dlResCh <- emptyRes:
				p.witPeer.Peer.Log().Trace("RequestWitnesses sent empty response because of empty witness data received from peer", "peer", p.ID())
			case <-wrapperReq.Request.Cancel:
				p.witPeer.Peer.Log().Trace("RequestWitnesses cancelled before sending empty response", "peer", p.ID())
				return
			}
			return
		}

		var witnesses []*stateless.Witness
		var responseHashes []common.Hash
		for hash, wit := range reconstructedWitness {
			witnesses = append(witnesses, wit)
			responseHashes = append(responseHashes, hash)
		}
		if len(witnesses) != len(hashes) {
			p.witPeer.Peer.Log().Error("Not able to fetch all requests witnesses", "peer", p.ID(), "requestedHashes", hashes, "responseHashes", responseHashes)
		}
		doneCh := make(chan error)
		go func() {
			<-doneCh
		}()

		// Adapt wit.Response[] to eth.Response.
		// We can only copy exported fields. The unexported fields (id, recv, code)
		// will have zero values in the ethRes sent to the caller.
		// Correlation still works via the Req field.
		ethRes := &eth.Response{
			Req:  wrapperReq.Request, // Point back to the embedded shim request.
			Res:  witnesses,
			Meta: lastWitRes.Meta,
			Time: lastWitRes.Time,
			Done: doneCh, // Send an ephemeral doneCh to keep compatibility.
		}

		// Forward the adapted response to the downloader's channel,
		// or stop if the request has been cancelled.
		log.Info("PSP - debug: RequestWitnesses sending response to downloader", "peer", p.ID(), "witnessCount", len(witnesses), "hashes", len(hashes))
		select {
		case dlResCh <- ethRes:
			log.Info("PSP - debug: RequestWitnesses successfully sent response to downloader", "peer", p.ID(), "witnessCount", len(witnesses))
			p.witPeer.Peer.Log().Trace("RequestWitnesses adapter forwarded eth response", "peer", p.ID())
		case <-wrapperReq.Request.Cancel:
			log.Info("PSP - debug: RequestWitnesses cancelled before sending response", "peer", p.ID())
			p.witPeer.Peer.Log().Trace("RequestWitnesses adapter cancelled before forwarding response", "peer", p.ID())
			// If cancelled, exit the goroutine. The closing of witResCh
			// will also eventually terminate the loop, but returning
			// here ensures we don't block trying to send after cancellation.
			return
		}
	}()

	// Return the embedded *eth.Request part of the wrapper.
	// This satisfies the function signature.
	p.witPeer.Peer.Log().Trace("RequestWitnesses returning ethReq shim", "peer", p.ID())
	return wrapperReq.Request, nil
}

func (p *ethPeer) receiveWitnessPage(
	witReqRes *witReqRes,
	receivedWitPages map[common.Hash][]wit.WitnessPageResponse,
	reconstructedWitness map[common.Hash]*stateless.Witness,
	hashes []common.Hash,
	witReqs *[]*wit.Request,
	witReqsWg *sync.WaitGroup,
	witTotalPages map[common.Hash]uint64,
	witTotalRequest map[common.Hash]uint64,
	witResCh chan *witReqRes,
	witReqSem chan int,
	mapsMu *sync.RWMutex,
	buildRequestMu *sync.RWMutex,
	failedRequests map[common.Hash]map[uint64]witReqRetryCount,
	downloadPaused map[common.Hash]bool,
	verifyPageCount func(common.Hash, uint64, string) bool,
	useCompact []bool,
	useCompactDefault bool,
) (retrievedError error) {
	defer func() {
		// if fails map on retry count and request again
		if retrievedError != nil {
			for _, request := range witReqRes.Request {
				if failedRequests[request.Hash] == nil {
					failedRequests[request.Hash] = make(map[uint64]witReqRetryCount)
				}
				retryCount := failedRequests[request.Hash][request.Page]
				retryCount.FailCount++
				if retryCount.FailCount <= DefaultMaxPagesRequestRetries {
					retryCount.ShouldRetryAgain = true
				}
				failedRequests[request.Hash][request.Page] = retryCount
			}

			// non blocking call to avoid race condition because of semaphore
			witReqsWg.Add(1) // protecting from not finishing before requests are built
			go func() {
				buildWitReqErr := p.buildWitnessRequests(hashes, witReqs, witReqsWg, witTotalPages, witTotalRequest, witResCh, witReqSem, mapsMu, buildRequestMu, failedRequests, useCompact, useCompactDefault)
				if buildWitReqErr != nil {
					p.witPeer.Peer.Log().Error("Error in building witness requests", "peer", p.ID(), "err", buildWitReqErr)
				}
				witReqsWg.Done()
			}()
		}
	}()

	// Check if the Response is nil.
	if witReqRes.Response == nil {
		p.witPeer.Peer.Log().Warn("RequestWitnesses received nil response from peer", "peer", p.ID())
		return errors.New("received nil response")
	}

	witPacketPtr, ok := witReqRes.Response.Res.(*wit.WitnessPacketRLPPacket)
	if !ok {
		p.witPeer.Peer.Log().Error("RequestWitnesses received unexpected response type", "type", fmt.Sprintf("%T", witReqRes.Response), "peer", p.ID())
		return errors.New("RequestWitnesses received unexpected response type")
	}

	for _, page := range witPacketPtr.WitnessPacketResponse {
		p.witPeer.Peer.Log().Trace("RequestWitnesses adapter received wit page response", "peer", p.ID(), "hash", page.Hash, "page", page.Page, "TotalPages", page.TotalPages, "lenData", len(page.Data))
		if len(page.Data) == 0 {
			continue
		}

		// Validate that current page number is within bounds
		if page.Page >= page.TotalPages {
			p.witPeer.Peer.Log().Warn("Peer sent invalid page number, dropping peer", "peer", p.ID(), "hash", page.Hash, "page", page.Page, "totalPages", page.TotalPages)
			return fmt.Errorf("peer sent invalid page number: page=%d >= totalPages=%d", page.Page, page.TotalPages)
		}

		receivedWitPages[page.Hash] = append(receivedWitPages[page.Hash], page)
		if len(receivedWitPages[page.Hash]) == int(page.TotalPages) {
			wit, err := p.reconstructWitness(receivedWitPages[page.Hash])
			if err != nil {
				return err
			}
			reconstructedWitness[page.Hash] = wit
		}

		// Check and validate TotalPages consistency
		mapsMu.Lock()
		existingTotalPages, hasTotalPages := witTotalPages[page.Hash]
		if hasTotalPages {
			// We already know TotalPages - verify it hasn't changed
			if existingTotalPages != page.TotalPages {
				mapsMu.Unlock()
				p.witPeer.Peer.Log().Warn("Peer sent inconsistent TotalPages, dropping peer", "peer", p.ID(), "hash", page.Hash, "existing", existingTotalPages, "new", page.TotalPages)
				downloadPaused[page.Hash] = true
				return fmt.Errorf("peer sent inconsistent TotalPages: existing=%d, new=%d", existingTotalPages, page.TotalPages)
			}
		} else {
			// First time learning TotalPages - store it
			witTotalPages[page.Hash] = page.TotalPages
		}
		mapsMu.Unlock()

		// Trigger page count verification if callback is provided (only on first page)
		// If verification fails (peer is dishonest), drop the peer immediately
		if verifyPageCount != nil && !hasTotalPages {
			isHonest := verifyPageCount(page.Hash, page.TotalPages, p.ID())
			if !isHonest {
				// Peer is dishonest - pause download and discard pages
				mapsMu.Lock()
				downloadPaused[page.Hash] = true
				mapsMu.Unlock()
				p.witPeer.Peer.Log().Warn("Peer failed verification, dropping peer", "peer", p.ID(), "hash", page.Hash, "totalPages", page.TotalPages)
				// Return error to trigger peer drop
				return fmt.Errorf("peer failed page count verification: claimed=%d pages", page.TotalPages)
			}
		}

		// Additional check: Verify we haven't received more pages than claimed
		mapsMu.RLock()
		currentTotalPages := witTotalPages[page.Hash]
		mapsMu.RUnlock()

		if len(receivedWitPages[page.Hash]) > int(currentTotalPages) {
			p.witPeer.Peer.Log().Warn("Peer sent more pages than TotalPages, dropping peer", "peer", p.ID(), "hash", page.Hash, "received", len(receivedWitPages[page.Hash]), "total", currentTotalPages)
			mapsMu.Lock()
			downloadPaused[page.Hash] = true
			mapsMu.Unlock()
			return fmt.Errorf("peer sent more pages than TotalPages: received=%d, total=%d", len(receivedWitPages[page.Hash]), currentTotalPages)
		}

		// Check if download is paused before building more requests
		mapsMu.RLock()
		paused := downloadPaused[page.Hash]
		mapsMu.RUnlock()

		if paused {
			// Download is paused, don't build more requests
			return nil
		}

		// non blocking call to avoid race condition because of semaphore
		witReqsWg.Add(1) // protecting from not finishing before requests are built
		go func() {
			buildWitReqErr := p.buildWitnessRequests(hashes, witReqs, witReqsWg, witTotalPages, witTotalRequest, witResCh, witReqSem, mapsMu, buildRequestMu, failedRequests, useCompact, useCompactDefault)
			if buildWitReqErr != nil {
				p.witPeer.Peer.Log().Error("Error in building witness requests", "peer", p.ID(), "err", buildWitReqErr)
			}
			witReqsWg.Done()
		}()
	}
	return nil
}

func (p *ethPeer) reconstructWitness(pages []wit.WitnessPageResponse) (*stateless.Witness, error) {
	// sort pages
	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Page < pages[j].Page
	})

	var reconstructedWitnessRLPBytes []byte
	for _, page := range pages {
		reconstructedWitnessRLPBytes = append(reconstructedWitnessRLPBytes, page.Data...)
	}

	wit := new(stateless.Witness)
	if err := rlp.DecodeBytes(reconstructedWitnessRLPBytes, wit); err != nil {
		p.witPeer.Peer.Log().Warn("RequestWitnesses adapter failed to decode witness page RLP", "err", err)
		return nil, err
	}
	return wit, nil
}

func (p *ethPeer) buildWitnessRequests(hashes []common.Hash,
	witReqs *[]*wit.Request,
	witReqsWg *sync.WaitGroup,
	witTotalPages map[common.Hash]uint64,
	witTotalRequest map[common.Hash]uint64,
	witReqResCh chan *witReqRes,
	witReqSem chan int,
	mapsMu *sync.RWMutex,
	buildRequestMu *sync.RWMutex,
	failedRequests map[common.Hash]map[uint64]witReqRetryCount,
	useCompact []bool,
	useCompactDefault bool,
) error {
	buildRequestMu.Lock()
	defer buildRequestMu.Unlock()

	// Build hashes list for logging
	hashesList := make([]string, 0, len(hashes))
	for _, h := range hashes {
		hashesList = append(hashesList, h.Hex()[:16])
	}
	log.Info("PSP - debug: Building witness requests",
		"hashes", len(hashes),
		"hashesList", hashesList)

	// Build useCompact map from slice, using default for missing entries
	useCompactMap := make(map[common.Hash]bool, len(hashes))
	for i, hash := range hashes {
		if i < len(useCompact) {
			useCompactMap[hash] = useCompact[i]
		} else {
			useCompactMap[hash] = useCompactDefault
		}
	}

	//checking requests to be done
	for _, hash := range hashes {
		mapsMu.RLock()
		start := witTotalRequest[hash]
		end, ok := witTotalPages[hash]
		mapsMu.RUnlock()
		if !ok || end == 0 {
			end = DefaultPagesRequestPerWitness
		}

		hashUseCompact := useCompactMap[hash]
		for page := start; page < end; page++ {
			if err := p.doWitnessRequest(
				hash,
				page,
				witReqs,
				witReqsWg,
				witReqResCh,
				witReqSem,
				mapsMu,
				witTotalRequest,
				hashUseCompact,
			); err != nil {
				return err
			}
		}
	}

	// checking failed requests to retry
	for hash, _ := range failedRequests {
		hashUseCompact := useCompactMap[hash]
		for page, _ := range failedRequests[hash] {
			retryCount := failedRequests[hash][page]
			if retryCount.ShouldRetryAgain {
				if err := p.doWitnessRequest(
					hash,
					page,
					witReqs,
					witReqsWg,
					witReqResCh,
					witReqSem,
					mapsMu,
					witTotalRequest,
					hashUseCompact,
				); err != nil {
					return err
				}
				retryCount.ShouldRetryAgain = false
			}
			failedRequests[hash][page] = retryCount
		}
	}
	return nil
}

// doWitnessRequest handles creating and dispatching a single witness request for a given hash and page.
func (p *ethPeer) doWitnessRequest(
	hash common.Hash,
	page uint64,
	witReqs *[]*wit.Request,
	witReqsWg *sync.WaitGroup,
	witReqResCh chan *witReqRes,
	witReqSem chan int,
	mapsMu *sync.RWMutex,
	witTotalRequest map[common.Hash]uint64,
	useCompact bool,
) error {
	// Compact witness requires WIT2 protocol support
	// Fallback to full witness for WIT1 peers for backward compatibility
	if useCompact && p.witPeer.Peer.Version() < wit.WIT2 {
		p.witPeer.Peer.Log().Info("Peer doesn't support WIT2, falling back to full witness", "peer", p.ID(), "version", p.witPeer.Peer.Version())
		useCompact = false
	}

	p.witPeer.Peer.Log().Debug("RequestWitnesses building a wit request", "peer", p.ID(), "hash", hash, "page", page, "compact", useCompact)
	witReqSem <- 1
	witResCh := make(chan *wit.Response)
	request := []wit.WitnessPageRequest{{Hash: hash, Page: page}}

	var witReq *wit.Request
	var err error

	// Use compact or full witness request based on parameter
	if useCompact {
		witReq, err = p.witPeer.Peer.RequestCompactWitness(request, witResCh)
	} else {
		witReq, err = p.witPeer.Peer.RequestWitness(request, witResCh)
	}

	if err != nil {
		p.witPeer.Peer.Log().Error("Error in making wit request", "peer", p.ID(), "compact", useCompact, "err", err)
		return err
	}
	go func() {
		witRes := <-witResCh
		// fan in to group all responses in single WitReqResCh
		witReqResCh <- &witReqRes{
			Request:  request,
			Response: witRes,
		}
	}()
	witReqsWg.Add(1)
	*witReqs = append(*witReqs, witReq)

	if page >= witTotalRequest[hash] {
		mapsMu.Lock()
		witTotalRequest[hash]++
		mapsMu.Unlock()
	}

	return nil
}
