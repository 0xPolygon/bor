// Copyright 2024 The go-ethereum Authors
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

package downloader

import (
	"errors"
	"fmt"
	"time"

	// Assuming witnesses are related to types
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

// shouldRequestCompactWitnessForBlock determines if we should request a compact witness
// for a given block number. This replicates the logic from eth/handler_eth.go but
// works with the downloader's BlockChain interface.
func shouldRequestCompactWitnessForBlock(d *Downloader, blockNum uint64) bool {
	windowSize := d.blockchain.GetCacheWindowSize()
	overlapSize := d.blockchain.GetCacheOverlapSize()
	cacheWarm := d.blockchain.IsCompactCacheWarm()

	// If the cache is cold (e.g., after restart), always request full witnesses.
	// The cache must be warmed by processing a window-start block with a full witness
	// before we can safely use compact witnesses.
	// Note: We do NOT optimize by checking if currentHeadNum >= blockWindowStart because
	// after a restart, the cache is lost even if the head is past the window start.
	// We must rely solely on IsCompactCacheWarm() which correctly returns false
	// when cacheWindowStart == 0 (uninitialized after restart).

	// Use the shared logic from core package
	return core.ShouldUseCompactWitness(
		cacheWarm,
		d.blockchain.IsParallelStatelessImportEnabled(),
		blockNum,
		windowSize,
		overlapSize,
	)
}

// witnessQueue implements typedQueue and is a type adapter between the generic
// concurrent fetcher and the downloader.
type witnessQueue Downloader

// waker returns a notification channel that gets pinged in case more witness
// fetches have been queued up, so the fetcher might assign it to idle peers.
// Note: This assumes a 'witnessWakeCh' exists or will be added to downloader.queue.
func (q *witnessQueue) waker() chan bool {
	return q.queue.witnessWakeCh // Placeholder: Needs implementation in queue struct
}

// pending returns the number of witnesses that are currently queued for fetching
// by the concurrent downloader.
// Note: This assumes a 'PendingWitnesses' method exists or will be added to downloader.queue.
func (q *witnessQueue) pending() int {
	return q.queue.PendingWitnesses() // Placeholder: Needs implementation in queue struct
}

// capacity is responsible for calculating how many witnesses a particular peer is
// estimated to be able to retrieve within the allotted round trip time.
// Note: This assumes a 'WitnessCapacity' method exists or will be added to peerConnection.
func (q *witnessQueue) capacity(peer *peerConnection, rtt time.Duration) int {
	return peer.WitnessCapacity(rtt) // Placeholder: Needs implementation in peerConnection
}

// updateCapacity is responsible for updating how many witnesses a particular peer
// is estimated to be able to retrieve in a unit time.
// Note: This assumes an 'UpdateWitnessRate' method exists or will be added to peerConnection.
func (q *witnessQueue) updateCapacity(peer *peerConnection, items int, span time.Duration) {
	peer.UpdateWitnessRate(items, span) // Placeholder: Needs implementation in peerConnection
}

// reserve is responsible for allocating a requested number of pending witnesses
// from the download queue to the specified peer.
// Note: This assumes a 'ReserveWitnesses' method exists or will be added to downloader.queue.
func (q *witnessQueue) reserve(peer *peerConnection, items int) (*fetchRequest, bool, bool) {
	// We don't limit requests here to avoid breaking the delivery mechanism.
	// Instead, we rely on the import batch limiting in processFullSyncContentStateless to
	// ensure we process a window-start block before importing too many blocks ahead.
	// This is simpler and safer than modifying requests after they're reserved.
	req, _, progress, throttle := q.queue.ReserveWitnesses(peer, items)
	return req, progress, throttle
}

// unreserve is responsible for removing the current witness retrieval allocation
// assigned to a specific peer and placing it back into the pool to allow
// reassigning to some other peer.
// Note: This assumes an 'ExpireWitnesses' method exists or will be added to downloader.queue.
func (q *witnessQueue) unreserve(peer string) int {
	fails := q.queue.ExpireWitnesses(peer) // Placeholder: Needs implementation in queue struct
	if fails > 2 {
		log.Trace("Witness delivery timed out", "peer", peer)
	} else {
		log.Debug("Witness delivery stalling", "peer", peer)
	}
	return fails
}

// request is responsible for converting a generic fetch request into a witness
// one and sending it to the remote peer for fulfillment using the wit protocol.
func (q *witnessQueue) request(peer *peerConnection, req *fetchRequest, resCh chan *eth.Response) (*eth.Request, error) {
	// Safety check: ensure the peer supports witness protocol
	if !peer.peer.SupportsWitness() {
		peer.log.Warn("Attempted to request witnesses from non-witness peer", "peer", peer.id)
		return nil, errors.New("peer does not support witness protocol")
	}

	// Extract hashes from the headers in the fetch request.
	hashes := make([]common.Hash, 0, len(req.Headers))
	for _, header := range req.Headers {
		hashes = append(hashes, header.Hash())
	}

	if len(hashes) == 0 {
		peer.log.Warn("Cannot form witness request, no headers in fetchRequest")
		return nil, errors.New("invalid witness fetch request: no hashes")
	}

	peer.log.Trace("Requesting new batch of witnesses", "count", len(hashes), "from_hash", hashes[0])

	// Safety check: if cache is cold and block is too far ahead, log warning
	// This is a fallback in case reserve() didn't catch it
	d := (*Downloader)(q)
	if !d.blockchain.IsCompactCacheWarm() && len(req.Headers) > 0 {
		currentHead := d.blockchain.CurrentBlock()
		if currentHead != nil {
			windowSize := d.blockchain.GetCacheWindowSize()
			if windowSize > 0 {
				currentHeadNum := currentHead.Number.Uint64()
				nextWindowStart := ((currentHeadNum / windowSize) + 1) * windowSize
				firstBlockNum := req.Headers[0].Number.Uint64()

				if firstBlockNum > nextWindowStart {
					log.Warn("PSP - Requesting witness for block ahead of next window-start when cache is cold",
						"blockNum", firstBlockNum,
						"currentHead", currentHeadNum,
						"nextWindowStart", nextWindowStart,
						"reason", "cache cold - should have been limited by reserve()")
				}
			}
		}
	}

	// Determine useCompact for each block in the batch.
	// This allows per-block determination of compact vs full witnesses within a single batch.
	useCompact := make([]bool, len(req.Headers))
	for i, header := range req.Headers {
		blockNum := header.Number.Uint64()
		useCompact[i] = shouldRequestCompactWitnessForBlock((*Downloader)(q), blockNum)
	}

	log.Info("PSP - debug: Downloader requesting witnesses",
		"count", len(hashes),
		"firstBlock", req.Headers[0].Number.Uint64(),
		"lastBlock", req.Headers[len(req.Headers)-1].Number.Uint64())

	// Use RequestWitnessesWithVerification with per-block useCompact decisions.
	// Note: We pass nil for verifyPageCount since the downloader doesn't have access to the handler's verification callback.
	// We pass false as the fallback useCompactDefault (not used when useCompact slice is provided).
	return peer.peer.RequestWitnessesWithVerification(hashes, resCh, nil, useCompact, false)
}

// deliver is responsible for taking a generic response packet from the concurrent
// fetcher, unpacking the witness data (using wit protocol definitions) and delivering
// it to the downloader's queue.
func (q *witnessQueue) deliver(peer *peerConnection, packet *eth.Response) (int, error) {
	log.Info("PSP - debug: Delivering witness response", "peer", peer.id, "responseType", fmt.Sprintf("%T", packet.Res))
	// Check the actual response type. Should be a pointer to WitnessPacketRLPPacket.
	witPacketData, ok := packet.Res.([]*stateless.Witness) // Expect pointer type
	if !ok {
		peer.log.Warn("Witness deliver unexpected response type", "type", fmt.Sprintf("%T", packet.Res))
		return 0, fmt.Errorf("unexpected response type: %T", packet.Res)
	}

	numWitnesses := len(witPacketData) // Number of raw witness blobs received
	log.Info("PSP - debug: Received witness response", "peer", peer.id, "count", numWitnesses)

	// Placeholder: Needs DeliverWitnesses method definition in queue struct
	// Adjust DeliverWitnesses to accept the raw RLP data or decoded witnesses.
	// Pass witnessData ( []rlp.RawValue ) or decoded data.
	// The `requests` parameter used previously seems incorrect based on witPacket structure.
	// Assuming the signature in queue.go will be updated to accept []rlp.RawValue and interface{}
	accepted, err := q.queue.DeliverWitnesses(peer.id, witPacketData, packet.Meta) // Pass raw witness data and potential metadata

	switch {
	case err == nil && numWitnesses == 0:
		log.Info("PSP - debug: Requested witnesses delivered (empty batch)", "peer", peer.id)
	case err == nil:
		log.Info("PSP - debug: Delivered new batch of witnesses", "peer", peer.id, "count", numWitnesses, "accepted", accepted)
	default:
		log.Warn("PSP - debug: Failed to deliver retrieved witnesses", "peer", peer.id, "err", err, "count", numWitnesses)
	}

	return accepted, err
}
