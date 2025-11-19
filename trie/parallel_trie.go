package trie

import (
	"bytes"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"golang.org/x/sync/errgroup"
)

// KVPair represents one hashed-key/value to insert into the trie.
// - Key: 32-byte keccak hash (account key for account trie).
// - Value: RLP-encoded value bytes (account RLP for account trie).
type KVPair struct {
	Key   []byte
	Value []byte
}

// BuildAccountTrieParallel constructs an account MPT using a Parallel Sparse Trie approach.
// It returns the computed root hash and a NodeSet containing all non-embedded nodes.
// Wrapper that uses zero owner for the account trie.
func BuildAccountTrieParallel(kvs []KVPair, workers int) (common.Hash, *trienode.NodeSet, error) {
	return BuildTrieParallelWithOwner(common.Hash{}, kvs, workers)
}

// ParallelTrie is a builder for an account/storage MPT that supports incremental
// inserts and deletes into a sharded, parallelizable structure. Keys are
// expected to be keccak(address) (32 bytes); values are RLP-encoded blobs.
//
// This builder does not mutate a live pointer-based trie. It accumulates
// per-shard final values and (re)builds the trie deterministically on Build.
type ParallelTrie struct {
	shards  [16]map[string][]byte
	locks   [16]sync.Mutex
	deleted [16]map[string]struct{}
	// committed guards further writes after Commit to mirror trie.Trie semantics.
	committed bool
}

// NewParallelTrie constructs an empty ParallelTrie for the account/storage trie.
func NewParallelTrie() *ParallelTrie {
	var pt ParallelTrie
	for i := range pt.shards {
		pt.shards[i] = make(map[string][]byte)
		pt.deleted[i] = make(map[string]struct{})
	}
	return &pt
}

// Insert inserts or updates the given hashed key to the provided RLP-encoded value.
// The key must be a 32-byte keccak hash (account/storage key).
func (p *ParallelTrie) Insert(key []byte, value []byte) {
	if p.committed {
		return
	}
	if len(key) == 0 {
		return
	}
	nib := int(key[0] >> 4)
	p.locks[nib].Lock()
	valCopy := append([]byte(nil), value...)
	p.shards[nib][string(key)] = valCopy
	delete(p.deleted[nib], string(key))
	p.locks[nib].Unlock()
}

// Delete marks the given hashed key as deleted. The key must be a 32-byte keccak hash.
func (p *ParallelTrie) Delete(key []byte) {
	if p.committed {
		return
	}
	if len(key) == 0 {
		return
	}
	nib := int(key[0] >> 4)
	p.locks[nib].Lock()
	delete(p.shards[nib], string(key))
	p.deleted[nib][string(key)] = struct{}{}
	p.locks[nib].Unlock()
}

// Build constructs the trie in parallel and returns the root and nodes.
// workers controls shard build parallelism; if <=0, a default of 16 is used.
func (p *ParallelTrie) Build(workers int) (common.Hash, *trienode.NodeSet, error) {
	var allKVs []KVPair
	for nib := 0; nib < 16; nib++ {
		p.locks[nib].Lock()
		for k, v := range p.shards[nib] {
			allKVs = append(allKVs, KVPair{Key: []byte(k), Value: v})
		}
		p.locks[nib].Unlock()
	}
	return BuildAccountTrieParallel(allKVs, workers)
}

// Update associates key with value. If value has length zero, it's treated as deletion.
func (p *ParallelTrie) Update(key, value []byte) error {
	if p.committed {
		return ErrCommitted
	}
	if len(value) == 0 {
		p.Delete(key)
		return nil
	}
	p.Insert(key, value)
	return nil
}

// Delete removes any existing value for key.
func (p *ParallelTrie) DeleteKey(key []byte) error {
	if p.committed {
		return ErrCommitted
	}
	p.Delete(key)
	return nil
}

// Get returns the value for key stored in the overlay, nil if not present.
// Note: This does not read from any backing database, only the overlay state.
func (p *ParallelTrie) Get(key []byte) ([]byte, error) {
	if p.committed {
		return nil, ErrCommitted
	}
	if len(key) == 0 {
		return nil, nil
	}
	nib := int(key[0] >> 4)
	p.locks[nib].Lock()
	defer p.locks[nib].Unlock()
	if v, ok := p.shards[nib][string(key)]; ok {
		return v, nil
	}
	// If explicitly deleted, also return nil
	if _, deleted := p.deleted[nib][string(key)]; deleted {
		return nil, nil
	}
	return nil, nil
}

// Value returns the overlay value for the given key and a boolean for presence.
func (p *ParallelTrie) Value(key []byte) ([]byte, bool) {
	if len(key) == 0 {
		return nil, false
	}
	nib := int(key[0] >> 4)
	p.locks[nib].Lock()
	defer p.locks[nib].Unlock()
	v, ok := p.shards[nib][string(key)]
	return v, ok
}

// IsDeleted returns whether the key is explicitly marked as deleted in overlay.
func (p *ParallelTrie) IsDeleted(key []byte) bool {
	if len(key) == 0 {
		return false
	}
	nib := int(key[0] >> 4)
	p.locks[nib].Lock()
	defer p.locks[nib].Unlock()
	_, ok := p.deleted[nib][string(key)]
	return ok
}

// Hash computes the root by building the trie in parallel.
func (p *ParallelTrie) Hash() common.Hash {
	root, _, _ := p.Build(0)
	return root
}

// Commit builds the trie in parallel and returns the root and nodeset.
// After commit, the overlay is marked committed and further writes are disallowed.
func (p *ParallelTrie) Commit(_ bool) (common.Hash, *trienode.NodeSet) {
	root, set, _ := p.Build(0)
	p.committed = true
	return root, set
}

// Pairs returns a snapshot of all (key,value) present in overlay.
func (p *ParallelTrie) Pairs() []KVPair {
	var out []KVPair
	for nib := 0; nib < 16; nib++ {
		p.locks[nib].Lock()
		for k, v := range p.shards[nib] {
			out = append(out, KVPair{Key: []byte(k), Value: append([]byte(nil), v...)})
		}
		p.locks[nib].Unlock()
	}
	return out
}

// Keys returns a snapshot of all currently-present keys (post-deletes).
func (p *ParallelTrie) Keys() [][]byte {
	var out [][]byte
	for nib := 0; nib < 16; nib++ {
		p.locks[nib].Lock()
		for k := range p.shards[nib] {
			out = append(out, []byte(k))
		}
		p.locks[nib].Unlock()
	}
	return out
}

// DeletedKeys returns a snapshot of keys explicitly deleted in this session.
func (p *ParallelTrie) DeletedKeys() [][]byte {
	var out [][]byte
	for nib := 0; nib < 16; nib++ {
		p.locks[nib].Lock()
		for k := range p.deleted[nib] {
			out = append(out, []byte(k))
		}
		p.locks[nib].Unlock()
	}
	return out
}

// CollectKVsFromTrie walks a pointer-based trie and collects all leaf key/value
// pairs as hashed-key and raw value blobs suitable for PST building.
func CollectKVsFromTrie(tr *Trie) ([]KVPair, error) {
	it, err := tr.NodeIterator(nil)
	if err != nil {
		return nil, err
	}
	var kvs []KVPair
	for it.Next(true) {
		if !it.Leaf() {
			continue
		}
		k := append([]byte(nil), it.LeafKey()...)
		v := append([]byte(nil), it.LeafBlob()...)
		kvs = append(kvs, KVPair{Key: k, Value: v})
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	return kvs, nil
}

// RebuildTrieParallelFromTrie rebuilds the given pointer-based trie using the
// parallel sparse trie builder and returns the root and full nodeset.
func RebuildTrieParallelFromTrie(tr *Trie, workers int) (common.Hash, *trienode.NodeSet, error) {
	kvs, err := CollectKVsFromTrie(tr)
	if err != nil {
		return common.Hash{}, nil, err
	}
	return BuildAccountTrieParallel(kvs, workers)
}

// BuildTrieParallelWithOwner constructs a trie using a Parallel Sparse Trie approach,
// assigning the provided owner to the returned NodeSet (zero for account trie,
// account address hash for storage tries).
func BuildTrieParallelWithOwner(owner common.Hash, kvs []KVPair, workers int) (common.Hash, *trienode.NodeSet, error) {
	var shards [16][]KVPair
	for _, kv := range kvs {
		if len(kv.Key) == 0 {
			continue
		}
		idx := int(kv.Key[0] >> 4)
		shards[idx] = append(shards[idx], kv)
	}
	for i := range shards {
		sort.Slice(shards[i], func(a, b int) bool { return bytes.Compare(shards[i][a].Key, shards[i][b].Key) < 0 })
	}
	type shardResult struct {
		st    *StackTrie
		nodes *trienode.NodeSet
	}
	results := make([]shardResult, 16)

	var eg errgroup.Group
	if workers <= 0 {
		workers = 16
	}
	eg.SetLimit(workers)
	for nib := 0; nib < 16; nib++ {
		nib := nib
		eg.Go(func() error {
			shard := shards[nib]
			set := trienode.NewNodeSet(owner)
			cb := func(path []byte, hash common.Hash, blob []byte) {
				p := append([]byte(nil), path...)
				b := append([]byte(nil), blob...)
				set.AddNode(p, trienode.New(hash, b))
			}
			st := NewStackTrie(cb)
			if len(shard) == 0 {
				results[nib] = shardResult{st: nil, nodes: set}
				return nil
			}
			var last []byte
			for i := range shard {
				nibbles := keybytesToHex(shard[i].Key)
				nibbles = nibbles[:len(nibbles)-1]
				if len(nibbles) == 0 {
					continue
				}
				nibbles = nibbles[1:]
				if last != nil && bytes.Compare(last, nibbles) >= 0 {
					if bytes.Equal(last, nibbles) {
						continue
					}
				}
				if err := st.UpdateHex(nibbles, shard[i].Value); err != nil {
					return err
				}
				last = append(last[:0], nibbles...)
			}
			results[nib] = shardResult{st: st, nodes: set}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return common.Hash{}, nil, err
	}
	all := trienode.NewNodeSet(owner)
	nonEmpty := 0
	for i := 0; i < 16; i++ {
		if results[i].st != nil {
			nonEmpty++
		}
	}
	h := newHasher(false)
	switch {
	case nonEmpty == 0:
		return types.EmptyRootHash, all, nil
	case nonEmpty == 1:
		var nib int
		for i := 0; i < 16; i++ {
			if results[i].st != nil {
				nib = i
				break
			}
		}
		st := results[nib].st
		switch st.root.typ {
		case branchNode:
			st.hash(st.root, []byte{byte(nib)})
			val := st.root.val
			enc := extNodeEncoder{
				Key: hexToCompactInPlace([]byte{byte(nib)}),
				Val: val,
			}
			enc.encode(h.encbuf)
			rootBlob := h.encodedBytes()
			rootHash := h.hashData(rootBlob)
			_ = all.MergeSet(results[nib].nodes)
			all.AddNode(nil, trienode.New(common.BytesToHash(rootHash), append([]byte(nil), rootBlob...)))
			return common.BytesToHash(rootHash), all, nil
		case extNode:
			prefix := append([]byte{byte(nib)}, st.root.key...)
			st.hash(st.root.children[0], prefix)
			enc := extNodeEncoder{
				Key: hexToCompactInPlace(append([]byte{}, prefix...)),
				Val: st.root.children[0].val,
			}
			enc.encode(h.encbuf)
			rootBlob := h.encodedBytes()
			rootHash := h.hashData(rootBlob)
			_ = all.MergeSet(results[nib].nodes)
			all.AddNode(nil, trienode.New(common.BytesToHash(rootHash), append([]byte(nil), rootBlob...)))
			return common.BytesToHash(rootHash), all, nil
		case leafNode:
			key := append([]byte{byte(nib)}, st.root.key...)
			key = append(key, byte(16))
			enc := leafNodeEncoder{
				Key: hexToCompactInPlace(append([]byte{}, key...)),
				Val: st.root.val,
			}
			enc.encode(h.encbuf)
			rootBlob := h.encodedBytes()
			rootHash := h.hashData(rootBlob)
			all.AddNode(nil, trienode.New(common.BytesToHash(rootHash), append([]byte(nil), rootBlob...)))
			return common.BytesToHash(rootHash), all, nil
		default:
			return common.Hash{}, all, nil
		}
	default:
		var children [17][]byte
		for i := 0; i < 16; i++ {
			if results[i].st == nil {
				continue
			}
			st := results[i].st
			st.hash(st.root, []byte{byte(i)})
			children[i] = append([]byte(nil), st.root.val...)
			_ = all.MergeSet(results[i].nodes)
		}
		enc := fullnodeEncoder{Children: children}
		enc.encode(h.encbuf)
		rootBlob := h.encodedBytes()
		rootHash := h.hashData(rootBlob)
		all.AddNode(nil, trienode.New(common.BytesToHash(rootHash), append([]byte(nil), rootBlob...)))
		return common.BytesToHash(rootHash), all, nil
	}
}
