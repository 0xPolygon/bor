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
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
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

	case *eth.NewPooledTransactionHashesPacket67:
		return h.txFetcher.Notify(peer.ID(), nil, nil, *packet)

	case *eth.NewPooledTransactionHashesPacket68:
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

	var witnessRequester func(hash common.Hash, sink chan *eth.Response) (*eth.Request, error)
	if h.statelessSync.Load() || h.syncWithWitnesses {
		// Create a witness requester that uses the wit.Peer's RequestWitness method
		witnessRequester = func(hash common.Hash, sink chan *eth.Response) (*eth.Request, error) {
			// Get the ethPeer from the peerSet
			ethPeer := h.peers.getOnePeerWithWitness(hash)
			if ethPeer == nil {
				return nil, fmt.Errorf("no peer with witness for hash %s is available", hash)
			}

			// Create verification callback for page count verification
			verifyPageCount := func(hash common.Hash, pageCount uint64, peer string) {
				// Get random peers for verification
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

				// Get witness page count from a peer
				getWitnessPageCount := func(peerID string, hash common.Hash) (uint64, error) {
					peer := h.peers.peer(peerID)
					if peer == nil || !peer.SupportsWitness() {
						return 0, fmt.Errorf("peer %s not available or doesn't support witness", peerID)
					}

					// Create a temporary channel to get the page count
					resCh := make(chan *eth.Response, 1)
					req, err := peer.RequestWitnesses([]common.Hash{hash}, resCh)
					if err != nil {
						return 0, err
					}
					defer req.Close()

					// Wait for response with timeout
					select {
					case res := <-resCh:
						if res == nil {
							return 0, fmt.Errorf("no response from peer %s", peerID)
						}
						// Extract page count from response
						if witPacket, ok := res.Res.(*wit.WitnessPacketRLPPacket); ok && len(witPacket.WitnessPacketResponse) > 0 {
							return witPacket.WitnessPacketResponse[0].TotalPages, nil
						}
						return 0, fmt.Errorf("invalid response format from peer %s", peerID)
					case <-time.After(5 * time.Second):
						return 0, fmt.Errorf("timeout waiting for response from peer %s", peerID)
					}
				}

				// Trigger verification in witness manager
				h.blockFetcher.GetWitnessManager().CheckWitnessPageCount(hash, pageCount, peer, getRandomPeers, getWitnessPageCount)
			}

			// Request witnesses using the wit peer with verification
			return ethPeer.RequestWitnessesWithVerification([]common.Hash{hash}, sink, verifyPageCount)
		}
	}

	for i := 0; i < len(unknownHashes); i++ {
		h.blockFetcher.Notify(peer.ID(), unknownHashes[i], unknownNumbers[i], time.Now(), peer.RequestOneHeader, peer.RequestBodies, witnessRequester)
	}

	return nil
}

// handleBlockBroadcast is invoked from a peer's message handler when it transmits a
// block broadcast for the local node to process.
func (h *ethHandler) handleBlockBroadcast(peer *eth.Peer, block *types.Block, td *big.Int) error {
	// If stateless sync is enabled, use the dedicated injectNeedWitness channel.
	// Otherwise, use the original Enqueue optimization.
	if h.statelessSync.Load() || h.syncWithWitnesses {
		log.Debug("Received block broadcast during stateless sync", "blockNumber", block.NumberU64(), "blockHash", block.Hash())

		// Create a witness requester closure *only if* the peer supports the protocol.
		witnessRequester := func(hash common.Hash, sink chan *eth.Response) (*eth.Request, error) {
			// Get the ethPeer from the peerSet
			ethPeer := h.peers.getOnePeerWithWitness(hash)
			if ethPeer == nil {
				return nil, fmt.Errorf("no peer with witness for hash %s is available", hash)
			}

			// Create verification callback for page count verification
			verifyPageCount := func(hash common.Hash, pageCount uint64, peer string) {
				// Get random peers for verification
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

				// Get witness page count from a peer
				getWitnessPageCount := func(peerID string, hash common.Hash) (uint64, error) {
					peer := h.peers.peer(peerID)
					if peer == nil || !peer.SupportsWitness() {
						return 0, fmt.Errorf("peer %s not available or doesn't support witness", peerID)
					}

					// Create a temporary channel to get the page count
					resCh := make(chan *eth.Response, 1)
					req, err := peer.RequestWitnesses([]common.Hash{hash}, resCh)
					if err != nil {
						return 0, err
					}
					defer req.Close()

					// Wait for response with timeout
					select {
					case res := <-resCh:
						if res == nil {
							return 0, fmt.Errorf("no response from peer %s", peerID)
						}
						// Extract page count from response
						if witPacket, ok := res.Res.(*wit.WitnessPacketRLPPacket); ok && len(witPacket.WitnessPacketResponse) > 0 {
							return witPacket.WitnessPacketResponse[0].TotalPages, nil
						}
						return 0, fmt.Errorf("invalid response format from peer %s", peerID)
					case <-time.After(5 * time.Second):
						return 0, fmt.Errorf("timeout waiting for response from peer %s", peerID)
					}
				}

				// Trigger verification in witness manager
				h.blockFetcher.GetWitnessManager().CheckWitnessPageCount(hash, pageCount, peer, getRandomPeers, getWitnessPageCount)
			}

			// Request witnesses using the wit peer with verification
			return ethPeer.RequestWitnessesWithVerification([]common.Hash{hash}, sink, verifyPageCount)
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
