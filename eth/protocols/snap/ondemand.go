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

package snap

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
)

// onDemandReqidBit marks a request id as belonging to an out-of-band, targeted
// bytecode fetch (FetchByteCodes) rather than the bulk sync loop. Loop reqids
// are uint64(rand.Int63()) and so always have the top bit clear; setting it
// guarantees the two id spaces never collide, so OnByteCodes can route a
// response to the right place unambiguously.
const onDemandReqidBit = uint64(1) << 63

// onDemandCodeFetchTimeout bounds how long a single peer is given to answer a
// targeted bytecode request before the next peer is tried. A variable rather
// than a constant so tests can shorten it.
var onDemandCodeFetchTimeout = 15 * time.Second

// onDemandCodeReq tracks one in-flight targeted bytecode request.
type onDemandCodeReq struct {
	deliver chan [][]byte
}

// FetchByteCodes retrieves the given contract bytecodes from a connected snap
// peer on demand — outside the bulk sync state machine — verifying each blob
// against its hash before returning it. It backs the stateless self-heal path:
// when witness-verified execution references a contract whose code is absent
// from local disk (WIT2 witnesses carry no code), the node fetches exactly that
// content-addressed blob and re-persists it, instead of stalling.
//
// It returns the subset of hashes it could fetch and verify, keyed by hash; a
// missing entry means no connected peer served it. Each eligible peer is tried
// at most once, until every hash is found, the peer set is exhausted, or ctx is
// cancelled. An error is returned only when nothing at all could be verified —
// a partial result is still worth persisting, so it is returned without one.
func (s *Syncer) FetchByteCodes(ctx context.Context, hashes []common.Hash) (map[common.Hash][]byte, error) {
	out := make(map[common.Hash][]byte, len(hashes))
	for _, peer := range s.codePeers() {
		pending := pendingByteCodes(hashes, out)
		if len(pending) == 0 {
			return out, nil
		}
		if err := s.fetchByteCodesFromPeer(ctx, peer, pending, out); err != nil {
			return out, partialOrErr(out, err)
		}
	}
	if pending := pendingByteCodes(hashes, out); len(pending) > 0 {
		return out, partialOrErr(out, fmt.Errorf("snap: no peer available to serve %d bytecode(s) on demand", len(pending)))
	}
	return out, nil
}

// partialOrErr returns nil when out already holds at least one verified blob —
// the caller learns of the unserved remainder from the missing keys — and err
// otherwise.
func partialOrErr(out map[common.Hash][]byte, err error) error {
	if len(out) > 0 {
		return nil
	}
	return err
}

// pendingByteCodes returns the hashes not yet present in out, capped at a single
// request's worth (maxCodeRequestCount).
func pendingByteCodes(hashes []common.Hash, out map[common.Hash][]byte) []common.Hash {
	var pending []common.Hash
	for _, h := range hashes {
		if _, ok := out[h]; !ok {
			pending = append(pending, h)
		}
	}
	return pending[:min(len(pending), maxCodeRequestCount)]
}

// codePeers snapshots the connected peers eligible to serve a targeted bytecode
// request: every registered peer not flagged as stateless. Peers are registered
// by lifecycle (Register/Unregister) independently of a running Sync cycle, so
// this is populated whenever snap peers are connected, even with the syncer
// otherwise idle.
func (s *Syncer) codePeers() []SyncPeer {
	s.lock.RLock()
	defer s.lock.RUnlock()
	peers := make([]SyncPeer, 0, len(s.peers))
	for id, p := range s.peers {
		if _, stateless := s.statelessPeers[id]; stateless {
			continue
		}
		peers = append(peers, p)
	}
	return peers
}

// fetchByteCodesFromPeer issues one targeted bytecode request to peer and folds
// any verified blobs into out. A non-nil error means ctx was cancelled and the
// caller should stop; a failed send or a timeout returns nil so the caller can
// try the next peer. The request is forgotten on every return path, so a late
// or duplicate response is dropped harmlessly by onDemandByteCodes.
func (s *Syncer) fetchByteCodesFromPeer(ctx context.Context, peer SyncPeer, pending []common.Hash, out map[common.Hash][]byte) error {
	reqid, req := s.newOnDemandCodeReq()
	defer s.forgetOnDemandCodeReq(reqid)

	if err := peer.RequestByteCodes(reqid, pending, maxRequestSize); err != nil {
		log.Debug("On-demand bytecode request failed to send", "peer", peer.ID(), "err", err)
		return nil
	}
	timeout := time.NewTimer(onDemandCodeFetchTimeout)
	defer timeout.Stop()

	select {
	case codes := <-req.deliver:
		verifyAndCollectByteCodes(pending, codes, out)
		return nil
	case <-timeout.C:
		log.Debug("On-demand bytecode request timed out", "peer", peer.ID(), "hashes", len(pending))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// newOnDemandCodeReq registers a fresh in-flight targeted request under a
// top-bit reqid and returns both.
func (s *Syncer) newOnDemandCodeReq() (uint64, *onDemandCodeReq) {
	s.lock.Lock()
	defer s.lock.Unlock()
	reqid := uint64(rand.Int63()) | onDemandReqidBit
	req := &onDemandCodeReq{deliver: make(chan [][]byte, 1)}
	s.onDemandCodeReqs[reqid] = req
	return reqid, req
}

// forgetOnDemandCodeReq drops an in-flight targeted request; forgetting an
// already-delivered (and hence already-removed) request is a no-op.
func (s *Syncer) forgetOnDemandCodeReq(reqid uint64) {
	s.lock.Lock()
	delete(s.onDemandCodeReqs, reqid)
	s.lock.Unlock()
}

// onDemandByteCodes delivers a targeted bytecode response (top-bit reqid) to its
// waiting FetchByteCodes caller. Stale, duplicate or unrequested responses are
// dropped harmlessly.
func (s *Syncer) onDemandByteCodes(id uint64, bytecodes [][]byte) error {
	s.lock.Lock()
	req, ok := s.onDemandCodeReqs[id]
	if ok {
		delete(s.onDemandCodeReqs, id)
	}
	s.lock.Unlock()
	if !ok {
		return nil
	}
	select {
	case req.deliver <- bytecodes:
	default:
	}
	return nil
}

// verifyAndCollectByteCodes hashes each delivered blob and, if that hash is one
// of wanted, records the blob into out under it. Because the key is the verified
// keccak of the blob itself, a peer cannot inject code for a hash it was not
// asked for; a blob whose hash matches nothing wanted (corrupt or unrequested)
// is simply ignored — that peer does not count as having served the hash, and
// the caller moves on to the next one.
func verifyAndCollectByteCodes(wanted []common.Hash, delivered [][]byte, out map[common.Hash][]byte) {
	want := make(map[common.Hash]struct{}, len(wanted))
	for _, h := range wanted {
		want[h] = struct{}{}
	}
	hasher := crypto.NewKeccakState()
	var h common.Hash
	for _, blob := range delivered {
		hasher.Reset()
		hasher.Write(blob)
		hasher.Read(h[:])
		if _, ok := want[h]; ok {
			out[h] = blob
		}
	}
}
