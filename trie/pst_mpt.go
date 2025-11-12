package trie

import (
	"bytes"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
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
func BuildAccountTrieParallel(kvs []KVPair, workers int) (common.Hash, *trienode.NodeSet, error) {
	// Partition by top hex-nibble of the 32-byte hashed key.
	var shards [16][]KVPair
	for _, kv := range kvs {
		if len(kv.Key) == 0 {
			continue
		}
		// Top nibble is the high 4 bits of the first byte.
		idx := int(kv.Key[0] >> 4)
		shards[idx] = append(shards[idx], kv)
	}
	// Sort each shard by key ascending to satisfy StackTrie insertion order.
	for i := range shards {
		sort.Slice(shards[i], func(a, b int) bool { return bytes.Compare(shards[i][a].Key, shards[i][b].Key) < 0 })
	}
	type shardResult struct {
		st    *StackTrie        // built shard trie (keys without top nibble)
		nodes *trienode.NodeSet // nodes collected via hashing callbacks
	}
	results := make([]shardResult, 16)

	// Build shards in parallel.
	var errgroup errgroup.Group
	if workers <= 0 {
		workers = 16
	}
	errgroup.SetLimit(workers)
	for nib := 0; nib < 16; nib++ {
		nib := nib
		errgroup.Go(func() error {
			shard := shards[nib]
			// NodeSet owner is zero for account trie.
			set := trienode.NewNodeSet(common.Hash{})
			// onTrieNode callback: deep-copy path and blob (volatile) before storing.
			cb := func(path []byte, hash common.Hash, blob []byte) {
				p := append([]byte(nil), path...)
				b := append([]byte(nil), blob...)
				set.AddNode(p, trienode.New(hash, b))
			}
			st := NewStackTrie(cb)
			if len(shard) == 0 {
				// No keys in this shard: no child at this nibble.
				results[nib] = shardResult{st: nil, nodes: set}
				return nil
			}
			// Insert strictly ascending keys using hex-nibbles without the top nibble.
			var last []byte
			for i := range shard {
				// Convert to hex-nibbles and strip terminator and top nibble.
				nibbles := keybytesToHex(shard[i].Key)
				nibbles = nibbles[:len(nibbles)-1] // drop terminator
				if len(nibbles) == 0 {
					continue
				}
				nibbles = nibbles[1:] // drop top nibble for this shard
				// Enforce strictly ascending order on the nibble keys.
				if last != nil && bytes.Compare(last, nibbles) >= 0 {
					// Should not happen due to our sorting; defensive.
					// Skip duplicates silently.
					if bytes.Equal(last, nibbles) {
						continue
					}
				}
				if err := st.UpdateHex(nibbles, shard[i].Value); err != nil {
					return err
				}
				last = append(last[:0], nibbles...)
			}
			// Defer hashing to merge stage to allow canonical root assembly.
			results[nib] = shardResult{st: st, nodes: set}
			return nil
		})
	}
	if err := errgroup.Wait(); err != nil {
		return common.Hash{}, nil, err
	}
	// Count non-empty shards and prepare the final NodeSet.
	all := trienode.NewNodeSet(common.Hash{})
	nonEmpty := 0
	for i := 0; i < 16; i++ {
		if results[i].st != nil {
			nonEmpty++
		}
	}
	h := newHasher(false)
	switch {
	case nonEmpty == 0:
		// Empty trie
		return common.Hash{}, all, nil
	case nonEmpty == 1:
		// Single child: canonicalize to ext/leaf root.
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
			// Parent ext over branch child.
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
			// Coalesce nibble into ext key; parent ext points to child of ext.
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
			// Coalesce nibble into leaf key.
			key := append([]byte{byte(nib)}, st.root.key...)
			key = append(key, byte(16)) // terminator
			enc := leafNodeEncoder{
				Key: hexToCompactInPlace(append([]byte{}, key...)),
				Val: st.root.val,
			}
			enc.encode(h.encbuf)
			rootBlob := h.encodedBytes()
			rootHash := h.hashData(rootBlob)
			// No deeper nodes; just add root.
			all.AddNode(nil, trienode.New(common.BytesToHash(rootHash), append([]byte(nil), rootBlob...)))
			return common.BytesToHash(rootHash), all, nil
		default:
			return common.Hash{}, all, nil
		}
	default:
		// 2+ children: full node root; hash each shard root with nibble prefix.
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

// ParallelSparseTrie is a builder for an account MPT that supports incremental
// inserts and deletes into a sharded, parallelizable structure. Keys are
// expected to be keccak(address) (32 bytes); values are RLP-encoded accounts.
//
// This builder does not mutate a live pointer-based trie. It accumulates
// per-shard final values and (re)builds the trie deterministically on Build.
type ParallelSparseTrie struct {
	shards [16]map[string][]byte
	locks  [16]sync.Mutex
}

// NewParallelSparseTrie constructs an empty PST for the account trie.
func NewParallelSparseTrie() *ParallelSparseTrie {
	var pst ParallelSparseTrie
	for i := range pst.shards {
		pst.shards[i] = make(map[string][]byte)
	}
	return &pst
}

// Insert inserts or updates the given hashed key to the provided RLP-encoded value.
// The key must be a 32-byte keccak hash (account key).
func (p *ParallelSparseTrie) Insert(key []byte, value []byte) {
	if len(key) == 0 {
		return
	}
	nib := int(key[0] >> 4)
	p.locks[nib].Lock()
	// Store a copy to avoid external mutation.
	valCopy := append([]byte(nil), value...)
	p.shards[nib][string(key)] = valCopy
	p.locks[nib].Unlock()
}

// Delete marks the given hashed key as deleted. The key must be a 32-byte keccak hash.
func (p *ParallelSparseTrie) Delete(key []byte) {
	if len(key) == 0 {
		return
	}
	nib := int(key[0] >> 4)
	p.locks[nib].Lock()
	// Represent deletion by absence in final map: remove any existing value.
	delete(p.shards[nib], string(key))
	p.locks[nib].Unlock()
}

// Build constructs the account trie in parallel and returns the root and nodes.
// workers controls shard build parallelism; if <=0, a default of 16 is used.
func (p *ParallelSparseTrie) Build(workers int) (common.Hash, *trienode.NodeSet, error) {
	// Snapshot shards into KV slices to avoid holding locks during build.
	var allKVs []KVPair
	for nib := 0; nib < 16; nib++ {
		p.locks[nib].Lock()
		for k, v := range p.shards[nib] {
			// Only inserts present; deletions are represented by absence.
			allKVs = append(allKVs, KVPair{Key: []byte(k), Value: v})
		}
		p.locks[nib].Unlock()
	}
	return BuildAccountTrieParallel(allKVs, workers)
}
