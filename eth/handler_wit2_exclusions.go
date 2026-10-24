package eth

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// witnessSourceExclusionTTL bounds how long an excluded (block, peer) pair is
// remembered. Exclusions are also dropped the moment the block imports
// (onBlockImported); the TTL is a backstop for blocks that never do — the
// block fetcher gives a block up after maxWitnessImportRetries — and is
// generous relative to the seconds a block normally takes to resolve.
const witnessSourceExclusionTTL = 2 * time.Minute

// witnessSourceExclusionSet remembers, per block, the peers whose served
// witness for that block was accepted on the WIT2 size oracle alone and then
// failed import. Such a peer is skipped when the fetcher resolves a witness
// source for the block (resolveWitnessFetchPeer), so the re-fetch reaches a
// different peer instead of pulling the same unusable bytes again. Bounded by
// the TTL sweep on every add and by drop() on block import.
type witnessSourceExclusionSet struct {
	mu      sync.Mutex
	entries map[common.Hash]map[string]time.Time
}

func newWitnessSourceExclusionSet() *witnessSourceExclusionSet {
	return &witnessSourceExclusionSet{entries: make(map[common.Hash]map[string]time.Time)}
}

// add excludes peer as a witness source for blockHash.
func (s *witnessSourceExclusionSet) add(blockHash common.Hash, peer string) {
	if peer == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	peers := s.entries[blockHash]
	if peers == nil {
		peers = make(map[string]time.Time)
		s.entries[blockHash] = peers
	}
	peers[peer] = time.Now()
}

// excluded reports whether peer is currently excluded as a witness source for
// blockHash.
func (s *witnessSourceExclusionSet) excluded(blockHash common.Hash, peer string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.entries[blockHash][peer]
	if !ok {
		return false
	}
	return time.Since(at) <= witnessSourceExclusionTTL
}

// drop forgets every exclusion recorded for blockHash.
func (s *witnessSourceExclusionSet) drop(blockHash common.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, blockHash)
}

// gcLocked removes exclusions past the TTL. Caller must hold the lock.
func (s *witnessSourceExclusionSet) gcLocked() {
	cutoff := time.Now().Add(-witnessSourceExclusionTTL)
	for hash, peers := range s.entries {
		for peer, at := range peers {
			if at.Before(cutoff) {
				delete(peers, peer)
			}
		}
		if len(peers) == 0 {
			delete(s.entries, hash)
		}
	}
}
