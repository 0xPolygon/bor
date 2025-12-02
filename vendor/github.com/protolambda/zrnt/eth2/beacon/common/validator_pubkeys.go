package common

import (
	"fmt"
	"sync"
)

// PubkeyCache is shared between any state. However, if .Append(index, pubkey) conflicts, a new cache will be forked out.
type PubkeyCache struct {
	parent *PubkeyCache
	// The count up until the conflicting validator index (cause of the pubkey cache fork).
	trustedParentCount ValidatorIndex
	pub2idx            map[BLSPubkey]ValidatorIndex
	// starting at trustedParentCount
	idx2pub []CachedPubkey
	// Can have many reads concurrently, but only 1 write.
	rwLock sync.RWMutex
}

func NewPubkeyCache(vals ValidatorRegistry) (*PubkeyCache, error) {
	valCount, err := vals.ValidatorCount()
	if err != nil {
		return nil, err
	}
	pc := &PubkeyCache{
		parent:             nil,
		trustedParentCount: 0,
		pub2idx:            make(map[BLSPubkey]ValidatorIndex),
		idx2pub:            make([]CachedPubkey, 0),
	}
	currentCount := uint64(len(pc.idx2pub))
	for i := currentCount; i < valCount; i++ {
		idx := ValidatorIndex(i)
		v, err := vals.Validator(idx)
		if err != nil {
			return nil, err
		}
		pub, err := v.Pubkey()
		if err != nil {
			return nil, err
		}
		pc.pub2idx[pub] = idx
		pc.idx2pub = append(pc.idx2pub, CachedPubkey{Compressed: pub})
	}
	return pc, nil
}

func EmptyPubkeyCache() *PubkeyCache {
	return &PubkeyCache{
		parent:             nil,
		trustedParentCount: 0,
		pub2idx:            make(map[BLSPubkey]ValidatorIndex),
		idx2pub:            make([]CachedPubkey, 0),
	}
}

// Get the pubkey of a validator index.
// Note: this does not mean the validator is part of the current state.
// It merely means that this is a known pubkey for that particular validator
// (could be in a later part of a forked version of the state).
func (pc *PubkeyCache) Pubkey(index ValidatorIndex) (pub *CachedPubkey, ok bool) {
	pc.rwLock.RLock()
	defer pc.rwLock.RUnlock()
	return pc.unsafePubkey(index)
}

func (pc *PubkeyCache) unsafePubkey(index ValidatorIndex) (pub *CachedPubkey, ok bool) {
	if index >= pc.trustedParentCount {
		if index >= pc.trustedParentCount+ValidatorIndex(len(pc.idx2pub)) {
			return nil, false
		}
		return &pc.idx2pub[index-pc.trustedParentCount], true
	} else if pc.parent != nil {
		return pc.parent.Pubkey(index)
	} else {
		return nil, false
	}
}

// Get the validator index of a pubkey.
// Note: this does not mean the validator is part of the current state.
// It merely means that this is a known pubkey for that particular validator
// (could be in a later part of a forked version of the state).
func (pc *PubkeyCache) ValidatorIndex(pubkey BLSPubkey) (index ValidatorIndex, ok bool) {
	pc.rwLock.RLock()
	defer pc.rwLock.RUnlock()
	return pc.unsafeValidatorIndex(pubkey)
}

func (pc *PubkeyCache) unsafeValidatorIndex(pubkey BLSPubkey) (index ValidatorIndex, ok bool) {
	index, ok = pc.pub2idx[pubkey]
	if !ok && pc.parent != nil {
		return pc.parent.ValidatorIndex(pubkey)
	}
	return index, ok
}

// AddValidator appends the (index, pubkey) pair to the pubkey cache. It returns the same cache if the added entry is not conflicting.
// If it conflicts, the part is inherited, and a forked pubkey cache is returned.
func (pc *PubkeyCache) AddValidator(index ValidatorIndex, pub BLSPubkey) (*PubkeyCache, error) {
	existingIndex, indexExists := pc.ValidatorIndex(pub)
	existingPubkey, pubkeyExists := pc.Pubkey(index)

	if indexExists {
		if existingIndex != index {
			// conflict detected! Deposit log fork!
			forkedPc := &PubkeyCache{
				parent: pc,
				// fork out the existing index, only trust the history
				trustedParentCount: existingIndex,
				pub2idx:            make(map[BLSPubkey]ValidatorIndex),
				idx2pub:            make([]CachedPubkey, 0),
			}
			// Do not have to unlock this cache (parent of forkedPc) early, as the forkedPc is guaranteed to handle it.
			return forkedPc.AddValidator(index, pub)
		}
		if pubkeyExists {
			if existingPubkey.Compressed != pub {
				// conflict detected! Deposit log fork!
				forkedPc := &PubkeyCache{
					parent: pc,
					// fork out the existing index, only trust the history
					trustedParentCount: index,
					pub2idx:            make(map[BLSPubkey]ValidatorIndex),
					idx2pub:            make([]CachedPubkey, 0),
				}
				// Do not have to unlock this cache (parent of forkedPc) early, as the forkedPc is guaranteed to handle it.
				return forkedPc.AddValidator(index, pub)
			}
		}
		// append is no-op, validator already exists
		return pc, nil
	}
	if pubkeyExists {
		if existingPubkey.Compressed != pub {
			// conflict detected! Deposit log fork!
			forkedPc := &PubkeyCache{
				parent: pc,
				// fork out the existing index, only trust the history
				trustedParentCount: index,
				pub2idx:            make(map[BLSPubkey]ValidatorIndex),
				idx2pub:            make([]CachedPubkey, 0),
			}
			// Do not have to unlock this cache (parent of forkedPc) early, as the forkedPc is guaranteed to handle it.
			return forkedPc.AddValidator(index, pub)
		}
	}
	pc.rwLock.Lock()
	defer pc.rwLock.Unlock()
	if expected := pc.trustedParentCount + ValidatorIndex(len(pc.idx2pub)); index != expected {
		// index is unknown, but too far ahead of cache; in between indices are missing.
		return nil, fmt.Errorf("AddValidator is incorrect, missing earlier index. got: (%d, %x), but currently expecting %d next", index, pub, expected)
	}
	pc.idx2pub = append(pc.idx2pub, CachedPubkey{Compressed: pub})
	pc.pub2idx[pub] = index
	return pc, nil
}
