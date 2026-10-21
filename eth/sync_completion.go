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

package eth

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const (
	syncAnnouncementBatch = 256
	syncAnnouncementRetry = 100 * time.Millisecond
)

// One worker owns a shared, pool-bounded snapshot and one cursor per peer.
// Queue rejection does not consume the cursor, even after rebroadcast expiry.
// The worker exits on completion, peer departure, or handler shutdown.
func (h *handler) recoverPendingAnnouncements(peers []*ethPeer, hashes []common.Hash) {
	defer h.wg.Done()
	pending := make(map[*ethPeer]int, len(peers))
	for _, peer := range peers {
		pending[peer] = 0
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for len(pending) > 0 {
		select {
		case <-h.quitSync:
			return
		case <-timer.C:
		}
		h.retryPendingAnnouncements(pending, hashes)
		timer.Reset(syncAnnouncementRetry)
	}
}

func (h *handler) retryPendingAnnouncements(pending map[*ethPeer]int, hashes []common.Hash) {
	for peer, offset := range pending {
		if h.peers.peer(peer.ID()) != peer {
			delete(pending, peer)
			continue
		}
		offset += h.queuePendingAnnouncements(peer, hashes[offset:])
		if offset == len(hashes) {
			delete(pending, peer)
		} else {
			pending[peer] = offset
		}
	}
}

func (h *handler) queuePendingAnnouncements(peer *ethPeer, hashes []common.Hash) int {
	hashes = hashes[:min(len(hashes), syncAnnouncementBatch)]
	batch := make([]common.Hash, 0, len(hashes))
	offsets := make([]int, 0, len(hashes))
	for i, hash := range hashes {
		tx := h.txpool.Get(hash)
		if tx == nil || tx.GetOptions() != nil || (h.privateTxGetter != nil && h.privateTxGetter.IsTxPrivate(hash)) {
			continue
		}
		batch = append(batch, hash)
		offsets = append(offsets, i)
	}
	if len(batch) == 0 {
		return len(hashes)
	}
	// The propagation queue retains a prefix; retry from its first rejected hash.
	accepted := peer.QueuePooledTransactionHashes(batch)
	if len(accepted) < len(batch) {
		return offsets[len(accepted)]
	}
	return len(hashes)
}
