// Copyright 2026 The go-ethereum Authors
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

package fetcher

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

// headerRequest carries what a header worker needs by value. The announce
// pointer is an ownership token only; its mutable fields belong to the loop.
type headerRequest struct {
	hash        common.Hash
	announce    *blockAnnounce
	fetch       headerRequesterFn
	announcedAt time.Time
}

type bodyRequest struct {
	hashes      []common.Hash
	announces   []*blockAnnounce
	fetch       bodyRequesterFn
	announcedAt time.Time
}

// runHeaderRequest performs one header retrieval using a slot the loop already
// reserved, releasing it on every exit. A blocked dispatch keeps that slot
// until the call below returns and the request is closed; the response deadline
// does not start until then, which is why admission and not the deadline is
// what bounds outstanding requests.
func (f *BlockFetcher) runHeaderRequest(peer string, req headerRequest) {
	defer f.releaseFetchSlot(peer)

	owned := []*blockAnnounce{req.announce}
	if req.fetch == nil {
		f.notifyFetchRelease(true, owned)
		return
	}
	resCh := make(chan *eth.Response)

	request, err := req.fetch(req.hash, resCh)
	if err != nil {
		log.Debug("Failed to request header", "peer", peer, "hash", req.hash, "err", err)
		f.notifyFetchRelease(true, owned)

		return
	}
	defer request.Close()

	timeout := time.NewTimer(2 * fetchTimeout)
	defer timeout.Stop()

	select {
	case res := <-resCh:
		f.deliverHeaders(peer, req, res)
	case <-timeout.C:
		log.Debug("BlockFetcher: header fetch timed out", "peer", peer, "hash", req.hash, "timeout", 2*fetchTimeout, "backoff", fetchPeerBackoff)
		f.backoffFetchPeer(peer)
		f.notifyFetchRelease(true, owned)
	case <-f.quit:
	}
}

// deliverHeaders filters a header response, then returns ownership. A response
// that did not carry the requested header leaves the announcement owned here,
// so releasing unconditionally is what lets a later announcement retry.
func (f *BlockFetcher) deliverHeaders(peer string, req headerRequest, res *eth.Response) {
	owned := []*blockAnnounce{req.announce}
	if res == nil {
		f.notifyFetchRelease(true, owned)
		return
	}
	res.Done <- nil

	headers, ok := res.Res.(*eth.BlockHeadersRequest)
	if !ok || headers == nil {
		f.notifyFetchRelease(true, owned)
		return
	}
	metrics.RecordPerItemDuration(blockHeaderItemDownloadTimer, res.Time, len(*headers))
	f.FilterHeaders(peer, *headers, time.Now(), req.announcedAt)
	f.notifyFetchRelease(true, owned)
}

// runBodyRequest performs one body retrieval for a batch of hashes.
func (f *BlockFetcher) runBodyRequest(peer string, req bodyRequest) {
	defer f.releaseFetchSlot(peer)

	bodyFetchMeter.Mark(int64(len(req.hashes)))

	if req.fetch == nil {
		f.notifyFetchRelease(false, req.announces)
		return
	}
	resCh := make(chan *eth.Response)

	request, err := req.fetch(req.hashes, resCh)
	if err != nil {
		log.Debug("Failed to request bodies", "peer", peer, "hashes", req.hashes, "err", err)
		f.notifyFetchRelease(false, req.announces)

		return
	}
	defer request.Close()

	timeout := time.NewTimer(2 * fetchTimeout)
	defer timeout.Stop()

	select {
	case res := <-resCh:
		f.deliverBodies(peer, req, res)
	case <-timeout.C:
		log.Debug("BlockFetcher: body fetch timed out", "peer", peer, "hashes", req.hashes, "timeout", 2*fetchTimeout, "backoff", fetchPeerBackoff)
		f.backoffFetchPeer(peer)
		f.notifyFetchRelease(false, req.announces)
	case <-f.quit:
	}
}

// deliverBodies filters a body response, then returns ownership of the batch.
// Hashes an empty or partial reply did not cover are still owned here; the loop
// keeps the ones that reached the import queue or witness retrieval.
func (f *BlockFetcher) deliverBodies(peer string, req bodyRequest, res *eth.Response) {
	if res == nil {
		f.notifyFetchRelease(false, req.announces)
		return
	}
	res.Done <- nil

	bodies, ok := res.Res.(*eth.BlockBodiesResponse)
	if !ok || bodies == nil {
		f.notifyFetchRelease(false, req.announces)
		return
	}
	// Withdrawals are ignored here, since the block fetcher is not used post-merge.
	txs, uncles, _ := bodies.Unpack()
	metrics.RecordPerItemDuration(blockBodyItemDownloadTimer, res.Time, len(txs))
	f.FilterBodies(peer, txs, uncles, time.Now(), req.announcedAt)
	f.notifyFetchRelease(false, req.announces)
}
