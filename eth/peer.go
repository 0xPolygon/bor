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
	return &ethPeerInfo{
		Version: p.Version(),
	}
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
	var mapsMu sync.RWMutex
	var buildRequestMu sync.RWMutex

	// non-blocking build first requests
	witReqsWg.Add(1)
	go func() {
		p.buildWitnessRequests(hashes, &witReqs, &witReqsWg, witTotalPages, witTotalRequest, witReqResCh, witReqSem, &mapsMu, &buildRequestMu, failedRequests)
		witReqsWg.Done()
	}()

	// closes witResCh after all requests are done
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
			p.receiveWitnessPage(witRes, receivedWitPages, reconstructedWitness, hashes, &witReqs, &witReqsWg, witTotalPages, witTotalRequest, witReqResCh, witReqSem, &mapsMu, &buildRequestMu, failedRequests)

			<-witReqSem
			witRes.Response.Done <- nil
			witReqsWg.Done()
			lastWitRes = witRes.Response
		}
		p.witPeer.Peer.Log().Trace("RequestWitnesses adapter received all responses", "peer", p.ID())

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
			Done: doneCh, // sends a ephemeral doneCh to keep compatibility
		}

		// Forward the adapted response to the downloader's channel,
		// or stop if the request has been cancelled.
		select {
		case dlResCh <- ethRes:
			p.witPeer.Peer.Log().Trace("RequestWitnesses adapter forwarded eth response", "peer", p.ID())
		case <-wrapperReq.Request.Cancel:
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
				p.buildWitnessRequests(hashes, witReqs, witReqsWg, witTotalPages, witTotalRequest, witResCh, witReqSem, mapsMu, buildRequestMu, failedRequests)
				witReqsWg.Done()
			}()
		}
	}()
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

		receivedWitPages[page.Hash] = append(receivedWitPages[page.Hash], page)
		if len(receivedWitPages[page.Hash]) == int(page.TotalPages) {
			wit, err := p.reconstructWitness(receivedWitPages[page.Hash])
			if err != nil {
				return err
			}
			reconstructedWitness[page.Hash] = wit
		}

		// check and build any remaining witnessRequest for the witnesses we dont know previously the totalPages
		mapsMu.Lock()
		witTotalPages[page.Hash] = page.TotalPages
		mapsMu.Unlock()

		// non blocking call to avoid race condition because of semaphore
		witReqsWg.Add(1) // protecting from not finishing before requests are built
		go func() {
			p.buildWitnessRequests(hashes, witReqs, witReqsWg, witTotalPages, witTotalRequest, witResCh, witReqSem, mapsMu, buildRequestMu, failedRequests)
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
) error {
	buildRequestMu.Lock()
	defer buildRequestMu.Unlock()

	//checking requests to be done
	for _, hash := range hashes {
		mapsMu.RLock()
		start := witTotalRequest[hash]
		end, ok := witTotalPages[hash]
		mapsMu.RUnlock()
		if !ok || end == 0 {
			end = DefaultPagesRequestPerWitness
		}

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
			); err != nil {
				return err
			}
		}
	}

	// checking failed requests to retry
	for hash, _ := range failedRequests {
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
) error {
	p.witPeer.Peer.Log().Debug("RequestWitnesses building a wit request", "peer", p.ID(), "hash", hash, "page", page)
	witReqSem <- 1
	witResCh := make(chan *wit.Response)
	request := []wit.WitnessPageRequest{{Hash: hash, Page: page}}
	witReq, err := p.witPeer.Peer.RequestWitness(request, witResCh)
	if err != nil {
		p.witPeer.Peer.Log().Error("RequestWitnesses failed to make wit request", "peer", p.ID(), "err", err)
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
