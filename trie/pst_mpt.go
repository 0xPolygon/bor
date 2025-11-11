package trie

import (
	"bytes"
	"cmp"
	"sort"

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
		childValue []byte            // representation for parent (raw <32B or 32B hash)
		nodes      *trienode.NodeSet // collected non-embedded nodes for this shard
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
				results[nib] = shardResult{childValue: nil, nodes: set}
				return nil
			}
			// Insert strictly ascending keys.
			var last []byte
			for i := range shard {
				// StackTrie enforces strictly ascending order; ensure and insert.
				if last != nil && cmp.Compare(bytes.Compare(last, shard[i].Key), 0) >= 0 {
					// Should not happen due to our sorting; defensive.
					// Skip duplicates silently.
					if bytes.Equal(last, shard[i].Key) {
						continue
					}
				}
				if err := st.Update(shard[i].Key, shard[i].Value); err != nil {
					return err
				}
				last = shard[i].Key
			}
			// Hash the shard root with a non-empty path prefix so that the
			// shard root yields the correct representation for a parent child:
			// - <32 bytes -> embedded raw
			// - >=32 bytes -> 32-byte hash
			st.hash(st.root, []byte{byte(nib)})
			// childValue is the representation used inside the parent full node.
			childVal := append([]byte(nil), st.root.val...)
			results[nib] = shardResult{childValue: childVal, nodes: set}
			return nil
		})
	}
	if err := errgroup.Wait(); err != nil {
		return common.Hash{}, nil, err
	}
	// Assemble the parent full node from shard child values and compute root hash.
	var children [17][]byte
	for i := 0; i < 16; i++ {
		children[i] = results[i].childValue
	}
	enc := fullnodeEncoder{Children: children}
	h := newHasher(false)
	enc.encode(h.encbuf)
	rootBlob := h.encodedBytes()
	rootHash := h.hashData(rootBlob)

	// Merge shard nodes into one NodeSet and add the root node.
	all := trienode.NewNodeSet(common.Hash{})
	for i := 0; i < 16; i++ {
		_ = all.MergeSet(results[i].nodes)
	}
	all.AddNode(nil, trienode.New(common.BytesToHash(rootHash), append([]byte(nil), rootBlob...)))
	return common.BytesToHash(rootHash), all, nil
}
