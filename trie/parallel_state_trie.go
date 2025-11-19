package trie

import (
	"runtime"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb/database"
)

// ParallelStateTrie implements the state.Trie interface (in core/state) using a
// ParallelTrie overlay for writes and a pointer-based trie for reads.
type ParallelStateTrie struct {
	base    *Trie       // read path, iterator and proof
	owner   common.Hash // owner (zero for account, address hash for storage)
	overlay *ParallelTrie
}

// NewParallelStateTrie creates a parallel state trie with an existing root.
func NewParallelStateTrie(id *ID, db database.NodeDatabase) (*ParallelStateTrie, error) {
	base, err := New(id, db)
	if err != nil {
		return nil, err
	}
	return &ParallelStateTrie{
		base:    base,
		owner:   id.Owner,
		overlay: NewParallelTrie(),
	}, nil
}

// GetKey returns the preimage for a hashed key. Not supported; returns nil.
func (t *ParallelStateTrie) GetKey(_ []byte) []byte { return nil }

// GetAccount returns the decoded account at address.
func (t *ParallelStateTrie) GetAccount(address common.Address) (*types.StateAccount, error) {
	hk := crypto.Keccak256(address.Bytes())
	if val, ok := t.overlay.Value(hk); ok {
		if len(val) == 0 {
			return nil, nil
		}
		ret := new(types.StateAccount)
		if err := rlp.DecodeBytes(val, ret); err != nil {
			return nil, err
		}
		return ret, nil
	}
	if t.overlay.IsDeleted(hk) {
		return nil, nil
	}
	res, err := t.base.Get(hk)
	if res == nil || err != nil {
		return nil, err
	}
	ret := new(types.StateAccount)
	if err := rlp.DecodeBytes(res, ret); err != nil {
		return nil, err
	}
	return ret, nil
}

// GetStorage returns the storage value for key. The returned bytes are raw content.
func (t *ParallelStateTrie) GetStorage(_ common.Address, key []byte) ([]byte, error) {
	hk := crypto.Keccak256(key)
	if val, ok := t.overlay.Value(hk); ok {
		if len(val) == 0 {
			return nil, nil
		}
		_, content, _, err := rlp.Split(val)
		return content, err
	}
	if t.overlay.IsDeleted(hk) {
		return nil, nil
	}
	enc, err := t.base.Get(hk)
	if err != nil || len(enc) == 0 {
		return nil, err
	}
	_, content, _, err := rlp.Split(enc)
	return content, err
}

// UpdateAccount writes the encoded account to the overlay.
func (t *ParallelStateTrie) UpdateAccount(address common.Address, acc *types.StateAccount, _ int) error {
	hk := crypto.Keccak256(address.Bytes())
	data, err := rlp.EncodeToBytes(acc)
	if err != nil {
		return err
	}
	return t.overlay.Update(hk, data)
}

// UpdateStorage writes the encoded storage to the overlay.
func (t *ParallelStateTrie) UpdateStorage(_ common.Address, key, value []byte) error {
	hk := crypto.Keccak256(key)
	enc, _ := rlp.EncodeToBytes(value)
	return t.overlay.Update(hk, enc)
}

// DeleteAccount deletes an account in the overlay.
func (t *ParallelStateTrie) DeleteAccount(address common.Address) error {
	hk := crypto.Keccak256(address.Bytes())
	return t.overlay.DeleteKey(hk)
}

// DeleteStorage deletes a storage slot in the overlay.
func (t *ParallelStateTrie) DeleteStorage(_ common.Address, key []byte) error {
	hk := crypto.Keccak256(key)
	return t.overlay.DeleteKey(hk)
}

// UpdateContractCode is a no-op for MPT.
func (t *ParallelStateTrie) UpdateContractCode(_ common.Address, _ common.Hash, _ []byte) error {
	return nil
}

// Hash computes the root from the merged (base + overlay) view.
func (t *ParallelStateTrie) Hash() common.Hash {
	root, _, _ := t.buildMerged(runtime.NumCPU())
	return root
}

// Commit builds the merged (base + overlay) trie and returns nodes. Marks overlay committed.
func (t *ParallelStateTrie) Commit(_ bool) (common.Hash, *trienode.NodeSet) {
	root, set, _ := t.buildMerged(runtime.NumCPU())
	// mark overlay committed
	t.overlay.committed = true
	return root, set
}

// Witness delegates to the base trie.
func (t *ParallelStateTrie) Witness() map[string]struct{} {
	return t.base.Witness()
}

// NodeIterator delegates to the base trie.
func (t *ParallelStateTrie) NodeIterator(start []byte) (NodeIterator, error) {
	return t.base.NodeIterator(start)
}

// Prove delegates to the base trie.
func (t *ParallelStateTrie) Prove(key []byte, proofDb ethdb.KeyValueWriter) error {
	return t.base.Prove(key, proofDb)
}

func (t *ParallelStateTrie) IsVerkle() bool { return false }

// buildMerged merges base kvs with overlay mutations and builds in parallel.
func (t *ParallelStateTrie) buildMerged(workers int) (common.Hash, *trienode.NodeSet, error) {
	baseKVs, err := CollectKVsFromTrie(t.base)
	if err != nil {
		return common.Hash{}, nil, err
	}
	merged := make(map[string][]byte, len(baseKVs))
	for _, kv := range baseKVs {
		merged[string(kv.Key)] = kv.Value
	}
	// Apply deletes first
	for _, k := range t.overlay.DeletedKeys() {
		delete(merged, string(k))
	}
	// Apply inserts/updates
	for _, kv := range t.overlay.Pairs() {
		merged[string(kv.Key)] = kv.Value
	}
	// Flatten back
	flat := make([]KVPair, 0, len(merged))
	for k, v := range merged {
		flat = append(flat, KVPair{Key: []byte(k), Value: v})
	}
	return BuildTrieParallelWithOwner(t.owner, flat, workers)
}
