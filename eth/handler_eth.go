// Copyright 2020 The go-ethereum Authors
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
	"math/big"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

// ethHandler implements the eth.Backend interface to handle the various network
// packets that are sent as replies or broadcasts.
type ethHandler handler

func (h *ethHandler) Chain() *core.BlockChain { return h.chain }
func (h *ethHandler) TxPool() eth.TxPool      { return h.txpool }

// RunPeer is invoked when a peer joins on the `eth` protocol.
func (h *ethHandler) RunPeer(peer *eth.Peer, hand eth.Handler) error {
	return (*handler)(h).runEthPeer(peer, hand)
}

// PeerInfo retrieves all known `eth` information about a peer.
func (h *ethHandler) PeerInfo(id enode.ID) interface{} {
	if p := h.peers.peer(id.String()); p != nil {
		return p.info()
	}

	return nil
}

// AcceptTxs retrieves whether transaction processing is enabled on the node
// or if inbound transactions should simply be dropped.
func (h *ethHandler) AcceptTxs() bool {
	return h.synced.Load()
}

// Handle is invoked from a peer's message handler when it receives a new remote
// message that the handler couldn't consume and serve itself.
func (h *ethHandler) Handle(peer *eth.Peer, packet eth.Packet) error {
	// Consume any broadcasts and announces, forwarding the rest to the downloader
	switch packet := packet.(type) {
	case *eth.NewBlockHashesPacket:
		hashes, numbers := packet.Unpack()
		return h.handleBlockAnnounces(peer, hashes, numbers)

	case *eth.NewBlockPacket:
		return h.handleBlockBroadcast(peer, packet.Block, packet.TD)

	case *eth.NewPooledTransactionHashesPacket:
		return h.txFetcher.Notify(peer.ID(), packet.Types, packet.Sizes, packet.Hashes)

	case *eth.TransactionsPacket:
		for _, tx := range *packet {
			if tx.Type() == types.BlobTxType {
				return errors.New("disallowed broadcast blob transaction")
			}
		}
		return h.txFetcher.Enqueue(peer.ID(), *packet, false)

	case *eth.PooledTransactionsResponse:
		// If we receive any blob transactions missing sidecars, or with
		// sidecars that don't correspond to the versioned hashes reported
		// in the header, disconnect from the sending peer.
		for _, tx := range *packet {
			if tx.Type() == types.BlobTxType {
				if tx.BlobTxSidecar() == nil {
					return errors.New("received sidecar-less blob transaction")
				}
				if err := tx.BlobTxSidecar().ValidateBlobCommitmentHashes(tx.BlobHashes()); err != nil {
					return err
				}
			}
		}
		return h.txFetcher.Enqueue(peer.ID(), *packet, true)

	default:
		return fmt.Errorf("unexpected eth packet type: %T", packet)
	}
}

// handleBlockAnnounces is invoked from a peer's message handler when it transmits a
// batch of block announcements for the local node to process.
func (h *ethHandler) handleBlockAnnounces(peer *eth.Peer, hashes []common.Hash, numbers []uint64) error {
	// Schedule all the unknown hashes for retrieval
	var (
		unknownHashes  = make([]common.Hash, 0, len(hashes))
		unknownNumbers = make([]uint64, 0, len(numbers))
	)

	for i := 0; i < len(hashes); i++ {
		if !h.chain.HasBlock(hashes[i], numbers[i]) {
			unknownHashes = append(unknownHashes, hashes[i])
			unknownNumbers = append(unknownNumbers, numbers[i])
		}
	}

	if h.statelessSync.Load() || h.syncWithWitnesses {
		for i := 0; i < len(unknownHashes); i++ {
			hash := unknownHashes[i]
			number := unknownNumbers[i]

			// Create witness requester closure that captures block number
			witnessRequester := func(hash common.Hash, sink chan *eth.Response) (*eth.Request, error) {
				// Get the ethPeer from the peerSet
				ethPeer := h.peers.getOnePeerWithWitness(hash)
				if ethPeer == nil {
					return nil, fmt.Errorf("no peer with witness for hash %s is available", hash)
				}

				// Determine if we should request compact witness based on sliding window
				useCompact := h.shouldRequestCompactWitness(number)

				// Request witnesses (compact or full) using the wit peer with verification
				return ethPeer.RequestWitnessesWithVerification([]common.Hash{hash}, sink, h.verifyPageCount, useCompact)
			}

			h.blockFetcher.Notify(peer.ID(), hash, number, time.Now(), peer.RequestOneHeader, peer.RequestBodies, witnessRequester)
		}
	} else {
		for i := 0; i < len(unknownHashes); i++ {
			h.blockFetcher.Notify(peer.ID(), unknownHashes[i], unknownNumbers[i], time.Now(), peer.RequestOneHeader, peer.RequestBodies, nil)
		}
	}

	return nil
}

// shouldRequestCompactWitness determines if we should request a compact witness based on
// the sliding window cache state. Returns true if witness should be compact, false for full.
func (h *ethHandler) shouldRequestCompactWitness(blockNum uint64) bool {
	windowSize := h.chain.GetCacheWindowSize()
	overlapSize := h.chain.GetCacheOverlapSize()
	cacheWarm := h.chain.IsCompactCacheWarm()
	cacheWindowStart := h.chain.GetCacheWindowStart()

	// Get current head for optimization check
	currentHead := h.chain.CurrentBlock()
	var currentHeadNum uint64
	if currentHead != nil {
		currentHeadNum = currentHead.Number.Uint64()
	}

	log.Info("PSP - shouldRequestCompactWitness decision",
		"blockNum", blockNum,
		"currentHead", currentHeadNum,
		"cacheWarm", cacheWarm,
		"cacheWindowStart", cacheWindowStart,
		"windowSize", windowSize,
		"overlapSize", overlapSize)

	// Optimization: If cache is cold after restart, we only need full witnesses
	// until the next window start (inclusive). After that, we can use compact witnesses.
	// This avoids requesting full witnesses for the entire backlog.
	// Example: If restarting at block 454 (window 440-459), request full witnesses
	// for blocks 455-460 (next window start), then compact for 461+.
	if !cacheWarm && windowSize > 0 && currentHead != nil {
		// If the requested block is more than windowSize blocks ahead of the current head,
		// we can use compact witnesses. This ensures that by the time we process
		// this block, we'll have processed at least one window-start block with
		// a full witness, warming the cache.
		// We check blockNum > currentHeadNum + windowSize to ensure we're at least
		// one full window ahead, which guarantees we'll have processed the next
		// window-start block by the time we get to this block.
		if blockNum > currentHeadNum+windowSize {
			// We're more than one window ahead, can use compact witness
			// This allows us to request compact witnesses for blocks in future windows
			// even if the cache isn't warm yet, since by the time we process them,
			// we'll have processed the window-start block with a full witness.
			cacheWarm = true
			log.Info("PSP - Optimization: allowing compact witness (block more than windowSize ahead)",
				"blockNum", blockNum,
				"currentHead", currentHeadNum,
				"windowSize", windowSize,
				"threshold", currentHeadNum+windowSize)
		} else {
			log.Info("PSP - Optimization: requiring full witness (block within windowSize)",
				"blockNum", blockNum,
				"currentHead", currentHeadNum,
				"windowSize", windowSize,
				"threshold", currentHeadNum+windowSize)
		}
	}

	result := shouldUseCompactWitness(
		cacheWarm,
		h.chain.IsParallelStatelessImportEnabled(),
		blockNum,
		windowSize,
		overlapSize,
	)

	log.Info("PSP - shouldRequestCompactWitness result",
		"blockNum", blockNum,
		"useCompact", result,
		"cacheWarm", cacheWarm)

	return result
}

func shouldUseCompactWitness(cacheWarm bool, parallel bool, blockNum, windowSize, overlapSize uint64) bool {
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

	// Block is within window (not at window start or overlap start), can use compact witness.
	return true
}

// verifyPageCount verifies the witness page count for a given block hash by
// comparing it against random peers' reported page counts.
// Returns true if the peer is honest (page count matches consensus), false otherwise.
//
// Optimization: The functions below (getRandomPeers, getWitnessPageCount) are passed as
// closures to CheckWitnessPageCount but are only executed if needed:
// - Not called if pageCount <= threshold (small witnesses)
// - Not called if cache hit (recently verified witnesses)
// This avoids unnecessary peer queries and metadata requests in most cases.
func (h *ethHandler) verifyPageCount(hash common.Hash, pageCount uint64, peer string) bool {
	// Define function to get random peers for verification
	// Note: This function is only called if verification is actually needed (cache miss + threshold exceeded)
	getRandomPeers := func() []string {
		allPeers := h.peers.getAllPeers()
		randomPeers := make([]string, 0, len(allPeers))
		for _, peer := range allPeers {
			if peer.SupportsWitness() {
				randomPeers = append(randomPeers, peer.ID())
			}
		}
		// Shuffle the peers to get random selection
		for i := len(randomPeers) - 1; i > 0; i-- {
			j := rand.Intn(i + 1)
			randomPeers[i], randomPeers[j] = randomPeers[j], randomPeers[i]
		}
		return randomPeers
	}

	// Define function to get witness page count from a peer
	// Note: This function is only called if verification is needed (after cache check)
	getWitnessPageCount := func(peerID string, hash common.Hash) (uint64, error) {
		peer := h.peers.peer(peerID)
		if peer == nil || !peer.SupportsWitness() {
			return 0, fmt.Errorf("peer %s not available or doesn't support witness", peerID)
		}

		// Use the new efficient method that only downloads page 0
		return peer.RequestWitnessPageCount(hash)
	}

	// Run synchronous verification and return result
	return h.blockFetcher.GetWitnessManager().CheckWitnessPageCount(hash, pageCount, peer, getRandomPeers, getWitnessPageCount)
}

// handleBlockBroadcast is invoked from a peer's message handler when it transmits a
// block broadcast for the local node to process.
func (h *ethHandler) handleBlockBroadcast(peer *eth.Peer, block *types.Block, td *big.Int) error {
	// If stateless sync is enabled, use the dedicated injectNeedWitness channel.
	// Otherwise, use the original Enqueue optimization.
	if h.statelessSync.Load() || h.syncWithWitnesses {
		log.Debug("Received block broadcast during stateless sync", "blockNumber", block.NumberU64(), "blockHash", block.Hash())

		// Determine if we should request compact witness for this block
		blockNum := block.NumberU64()
		useCompact := h.shouldRequestCompactWitness(blockNum)

		// Create a witness requester closure *only if* the peer supports the protocol.
		witnessRequester := func(hash common.Hash, sink chan *eth.Response) (*eth.Request, error) {
			// Get the ethPeer from the peerSet
			ethPeer := h.peers.getOnePeerWithWitness(hash)
			if ethPeer == nil {
				return nil, fmt.Errorf("no peer with witness for hash %s is available", hash)
			}

			// Request witnesses (compact or full) using the wit peer with verification
			return ethPeer.RequestWitnessesWithVerification([]common.Hash{hash}, sink, h.verifyPageCount, useCompact)
		}

		// Call the new fetcher method to inject the block
		if err := h.blockFetcher.InjectBlockWithWitnessRequirement(peer.ID(), block, witnessRequester); err != nil {
			// Log the error if injection failed (e.g., channel full)
			log.Debug("Failed to inject block requiring witness", "hash", block.Hash(), "peer", peer.ID(), "err", err)
			// Return nil? Or the error? Let's return nil as dropping isn't a peer protocol error.
			return nil
		}
	} else {
		// Not in stateless mode, use the direct Enqueue optimization.
		h.blockFetcher.Enqueue(peer.ID(), block)
	}

	// Assuming the block is importable by the peer, but possibly not yet done so,
	// calculate the head hash and TD that the peer truly must have.
	var (
		trueHead = block.ParentHash()
		trueTD   = new(big.Int).Sub(td, block.Difficulty())
	)
	// Update the peer's total difficulty if better than the previous
	if _, td := peer.Head(); trueTD.Cmp(td) > 0 {
		peer.SetHead(trueHead, trueTD)
		h.chainSync.handlePeerEvent()
	}

	return nil
}
