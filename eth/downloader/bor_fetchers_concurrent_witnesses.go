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

	// Get current head for optimization check
	currentHead := d.blockchain.CurrentBlock()
	var currentHeadNum uint64
	if currentHead != nil {
		currentHeadNum = currentHead.Number.Uint64()
	}

	// Optimization: If cache is cold after restart, we only need full witnesses
	// until the next window start (inclusive). After that, we can use compact witnesses.
	if !cacheWarm && windowSize > 0 && currentHead != nil {
		// Calculate the window start for the requested block
		blockWindowStart := (blockNum / windowSize) * windowSize

		// Never apply optimization to window-start blocks or overlap-start blocks.
		// These always require full witnesses, regardless of cache state.
		if blockNum == blockWindowStart {
			return false // Window-start block always requires full witness
		}
		if overlapSize > 0 {
			// Check if this is an overlap-start block
			var overlapStartOffset uint64
			if overlapSize >= windowSize {
				overlapStartOffset = 0
			} else {
				overlapStartOffset = windowSize - overlapSize
			}
			blockAtOverlapStart := blockWindowStart + overlapStartOffset
			if blockNum == blockAtOverlapStart {
				return false // Overlap-start block always requires full witness
			}
		}
		// Check if window-start has been processed
		if currentHeadNum >= blockWindowStart {
			cacheWarm = true // Window-start already processed, cache should be warm
		}
	}

	// Use the same logic as shouldUseCompactWitness
	return shouldUseCompactWitnessForDownloader(
		cacheWarm,
		d.blockchain.IsParallelStatelessImportEnabled(),
		blockNum,
		windowSize,
		overlapSize,
	)
}

// shouldUseCompactWitnessForDownloader replicates the logic from shouldUseCompactWitness
// in eth/handler_eth.go but is accessible from the downloader package.
func shouldUseCompactWitnessForDownloader(cacheWarm bool, parallel bool, blockNum, windowSize, overlapSize uint64) bool {
	// Compact witness requires sequential block processing.
	if parallel {
		return false
	}
	// If the compact cache hasn't been warmed yet (e.g. node restarted mid-window),
	// request full witnesses until we process the next window start.
	if !cacheWarm {
		return false
	}
	// Without a valid window, fall back to full witnesses.
	if windowSize == 0 {
		return false
	}

	// Calculate the consensus-aligned window start for the requested block.
	expectedWindowStart := (blockNum / windowSize) * windowSize
	if blockNum == expectedWindowStart {
		return false
	}

	// Calculate the first block of the overlap region for this window.
	if overlapSize == 0 {
		return true
	}
	var overlapStartOffset uint64
	if overlapSize >= windowSize {
		overlapStartOffset = 0
	} else {
		overlapStartOffset = windowSize - overlapSize
	}
	blockAtOverlapStart := expectedWindowStart + overlapStartOffset
	if blockNum == blockAtOverlapStart {
		return false
	}

	return true
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
	// Assuming ReserveWitnesses returns *fetchRequest, bool, bool like ReserveBodies
	return q.queue.ReserveWitnesses(peer, items) // Placeholder: Needs implementation in queue struct
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

	// Determine if we should request compact witnesses for this batch.
	// Since RequestWitnessesWithVerification takes a single useCompact flag for all hashes,
	// we check if ALL blocks should use compact witnesses. If any block needs a full witness,
	// we request full for all (conservative but safe).
	useCompact := true
	for _, header := range req.Headers {
		blockNum := header.Number.Uint64()
		if !shouldRequestCompactWitnessForBlock((*Downloader)(q), blockNum) {
			useCompact = false
			break
		}
	}

	log.Info("PSP - debug: Downloader requesting witnesses",
		"count", len(hashes),
		"useCompact", useCompact,
		"firstBlock", req.Headers[0].Number.Uint64(),
		"lastBlock", req.Headers[len(req.Headers)-1].Number.Uint64())

	// Use RequestWitnessesWithVerification with the determined useCompact flag
	// Note: We pass nil for verifyPageCount since the downloader doesn't have access to the handler's verification callback
	return peer.peer.RequestWitnessesWithVerification(hashes, resCh, nil, useCompact)
}

// deliver is responsible for taking a generic response packet from the concurrent
// fetcher, unpacking the witness data (using wit protocol definitions) and delivering
// it to the downloader's queue.
func (q *witnessQueue) deliver(peer *peerConnection, packet *eth.Response) (int, error) {
	log.Trace("Delivering witness response", "peer", peer.id)
	// Check the actual response type. Should be a pointer to WitnessPacketRLPPacket.
	witPacketData, ok := packet.Res.([]*stateless.Witness) // Expect pointer type
	if !ok {
		peer.log.Warn("Witness deliver unexpected response type", "type", fmt.Sprintf("%T", packet.Res))
		return 0, fmt.Errorf("unexpected response type: %T", packet.Res)
	}

	numWitnesses := len(witPacketData) // Number of raw witness blobs received

	// Placeholder: Needs DeliverWitnesses method definition in queue struct
	// Adjust DeliverWitnesses to accept the raw RLP data or decoded witnesses.
	// Pass witnessData ( []rlp.RawValue ) or decoded data.
	// The `requests` parameter used previously seems incorrect based on witPacket structure.
	// Assuming the signature in queue.go will be updated to accept []rlp.RawValue and interface{}
	accepted, err := q.queue.DeliverWitnesses(peer.id, witPacketData, packet.Meta) // Pass raw witness data and potential metadata

	switch {
	case err == nil && numWitnesses == 0:
		peer.log.Trace("Requested witnesses delivered (empty batch)")
	case err == nil:
		peer.log.Trace("Delivered new batch of witnesses", "count", numWitnesses, "accepted", accepted)
	default:
		peer.log.Debug("Failed to deliver retrieved witnesses", "err", err)
	}

	return accepted, err
}
