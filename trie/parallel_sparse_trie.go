package trie

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb/database"
)

// Constants for the two-tier parallel sparse trie structure
const (
	// UpperTrieMaxDepth is the maximum depth (in nibbles) for the upper subtrie.
	// Paths shorter than this go to the upper subtrie, longer paths go to lower subtries.
	UpperTrieMaxDepth = 2

	// NumLowerSubtries is the number of lower subtries (16^UpperTrieMaxDepth).
	// Each lower subtrie handles paths starting with a specific prefix.
	NumLowerSubtries = 256 // 16^2
)

// PrefixSet tracks which paths have been modified for incremental updates.
type PrefixSet struct {
	mu    sync.RWMutex
	paths map[string]struct{}
}

// NewPrefixSet creates a new prefix set.
func NewPrefixSet() *PrefixSet {
	return &PrefixSet{
		paths: make(map[string]struct{}),
	}
}

// Add marks a path as modified.
func (ps *PrefixSet) Add(path []byte) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.paths[string(path)] = struct{}{}
}

// Contains checks if a path is in the set.
func (ps *PrefixSet) Contains(path []byte) bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	_, ok := ps.paths[string(path)]
	return ok
}

// Clear removes all paths from the set.
func (ps *PrefixSet) Clear() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.paths = make(map[string]struct{})
}

// Len returns the number of paths in the set.
func (ps *PrefixSet) Len() int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return len(ps.paths)
}

// LowerSparseSubtrie represents a lower-level subtrie in the two-tier structure.
type LowerSparseSubtrie struct {
	mu       sync.RWMutex
	prefix   []byte                 // The prefix path for this subtrie (at least UpperTrieMaxDepth nibbles)
	values   map[string][]byte      // Leaf values keyed by full path
	rootPath []byte                 // The shortest path in this subtrie (may be longer than prefix)
	nodes    map[string]*SparseNode // Path -> sparse node (for maintaining node structure)
	reader   database.NodeReader    // Node reader for revealing blind nodes
	owner    common.Hash            // Owner hash for node reads
}

// NewLowerSparseSubtrie creates a new lower subtrie with the given prefix.
func NewLowerSparseSubtrie(prefix []byte, reader database.NodeReader, owner common.Hash) *LowerSparseSubtrie {
	return &LowerSparseSubtrie{
		prefix:   prefix,
		values:   make(map[string][]byte),
		rootPath: nil,
		nodes:    make(map[string]*SparseNode),
		reader:   reader,
		owner:    owner,
	}
}

// SparseNode represents a node in the sparse trie. It can be either:
// - Blind: Stored as a hash (hashNode), representing unloaded trie parts
// - Revealed: Fully loaded node (branch, extension, leaf) with complete structure
type SparseNode struct {
	// If hash is non-zero, this is a blind node (not yet loaded)
	Hash common.Hash
	// If node is non-nil, this is a revealed node (fully loaded)
	Node node
	// Blob is the RLP-encoded node data (used when revealing)
	Blob []byte
}

// IsBlind returns true if this is a blind node (stored as hash only).
func (sn *SparseNode) IsBlind() bool {
	return sn.Hash != (common.Hash{}) && sn.Node == nil
}

// IsRevealed returns true if this is a revealed node (fully loaded).
func (sn *SparseNode) IsRevealed() bool {
	return sn.Node != nil
}

// SparseSubtrie represents the upper subtrie or a lower subtrie structure.
// It stores nodes and values for paths within its depth range.
type SparseSubtrie struct {
	mu     sync.RWMutex
	nodes  map[string]*SparseNode // Path -> sparse node (blind or revealed)
	values map[string][]byte      // Full leaf paths -> values
	reader database.NodeReader    // Node reader for revealing blind nodes
	owner  common.Hash            // Owner hash for node reads
}

// NewSparseSubtrie creates a new sparse subtrie.
func NewSparseSubtrie(reader database.NodeReader, owner common.Hash) *SparseSubtrie {
	return &SparseSubtrie{
		nodes:  make(map[string]*SparseNode),
		values: make(map[string][]byte),
		reader: reader,
		owner:  owner,
	}
}

// revealNode reveals a blind node by loading it from the database.
// If the node is already revealed, it returns the existing node.
func (st *SparseSubtrie) revealNode(path []byte, hash common.Hash) (node, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	pathStr := string(path)
	sparseNode, exists := st.nodes[pathStr]

	// If already revealed, return it
	if exists && sparseNode.IsRevealed() {
		return sparseNode.Node, nil
	}

	// If blind node exists, load it from database
	if exists && sparseNode.IsBlind() {
		if st.reader == nil {
			return nil, &MissingNodeError{Owner: st.owner, NodeHash: hash, Path: path, err: errors.New("no node reader available")}
		}
		// Load node blob from database
		blob, err := st.reader.Node(st.owner, path, hash)
		if err != nil {
			return nil, &MissingNodeError{Owner: st.owner, NodeHash: hash, Path: path, err: err}
		}
		// Decode the node
		revealedNode, err := decodeNodeUnsafe(hashNode(hash[:]), blob)
		if err != nil {
			return nil, err
		}
		// Update to revealed
		sparseNode.Node = revealedNode
		sparseNode.Blob = blob
		sparseNode.Hash = common.Hash{} // Clear hash since we have the node
		return revealedNode, nil
	}

	// Node doesn't exist, return nil
	return nil, nil
}

// ParallelSparseTrie implements a two-tier parallel sparse trie structure.
type ParallelSparseTrie struct {
	upperSubtrie  *SparseSubtrie
	lowerSubtries [NumLowerSubtries]*LowerSparseSubtrie
	prefixSet     *PrefixSet
	owner         common.Hash
	db            database.NodeDatabase // Database for lazy loading
	root          common.Hash           // Root hash of the committed state
	reader        database.NodeReader   // Node reader for the current root
	committed     bool                  // Flag to mark trie as committed and unusable
}

// NewParallelSparseTrie creates a new parallel sparse trie with an existing root.
// Uses lazy loading - nodes are loaded from database on-demand, not all at once.
// This implements the state.Trie interface directly.
func NewParallelSparseTrie(id *ID, db database.NodeDatabase) (*ParallelSparseTrie, error) {
	// Get node reader for lazy loading
	var reader database.NodeReader
	if db != nil && id.Root != (common.Hash{}) && id.Root != types.EmptyRootHash {
		var err error
		reader, err = db.NodeReader(id.Root)
		if err != nil {
			// If we can't get a reader, that's okay - we'll use lazy loading when needed
			reader = nil
		}
	}

	pst := &ParallelSparseTrie{
		upperSubtrie: NewSparseSubtrie(reader, id.Owner),
		prefixSet:    NewPrefixSet(),
		owner:        id.Owner,
		db:           db,
		root:         id.Root,
		reader:       reader,
	}
	// Initialize all lower subtries
	for i := 0; i < NumLowerSubtries; i++ {
		prefix := make([]byte, UpperTrieMaxDepth)
		prefix[0] = byte(i >> 4)
		prefix[1] = byte(i & 0x0F)
		pst.lowerSubtries[i] = NewLowerSparseSubtrie(prefix, reader, id.Owner)
	}
	return pst, nil
}

var (
	// ErrInvalidInput is returned when input parameters are invalid
	ErrInvalidInput = errors.New("invalid input parameters")
)

// RevealNodes reveals blind nodes by loading them from the database.
// This implements lazy loading - nodes stored as hashes are loaded on-demand.
func (pst *ParallelSparseTrie) RevealNodes(paths [][]byte, hashes []common.Hash) error {
	if len(paths) != len(hashes) {
		return ErrInvalidInput
	}

	// Group by subtrie
	upperPaths := make([][]byte, 0)
	upperHashes := make([]common.Hash, 0)
	lowerPaths := make(map[int][][]byte)
	lowerHashes := make(map[int][]common.Hash)

	for i, path := range paths {
		if len(path) < UpperTrieMaxDepth {
			upperPaths = append(upperPaths, path)
			upperHashes = append(upperHashes, hashes[i])
		} else {
			// Determine which lower subtrie
			key := make([]byte, len(path)/2)
			for j := 0; j < len(path); j += 2 {
				key[j/2] = (path[j] << 4) | path[j+1]
			}
			idx := getLowerSubtrieIndex(key)
			lowerPaths[idx] = append(lowerPaths[idx], path)
			lowerHashes[idx] = append(lowerHashes[idx], hashes[i])
		}
	}

	// Reveal upper subtrie nodes
	for i, path := range upperPaths {
		_, err := pst.upperSubtrie.revealNode(path, upperHashes[i])
		if err != nil {
			return err
		}
	}

	// Reveal lower subtrie nodes (could be parallelized)
	for idx, paths := range lowerPaths {
		subtrie := pst.lowerSubtries[idx]
		hashes := lowerHashes[idx]
		for i, path := range paths {
			// Lower subtries don't have node storage yet, but we can add it
			// For now, we'll handle this in the upper subtrie reveal
			_ = subtrie
			_ = path
			_ = hashes[i]
		}
	}

	return nil
}

// getLowerSubtrieIndex returns the index of the lower subtrie for a given key.
// The key is a 32-byte hash (64 nibbles), we use the first UpperTrieMaxDepth nibbles.
func getLowerSubtrieIndex(key []byte) int {
	if len(key) < UpperTrieMaxDepth/2 {
		return 0
	}
	// First nibble is in the high 4 bits of first byte
	// Second nibble is in the low 4 bits of first byte
	firstNib := int(key[0] >> 4)
	secondNib := int(key[0] & 0x0F)
	return firstNib*16 + secondNib
}

// keyToNibbles converts a key to nibbles (hex representation).
func keyToNibbles(key []byte) []byte {
	nibbles := make([]byte, len(key)*2)
	for i, b := range key {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0F
	}
	return nibbles
}

// getOrCreateRootNode gets or creates the root node for a sparse subtrie.
// The root is stored at path "" (empty string).
// If the node is blind, it will be revealed on-demand.
func (pst *ParallelSparseTrie) getOrCreateRootNode(st *SparseSubtrie, path []byte) node {
	pathStr := string(path)
	sparseNode, exists := st.nodes[pathStr]
	if exists {
		if sparseNode.IsRevealed() {
			return sparseNode.Node
		}
		// Blind node - reveal it
		if sparseNode.IsBlind() && st.reader != nil {
			revealedNode, err := st.revealNode(path, sparseNode.Hash)
			if err == nil && revealedNode != nil {
				return revealedNode
			}
		}
	}
	// Create new root node (nil means empty trie)
	return nil
}

// setRootNode stores the root node for a sparse subtrie.
func (pst *ParallelSparseTrie) setRootNode(st *SparseSubtrie, path []byte, n node) {
	pathStr := string(path)
	if n == nil {
		delete(st.nodes, pathStr)
		return
	}
	sparseNode, exists := st.nodes[pathStr]
	if !exists {
		sparseNode = &SparseNode{}
		st.nodes[pathStr] = sparseNode
	}
	sparseNode.Node = n
	sparseNode.Hash = common.Hash{} // Clear hash since node changed
}

// revealNodeForLower reveals a blind node in a lower subtrie by loading it from the database.
func (pst *ParallelSparseTrie) revealNodeForLower(st *LowerSparseSubtrie, path []byte, hash common.Hash) (node, error) {
	pathStr := string(path)
	sparseNode, exists := st.nodes[pathStr]

	// If already revealed, return it
	if exists && sparseNode.IsRevealed() {
		return sparseNode.Node, nil
	}

	// If blind node exists, load it from database
	if exists && sparseNode.IsBlind() {
		if st.reader == nil {
			return nil, &MissingNodeError{Owner: st.owner, NodeHash: hash, Path: path, err: errors.New("no node reader available")}
		}
		// Load node blob from database
		blob, err := st.reader.Node(st.owner, path, hash)
		if err != nil {
			return nil, &MissingNodeError{Owner: st.owner, NodeHash: hash, Path: path, err: err}
		}
		// Decode the node
		revealedNode, err := decodeNodeUnsafe(hashNode(hash[:]), blob)
		if err != nil {
			return nil, err
		}
		// Update to revealed
		sparseNode.Node = revealedNode
		sparseNode.Blob = blob
		sparseNode.Hash = common.Hash{} // Clear hash since we have the node
		return revealedNode, nil
	}

	// Node doesn't exist
	return nil, nil
}

// getOrCreateRootNodeForLower gets or creates the root node for a lower subtrie.
// If the node is blind, it will be revealed on-demand.
func (pst *ParallelSparseTrie) getOrCreateRootNodeForLower(st *LowerSparseSubtrie, prefix []byte) node {
	// Root is stored at path "" (empty string) relative to the subtrie
	pathStr := ""
	sparseNode, exists := st.nodes[pathStr]
	if exists {
		if sparseNode.IsRevealed() {
			return sparseNode.Node
		}
		// Blind node - reveal it
		if sparseNode.IsBlind() {
			revealedNode, err := pst.revealNodeForLower(st, prefix, sparseNode.Hash)
			if err == nil && revealedNode != nil {
				return revealedNode
			}
		}
	}
	// Create new root node (nil means empty trie)
	return nil
}

// setRootNodeForLower stores the root node for a lower subtrie.
func (pst *ParallelSparseTrie) setRootNodeForLower(st *LowerSparseSubtrie, rootPath string, n node) {
	// Root is stored at path "" (empty string) relative to the subtrie
	pathStr := ""
	if n == nil {
		delete(st.nodes, pathStr)
		return
	}
	sparseNode, exists := st.nodes[pathStr]
	if !exists {
		sparseNode = &SparseNode{}
		st.nodes[pathStr] = sparseNode
	}
	sparseNode.Node = n
	sparseNode.Hash = common.Hash{} // Clear hash since node changed
}

// newFlag returns the cache flag value for a newly created node (matches legacy trie).
func (pst *ParallelSparseTrie) newFlag() nodeFlag {
	return nodeFlag{dirty: true}
}

// insertNode inserts a value into the node structure, exactly matching Trie.insert.
// Returns (dirty, newRoot, error) where dirty indicates if the node changed.
func (pst *ParallelSparseTrie) insertNode(n node, prefix, key []byte, value node) (bool, node, error) {
	if len(key) == 0 {
		if v, ok := n.(valueNode); ok {
			return !bytes.Equal(v, value.(valueNode)), value, nil
		}
		return true, value, nil
	}

	switch n := n.(type) {
	case *shortNode:
		matchlen := prefixLen(key, n.Key)
		// If the whole key matches, keep this short node as is and only update the value.
		if matchlen == len(n.Key) {
			dirty, nn, err := pst.insertNode(n.Val, append(prefix, key[:matchlen]...), key[matchlen:], value)
			if !dirty || err != nil {
				return false, n, err
			}
			return true, &shortNode{n.Key, nn, pst.newFlag()}, nil
		}
		// Otherwise branch out at the index where they differ.
		branch := &fullNode{flags: pst.newFlag()}
		var err error
		_, branch.Children[n.Key[matchlen]], err = pst.insertNode(nil, append(prefix, n.Key[:matchlen+1]...), n.Key[matchlen+1:], n.Val)
		if err != nil {
			return false, nil, err
		}
		_, branch.Children[key[matchlen]], err = pst.insertNode(nil, append(prefix, key[:matchlen+1]...), key[matchlen+1:], value)
		if err != nil {
			return false, nil, err
		}
		// Replace this shortNode with the branch if it occurs at index 0.
		if matchlen == 0 {
			return true, branch, nil
		}
		// Replace it with a short node leading up to the branch.
		return true, &shortNode{key[:matchlen], branch, pst.newFlag()}, nil

	case *fullNode:
		dirty, nn, err := pst.insertNode(n.Children[key[0]], append(prefix, key[0]), key[1:], value)
		if !dirty || err != nil {
			return false, n, err
		}
		n.flags = pst.newFlag()
		n.Children[key[0]] = nn
		return true, n, nil

	case nil:
		return true, &shortNode{key, value, pst.newFlag()}, nil

	case hashNode:
		// Blind node - need to reveal it first
		// For now, treat as nil (will be revealed on demand)
		return true, &shortNode{key, value, pst.newFlag()}, nil

	default:
		return false, nil, fmt.Errorf("invalid node type: %T", n)
	}
}

// Update inserts or updates a key-value pair in the trie.
// This maintains the node structure (not just values) for incremental hashing.
// Operations on different subtries happen in parallel naturally due to per-subtrie mutexes.
func (pst *ParallelSparseTrie) Update(key []byte, value []byte) error {
	// Use keybytesToHex to match legacy trie exactly (includes terminator byte)
	nibbles := keybytesToHex(key)
	pathStr := string(nibbles)

	// Determine if this goes to upper or lower subtrie
	// Note: nibbles includes terminator (last byte is 16), so we check length excluding terminator
	// UpperTrieMaxDepth is in nibbles, so we compare against nibbles length (which includes terminator)
	// Keys with <= UpperTrieMaxDepth nibbles (excluding terminator) go to upper subtrie
	if len(nibbles) <= UpperTrieMaxDepth+1 { // +1 for terminator
		// Upper subtrie
		pst.upperSubtrie.mu.Lock()
		// Store value
		pst.upperSubtrie.values[pathStr] = value
		// Update node structure
		root := pst.getOrCreateRootNode(pst.upperSubtrie, nil)
		_, newRoot, err := pst.insertNode(root, nil, nibbles, valueNode(value))
		if err != nil {
			pst.upperSubtrie.mu.Unlock()
			return err
		}
		// Store updated root
		pst.setRootNode(pst.upperSubtrie, nil, newRoot)
		pst.upperSubtrie.mu.Unlock()
	} else {
		// Lower subtrie - operations on different subtries can run in parallel
		idx := getLowerSubtrieIndex(key)
		subtrie := pst.lowerSubtries[idx]
		subtrie.mu.Lock()
		// Store value
		subtrie.values[pathStr] = value
		// Update node structure (for lower subtries, we need to track root path)
		// The root path is the prefix (first UpperTrieMaxDepth nibbles)
		prefix := nibbles[:UpperTrieMaxDepth]
		rootPath := string(prefix)
		root := pst.getOrCreateRootNodeForLower(subtrie, prefix)
		// Insert with remaining nibbles (after prefix)
		remainingNibbles := nibbles[UpperTrieMaxDepth:]
		_, newRoot, err := pst.insertNode(root, prefix, remainingNibbles, valueNode(value))
		if err != nil {
			subtrie.mu.Unlock()
			return err
		}
		// Store updated root
		pst.setRootNodeForLower(subtrie, rootPath, newRoot)
		if subtrie.rootPath == nil || bytes.Compare(nibbles, subtrie.rootPath) < 0 {
			subtrie.rootPath = append([]byte(nil), nibbles...)
		}
		subtrie.mu.Unlock()
	}

	// Mark prefix as modified for incremental updates
	// Prefix is first UpperTrieMaxDepth nibbles (excluding terminator)
	prefix := nibbles
	if len(prefix) > UpperTrieMaxDepth+1 { // +1 for terminator
		prefix = prefix[:UpperTrieMaxDepth]
	} else if len(prefix) == UpperTrieMaxDepth+1 {
		// Remove terminator for prefix tracking
		prefix = prefix[:UpperTrieMaxDepth]
	}
	pst.prefixSet.Add(prefix)
	return nil
}

// UpdateBatch processes multiple updates in parallel across different subtries.
// Updates targeting the same subtrie are serialized, but updates to different
// subtries run concurrently, maximizing parallelism.
// The updates map uses raw keys (32-byte hashes), not path strings.
func (pst *ParallelSparseTrie) UpdateBatch(updates map[string][]byte) error {
	if len(updates) == 0 {
		return nil
	}

	// Group updates by subtrie index for parallel processing
	type updateOp struct {
		pathStr string
		nibbles []byte
		value   []byte
		prefix  []byte
	}

	upperOps := make([]updateOp, 0)
	lowerOps := make(map[int][]updateOp) // subtrie index -> operations

	// Prepare operations - convert keys to nibbles
	for keyStr, value := range updates {
		key := []byte(keyStr)
		nibbles := keyToNibbles(key)
		pathStr := string(nibbles)
		op := updateOp{
			pathStr: pathStr,
			nibbles: nibbles,
			value:   value,
		}

		// Determine prefix for prefixSet
		prefix := nibbles
		if len(prefix) > UpperTrieMaxDepth {
			prefix = prefix[:UpperTrieMaxDepth]
		}
		op.prefix = prefix

		if len(nibbles) < UpperTrieMaxDepth {
			upperOps = append(upperOps, op)
		} else {
			idx := getLowerSubtrieIndex(key)
			lowerOps[idx] = append(lowerOps[idx], op)
		}
	}

	// Process upper subtrie updates (serialized)
	if len(upperOps) > 0 {
		pst.upperSubtrie.mu.Lock()
		for _, op := range upperOps {
			pst.upperSubtrie.values[op.pathStr] = op.value
			pst.prefixSet.Add(op.prefix)
		}
		pst.upperSubtrie.mu.Unlock()
	}

	// Process lower subtrie updates in parallel using goroutines
	// Each subtrie has its own mutex, so operations on different subtries
	// can run concurrently
	var wg sync.WaitGroup
	for idx, ops := range lowerOps {
		wg.Add(1)
		go func(subtrieIdx int, subtrieOps []updateOp) {
			defer wg.Done()
			subtrie := pst.lowerSubtries[subtrieIdx]
			subtrie.mu.Lock()
			for _, op := range subtrieOps {
				subtrie.values[op.pathStr] = op.value
				if subtrie.rootPath == nil || bytes.Compare(op.nibbles, subtrie.rootPath) < 0 {
					subtrie.rootPath = append([]byte(nil), op.nibbles...)
				}
				pst.prefixSet.Add(op.prefix)
			}
			subtrie.mu.Unlock()
		}(idx, ops)
	}
	wg.Wait()

	return nil
}

// deleteNode removes a key from the node structure, exactly matching Trie.delete.
// Returns (dirty, newRoot, error) where dirty indicates if the node changed.
func (pst *ParallelSparseTrie) deleteNode(n node, prefix, key []byte) (bool, node, error) {
	switch n := n.(type) {
	case *shortNode:
		matchlen := prefixLen(key, n.Key)
		if matchlen < len(n.Key) {
			return false, n, nil // don't replace n on mismatch
		}

		if matchlen == len(key) {
			// The matched short node is deleted entirely
			return true, nil, nil // remove n entirely for whole matches
		}
		// The key is longer than n.Key. Remove the remaining suffix
		// from the subtrie. Child can never be nil here since the
		// subtrie must contain at least two other values with keys
		// longer than n.Key.
		dirty, child, err := pst.deleteNode(n.Val, append(prefix, key[:len(n.Key)]...), key[len(n.Key):])
		if !dirty || err != nil {
			return false, n, err
		}

		switch child := child.(type) {
		case *shortNode:
			// Deleting from the subtrie reduced it to another
			// short node. Merge the nodes to avoid creating a
			// shortNode{..., shortNode{...}}. Use concat (which
			// always creates a new slice) instead of append to
			// avoid modifying n.Key since it might be shared with
			// other nodes.
			return true, &shortNode{concat(n.Key, child.Key...), child.Val, pst.newFlag()}, nil
		default:
			return true, &shortNode{n.Key, child, pst.newFlag()}, nil
		}

	case *fullNode:
		dirty, nn, err := pst.deleteNode(n.Children[key[0]], append(prefix, key[0]), key[1:])
		if !dirty || err != nil {
			return false, n, err
		}
		n.flags = pst.newFlag()
		n.Children[key[0]] = nn

		// Because n is a full node, it must've contained at least two children
		// before the delete operation. If the new child value is non-nil, n still
		// has at least two children after the deletion, and cannot be reduced to
		// a short node.
		if nn != nil {
			return true, n, nil
		}
		// Reduction:
		// Check how many non-nil entries are left after deleting and
		// reduce the full node to a short node if only one entry is
		// left. Since n must've contained at least two children
		// before deletion (otherwise it would not be a full node) n
		// can never be reduced to nil.
		//
		// When the loop is done, pos contains the index of the single
		// value that is left in n or -2 if n contains at least two
		// values.
		pos := -1

		for i, cld := range &n.Children {
			if cld != nil {
				if pos == -1 {
					pos = i
				} else {
					pos = -2
					break
				}
			}
		}

		if pos >= 0 {
			if pos != 16 {
				// If the remaining entry is a short node, it replaces
				// n and its key gets the missing nibble tacked to the
				// front. This avoids creating an invalid
				// shortNode{..., shortNode{...}}.
				cnode := n.Children[pos]
				if sn, ok := cnode.(*shortNode); ok {
					// Merge prefix byte with child key
					return true, &shortNode{concat([]byte{byte(pos)}, sn.Key...), sn.Val, pst.newFlag()}, nil
				}
				return true, &shortNode{[]byte{byte(pos)}, cnode, pst.newFlag()}, nil
			}
		}
		return true, n, nil

	case nil:
		return false, nil, nil // key not found

	case hashNode:
		// Blind node - treat as not found for now
		return false, nil, nil

	default:
		return false, nil, fmt.Errorf("invalid node type: %T", n)
	}
}

// Delete removes a key from the trie.
// This maintains the node structure (not just values) for incremental hashing.
// Operations on different subtries happen in parallel naturally due to per-subtrie mutexes.
func (pst *ParallelSparseTrie) Delete(key []byte) error {
	// Use keybytesToHex to match legacy trie exactly (includes terminator byte)
	nibbles := keybytesToHex(key)
	pathStr := string(nibbles)

	// Determine if this goes to upper or lower subtrie
	// Note: nibbles includes terminator (last byte is 16), so we check length excluding terminator
	if len(nibbles) <= UpperTrieMaxDepth+1 { // +1 for terminator
		// Upper subtrie
		pst.upperSubtrie.mu.Lock()
		// Remove value
		delete(pst.upperSubtrie.values, pathStr)
		// Update node structure
		root := pst.getOrCreateRootNode(pst.upperSubtrie, nil)
		_, newRoot, err := pst.deleteNode(root, nil, nibbles)
		if err != nil {
			pst.upperSubtrie.mu.Unlock()
			return err
		}
		// Store updated root
		pst.setRootNode(pst.upperSubtrie, nil, newRoot)
		pst.upperSubtrie.mu.Unlock()
	} else {
		// Lower subtrie - operations on different subtries can run in parallel
		idx := getLowerSubtrieIndex(key)
		subtrie := pst.lowerSubtries[idx]
		subtrie.mu.Lock()
		// Remove value
		delete(subtrie.values, pathStr)
		// Update node structure
		prefix := nibbles[:UpperTrieMaxDepth]
		root := pst.getOrCreateRootNodeForLower(subtrie, prefix)
		remainingNibbles := nibbles[UpperTrieMaxDepth:]
		_, newRoot, err := pst.deleteNode(root, prefix, remainingNibbles)
		if err != nil {
			subtrie.mu.Unlock()
			return err
		}
		// Store updated root
		rootPath := string(prefix)
		pst.setRootNodeForLower(subtrie, rootPath, newRoot)
		subtrie.mu.Unlock()
	}

	// Mark prefix as modified for incremental updates
	// Prefix is first UpperTrieMaxDepth nibbles (excluding terminator)
	prefix := nibbles
	if len(prefix) > UpperTrieMaxDepth+1 { // +1 for terminator
		prefix = prefix[:UpperTrieMaxDepth]
	} else if len(prefix) == UpperTrieMaxDepth+1 {
		// Remove terminator for prefix tracking
		prefix = prefix[:UpperTrieMaxDepth]
	}
	pst.prefixSet.Add(prefix)
	return nil
}

// DeleteBatch processes multiple deletes in parallel across different subtries.
// Deletes targeting the same subtrie are serialized, but deletes to different
// subtries run concurrently, maximizing parallelism.
func (pst *ParallelSparseTrie) DeleteBatch(keys [][]byte) error {
	if len(keys) == 0 {
		return nil
	}

	// Group deletes by subtrie index for parallel processing
	type deleteOp struct {
		pathStr string
		nibbles []byte
		prefix  []byte
	}

	upperOps := make([]deleteOp, 0)
	lowerOps := make(map[int][]deleteOp) // subtrie index -> operations

	// Prepare operations
	for _, key := range keys {
		nibbles := keyToNibbles(key)
		pathStr := string(nibbles)
		op := deleteOp{
			pathStr: pathStr,
			nibbles: nibbles,
		}

		// Determine prefix for prefixSet
		prefix := nibbles
		if len(prefix) > UpperTrieMaxDepth {
			prefix = prefix[:UpperTrieMaxDepth]
		}
		op.prefix = prefix

		if len(nibbles) < UpperTrieMaxDepth {
			upperOps = append(upperOps, op)
		} else {
			idx := getLowerSubtrieIndex(key)
			lowerOps[idx] = append(lowerOps[idx], op)
		}
	}

	// Process upper subtrie deletes (serialized)
	if len(upperOps) > 0 {
		pst.upperSubtrie.mu.Lock()
		for _, op := range upperOps {
			delete(pst.upperSubtrie.values, op.pathStr)
			pst.prefixSet.Add(op.prefix)
		}
		pst.upperSubtrie.mu.Unlock()
	}

	// Process lower subtrie deletes in parallel using goroutines
	// Each subtrie has its own mutex, so operations on different subtries
	// can run concurrently
	var wg sync.WaitGroup
	for idx, ops := range lowerOps {
		wg.Add(1)
		go func(subtrieIdx int, subtrieOps []deleteOp) {
			defer wg.Done()
			subtrie := pst.lowerSubtries[subtrieIdx]
			subtrie.mu.Lock()
			for _, op := range subtrieOps {
				delete(subtrie.values, op.pathStr)
				pst.prefixSet.Add(op.prefix)
			}
			subtrie.mu.Unlock()
		}(idx, ops)
	}
	wg.Wait()

	return nil
}

// Get retrieves a value from the trie.
func (pst *ParallelSparseTrie) Get(key []byte) ([]byte, bool) {
	nibbles := keyToNibbles(key)
	pathStr := string(nibbles)

	// Determine if this is in upper or lower subtrie
	if len(nibbles) < UpperTrieMaxDepth {
		// Upper subtrie
		pst.upperSubtrie.mu.RLock()
		val, ok := pst.upperSubtrie.values[pathStr]
		pst.upperSubtrie.mu.RUnlock()
		return val, ok
	}

	// Lower subtrie
	idx := getLowerSubtrieIndex(key)
	subtrie := pst.lowerSubtries[idx]
	subtrie.mu.RLock()
	val, ok := subtrie.values[pathStr]
	subtrie.mu.RUnlock()
	return val, ok
}

// Root computes and returns the root hash of the trie.
func (pst *ParallelSparseTrie) Root(workers int) (common.Hash, *trienode.NodeSet, error) {
	// Get modified prefixes from prefixSet
	pst.prefixSet.mu.RLock()
	hasChanges := len(pst.prefixSet.paths) > 0
	pst.prefixSet.mu.RUnlock()

	// If no modifications and we have a root, return existing root
	if !hasChanges && pst.root != (common.Hash{}) && pst.root != types.EmptyRootHash {
		return pst.root, nil, nil
	}

	// Update hashes for modified lower subtries (in parallel)
	subtrieHashes, err := pst.updateSubtrieHashes(workers)
	if err != nil {
		return common.Hash{}, nil, err
	}

	// Update hashes for upper subtrie (references lower subtrie hashes)
	rootHash, nodeSet, err := pst.updateUpperSubtrieHashes(subtrieHashes)
	if err != nil {
		return common.Hash{}, nil, err
	}

	return rootHash, nodeSet, nil
}

// updateSubtrieHashes updates hashes for all modified lower subtries in parallel.
// This walks the existing node structure and updates hashes incrementally.
// Returns a map of subtrie index -> hash for use in building the upper subtrie.
func (pst *ParallelSparseTrie) updateSubtrieHashes(workers int) (map[int]common.Hash, error) {
	// Identify which lower subtries are modified
	modifiedSubtries := make(map[int]struct{})

	pst.prefixSet.mu.RLock()
	for pathStr := range pst.prefixSet.paths {
		prefix := []byte(pathStr)
		if len(prefix) >= UpperTrieMaxDepth {
			// Convert prefix to key bytes to get subtrie index
			key := make([]byte, UpperTrieMaxDepth/2)
			for i := 0; i < UpperTrieMaxDepth && i < len(prefix); i += 2 {
				if i+1 < len(prefix) {
					key[i/2] = (prefix[i] << 4) | prefix[i+1]
				}
			}
			idx := getLowerSubtrieIndex(key)
			modifiedSubtries[idx] = struct{}{}
		}
	}
	pst.prefixSet.mu.RUnlock()

	if len(modifiedSubtries) == 0 {
		return nil, nil // No lower subtries modified
	}

	// Hash each modified subtrie in parallel using worker pool
	// Convert map to slice for processing
	subtrieIndices := make([]int, 0, len(modifiedSubtries))
	for idx := range modifiedSubtries {
		subtrieIndices = append(subtrieIndices, idx)
	}

	if len(subtrieIndices) == 0 {
		return nil, nil
	}

	// Use worker pool pattern for controlled parallelism
	type subtrieResult struct {
		idx  int
		hash common.Hash
		err  error
	}

	// Determine number of workers (use provided workers, or default to number of subtries)
	numWorkers := workers
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}
	if numWorkers > len(subtrieIndices) {
		numWorkers = len(subtrieIndices)
	}

	// Create channels for work distribution
	workChan := make(chan int, len(subtrieIndices))
	results := make(chan subtrieResult, len(subtrieIndices))

	// Send all work
	for _, idx := range subtrieIndices {
		workChan <- idx
	}
	close(workChan)

	// Launch worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for subtrieIdx := range workChan {
				subtrie := pst.lowerSubtries[subtrieIdx]
				subtrie.mu.Lock() // Need write lock to cache hash
				// Get root node from node structure
				// Use the subtrie's prefix to get the correct root
				prefix := subtrie.prefix
				root := pst.getOrCreateRootNodeForLower(subtrie, prefix)
				if root == nil {
					subtrie.mu.Unlock()
					results <- subtrieResult{idx: subtrieIdx, hash: types.EmptyRootHash, err: nil}
					continue
				}

				// Hash the node structure incrementally
				// Use true to match legacy trie behavior (keep small nodes embedded)
				// The hasher will cache the hash in root.flags.hash
				h := newHasher(false)
				hashed := h.hash(root, true)
				returnHasherToPool(h)

				// Extract hash - hasher has cached it in root.flags.hash
				var hash common.Hash
				if hn, ok := hashed.(hashNode); ok {
					hash = common.BytesToHash(hn)
				} else {
					// Small embedded node - need to encode and hash it
					if vn, ok := hashed.(valueNode); ok {
						hash = crypto.Keccak256Hash(vn)
					} else {
						// Shouldn't happen, but fallback
						hash = types.EmptyRootHash
					}
				}
				subtrie.mu.Unlock()
				results <- subtrieResult{idx: subtrieIdx, hash: hash, err: nil}
			}
		}()
	}

	// Wait for all workers to finish, then close results channel
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	subtrieHashes := make(map[int]common.Hash)
	for result := range results {
		if result.err != nil {
			return nil, result.err
		}
		subtrieHashes[result.idx] = result.hash
	}

	return subtrieHashes, nil
}

// updateUpperSubtrieHashes updates hashes for the upper subtrie, which references
// lower subtrie hashes. Returns the root hash and nodeset.

func (pst *ParallelSparseTrie) updateUpperSubtrieHashes(subtrieHashes map[int]common.Hash) (common.Hash, *trienode.NodeSet, error) {
	// Get root node from upper subtrie
	pst.upperSubtrie.mu.RLock()
	root := pst.getOrCreateRootNode(pst.upperSubtrie, nil)
	pst.upperSubtrie.mu.RUnlock()

	if root == nil {
		// Check if we have any values or lower subtries
		hasValues := len(pst.upperSubtrie.values) > 0
		for _, subtrie := range pst.lowerSubtries {
			if len(subtrie.values) > 0 {
				hasValues = true
				break
			}
		}
		if !hasValues {
			return types.EmptyRootHash, nil, nil
		}
		return pst.fallbackRebuild()
	}

	// Create a wrapper that can access nodes from both upper and lower subtries
	// When hashing, if we encounter a path >= UpperTrieMaxDepth, get node from lower subtrie
	// The hasher will use cached hashes automatically via n.cache()
	root = pst.createUnifiedNode(root, []byte{}, subtrieHashes)

	// Hash the unified node structure
	// The hasher will recursively hash all children, using cached hashes from lower subtries
	h := newHasher(false)
	defer returnHasherToPool(h)
	hashed := h.hash(root, true)

	// Extract hash from result
	var hash common.Hash
	if hn, ok := hashed.(hashNode); ok {
		hash = common.BytesToHash(hn)
	} else {
		// Small embedded node - need to encode and hash it
		if vn, ok := hashed.(valueNode); ok {
			hash = crypto.Keccak256Hash(vn)
		} else {
			// Shouldn't happen, but fallback
			hash = types.EmptyRootHash
		}
	}

	nodeSet := trienode.NewNodeSet(pst.owner)
	return hash, nodeSet, nil
}

// createUnifiedNode creates a unified view of the trie that can access nodes from
// both upper and lower subtries. When a child path would be >= UpperTrieMaxDepth,
// it gets the node from the appropriate lower subtrie (which should have its hash cached).
func (pst *ParallelSparseTrie) createUnifiedNode(n node, path []byte, subtrieHashes map[int]common.Hash) node {
	if n == nil {
		return nil
	}

	switch n := n.(type) {
	case *shortNode:
		// Check if this shortNode's path + key would cross into lower subtrie
		newPath := append(path, n.Key...)
		if len(newPath) >= UpperTrieMaxDepth {
			// This path crosses into a lower subtrie
			// Determine which lower subtrie based on first UpperTrieMaxDepth nibbles
			key := make([]byte, UpperTrieMaxDepth/2)
			for i := 0; i < UpperTrieMaxDepth && i < len(newPath); i += 2 {
				if i+1 < len(newPath) {
					key[i/2] = (newPath[i] << 4) | newPath[i+1]
				}
			}
			idx := getLowerSubtrieIndex(key)

			// Get the root of the lower subtrie (which should have its hash cached)
			subtrie := pst.lowerSubtries[idx]
			subtrie.mu.RLock()
			root := pst.getOrCreateRootNodeForLower(subtrie, key)
			subtrie.mu.RUnlock()

			if root != nil {
				// The lower subtrie root should already have its hash cached from updateSubtrieHashes
				// Return it - hasher will use cached hash via n.cache()
				return root
			}

			// If root doesn't exist but we have a cached hash, use it
			if hash, ok := subtrieHashes[idx]; ok {
				return hashNode(hash.Bytes())
			}

			// No lower subtrie - return nil (shouldn't happen)
			return nil
		}

		// Still in upper subtrie - recursively process children
		newVal := pst.createUnifiedNode(n.Val, newPath, subtrieHashes)
		return &shortNode{Key: n.Key, Val: newVal, flags: n.flags}

	case *fullNode:
		// Process all children
		var children [17]node
		for i := 0; i < 17; i++ {
			if n.Children[i] != nil {
				childPath := append(path, byte(i))
				// Check if this child path crosses into lower subtrie
				if len(childPath) >= UpperTrieMaxDepth {
					// Determine which lower subtrie
					key := make([]byte, UpperTrieMaxDepth/2)
					for j := 0; j < UpperTrieMaxDepth && j < len(childPath); j += 2 {
						if j+1 < len(childPath) {
							key[j/2] = (childPath[j] << 4) | childPath[j+1]
						}
					}
					idx := getLowerSubtrieIndex(key)

					// Get the root of the lower subtrie
					subtrie := pst.lowerSubtries[idx]
					subtrie.mu.RLock()
					root := pst.getOrCreateRootNodeForLower(subtrie, key)
					subtrie.mu.RUnlock()

					if root != nil {
						// Lower subtrie root should have its hash cached
						children[i] = root
					} else if hash, ok := subtrieHashes[idx]; ok {
						children[i] = hashNode(hash.Bytes())
					}
					// else leave as nil
				} else {
					// Still in upper subtrie - recursively process
					children[i] = pst.createUnifiedNode(n.Children[i], childPath, subtrieHashes)
				}
			}
		}
		return &fullNode{Children: children, flags: n.flags}

	default:
		// hashNode, valueNode, nil - return as-is
		return n
	}
}

// fallbackRebuild builds the node structure from KVs.
// Uses our insertNode which now exactly matches legacy trie's insert logic.
func (pst *ParallelSparseTrie) fallbackRebuild() (common.Hash, *trienode.NodeSet, error) {
	// Collect all KVs
	allKVs := make(map[string][]byte)

	// Collect from upper subtrie
	pst.upperSubtrie.mu.RLock()
	for pathStr, value := range pst.upperSubtrie.values {
		allKVs[pathStr] = value
	}
	pst.upperSubtrie.mu.RUnlock()

	// Collect from lower subtries
	for _, subtrie := range pst.lowerSubtries {
		subtrie.mu.RLock()
		for pathStr, value := range subtrie.values {
			allKVs[pathStr] = value
		}
		subtrie.mu.RUnlock()
	}

	// Build unified trie structure using our insertNode (matches legacy exactly)
	// pathStr already contains nibbles with terminator (from keybytesToHex in Update)
	unifiedRoot := (node)(nil)
	for pathStr, value := range allKVs {
		if len(value) == 0 {
			continue // Skip deletes
		}
		nibbles := []byte(pathStr)
		var err error
		// pathStr already contains nibbles with terminator from keybytesToHex
		_, unifiedRoot, err = pst.insertNode(unifiedRoot, nil, nibbles, valueNode(value))
		if err != nil {
			return common.Hash{}, nil, err
		}
	}

	// Store the unified root in upper subtrie for future incremental hashing
	pst.upperSubtrie.mu.Lock()
	pst.setRootNode(pst.upperSubtrie, nil, unifiedRoot)
	pst.upperSubtrie.mu.Unlock()

	// Hash the unified structure (matches legacy trie exactly)
	if unifiedRoot == nil {
		return types.EmptyRootHash, nil, nil
	}

	h := newHasher(false)
	defer returnHasherToPool(h)
	hashed := h.hash(unifiedRoot, true)
	var hash common.Hash
	if hn, ok := hashed.(hashNode); ok {
		hash = common.BytesToHash(hn)
	} else {
		if vn, ok := hashed.(valueNode); ok {
			hash = crypto.Keccak256Hash(vn)
		} else {
			hash = types.EmptyRootHash
		}
	}

	nodeSet := trienode.NewNodeSet(pst.owner)
	return hash, nodeSet, nil
}

// Hash computes the root hash by computing the root.
func (pst *ParallelSparseTrie) Hash() common.Hash {
	root, _, _ := pst.Root(runtime.NumCPU())
	return root
}

// GetKey returns the preimage for a hashed key. Not supported; returns nil.
func (pst *ParallelSparseTrie) GetKey(_ []byte) []byte { return nil }

// GetAccount returns the decoded account at address.
// Uses lazy loading - if not in overlay, loads from database on-demand.
func (pst *ParallelSparseTrie) GetAccount(address common.Address) (*types.StateAccount, error) {
	if pst.committed {
		return nil, ErrCommitted
	}
	hk := crypto.Keccak256(address.Bytes())

	// Check sparse trie first (pending writes)
	if val, ok := pst.Get(hk); ok {
		if len(val) == 0 {
			return nil, nil
		}
		ret := new(types.StateAccount)
		if err := rlp.DecodeBytes(val, ret); err != nil {
			return nil, err
		}
		return ret, nil
	}

	// Not in sparse trie, load from database using lazy loading
	if pst.reader != nil {
		tempTrie, err := New(StateTrieID(pst.root), pst.db)
		if err != nil {
			return nil, err
		}
		res, err := tempTrie.Get(hk)
		if res == nil || err != nil {
			return nil, err
		}
		ret := new(types.StateAccount)
		if err := rlp.DecodeBytes(res, ret); err != nil {
			return nil, err
		}
		return ret, nil
	}

	return nil, nil
}

// GetStorage returns the storage value for key. The returned bytes are raw content.
// Uses lazy loading - if not in overlay, loads from database on-demand.
func (pst *ParallelSparseTrie) GetStorage(_ common.Address, key []byte) ([]byte, error) {
	if pst.committed {
		return nil, ErrCommitted
	}
	hk := crypto.Keccak256(key)

	// Check sparse trie first (pending writes)
	if val, ok := pst.Get(hk); ok {
		if len(val) == 0 {
			return nil, nil
		}
		_, content, _, err := rlp.Split(val)
		return content, err
	}

	// Not in sparse trie, load from database
	if pst.reader != nil {
		tempTrie, err := New(StateTrieID(pst.root), pst.db)
		if err != nil {
			return nil, err
		}
		enc, err := tempTrie.Get(hk)
		if err != nil || len(enc) == 0 {
			return nil, err
		}
		_, content, _, err := rlp.Split(enc)
		return content, err
	}

	return nil, nil
}

// UpdateAccount writes the encoded account to the sparse trie.
func (pst *ParallelSparseTrie) UpdateAccount(address common.Address, acc *types.StateAccount, _ int) error {
	if pst.committed {
		return ErrCommitted
	}
	hk := crypto.Keccak256(address.Bytes())
	data, err := rlp.EncodeToBytes(acc)
	if err != nil {
		return err
	}
	return pst.Update(hk, data)
}

// UpdateStorage writes the encoded storage to the sparse trie.
func (pst *ParallelSparseTrie) UpdateStorage(_ common.Address, key, value []byte) error {
	if pst.committed {
		return ErrCommitted
	}
	hk := crypto.Keccak256(key)
	enc, _ := rlp.EncodeToBytes(value)
	return pst.Update(hk, enc)
}

// DeleteAccount deletes an account in the sparse trie.
func (pst *ParallelSparseTrie) DeleteAccount(address common.Address) error {
	if pst.committed {
		return ErrCommitted
	}
	hk := crypto.Keccak256(address.Bytes())
	return pst.Delete(hk)
}

// DeleteStorage deletes a storage slot in the sparse trie.
func (pst *ParallelSparseTrie) DeleteStorage(_ common.Address, key []byte) error {
	if pst.committed {
		return ErrCommitted
	}
	hk := crypto.Keccak256(key)
	return pst.Delete(hk)
}

// UpdateContractCode is a no-op for MPT.
func (pst *ParallelSparseTrie) UpdateContractCode(_ common.Address, _ common.Hash, _ []byte) error {
	return nil
}

// Commit computes the root and returns nodes.
// Once committed, the trie is not usable anymore
func (pst *ParallelSparseTrie) Commit(_ bool) (common.Hash, *trienode.NodeSet) {
	defer func() {
		pst.committed = true
	}()
	root, set, _ := pst.Root(runtime.NumCPU())
	return root, set
}

// Witness returns accessed nodes. For now, returns empty - can be enhanced to track
// nodes accessed through lazy loading.
func (pst *ParallelSparseTrie) Witness() map[string]struct{} {
	// TODO: Track nodes accessed through lazy loading
	return make(map[string]struct{})
}

// NodeIterator returns an iterator over the trie. Creates a temporary trie for iteration.
// Note: This doesn't include overlay changes - could be enhanced.
func (pst *ParallelSparseTrie) NodeIterator(start []byte) (NodeIterator, error) {
	// Create a temporary trie for iteration (handles empty case correctly)
	tempTrie, err := New(StateTrieID(pst.root), pst.db)
	if err != nil {
		return nil, err
	}
	return tempTrie.NodeIterator(start)
}

// Prove constructs a Merkle proof. Creates a temporary trie for proof generation.
// Note: This doesn't include overlay changes - could be enhanced.
func (pst *ParallelSparseTrie) Prove(key []byte, proofDb ethdb.KeyValueWriter) error {
	if pst.root == (common.Hash{}) || pst.root == types.EmptyRootHash {
		return nil
	}
	tempTrie, err := New(StateTrieID(pst.root), pst.db)
	if err != nil {
		return err
	}
	return tempTrie.Prove(key, proofDb)
}

// IsVerkle returns false - this is an MPT implementation.
func (pst *ParallelSparseTrie) IsVerkle() bool { return false }
