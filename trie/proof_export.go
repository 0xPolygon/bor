package trie

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
)

// PathNode represents a trie node on the path, with its hex nibbles path (no terminator)
// and the RLP-encoded node blob.
type PathNode struct {
	Path []byte
	Blob []byte
}

// ProofToPath converts a merkle proof (as produced by (*Trie).Prove) into a root->leaf
// sequence of path nodes. The returned paths are hex nibbles without the terminator.
// The function also verifies that the proof is consistent with the provided root hash.
func ProofToPath(rootHash common.Hash, key []byte, proof ethdb.KeyValueReader) ([]PathNode, error) {
	// Verify the proof first to ensure correctness (also sanity-checks presence of nodes).
	if _, err := VerifyProof(rootHash, key, proof); err != nil {
		return nil, err
	}

	keyhex := keybytesToHex(key)
	var (
		nodes    []PathNode
		path     []byte
		remain   = append([]byte{}, keyhex...)
		wantHash = rootHash
	)

	for {
		buf, _ := proof.Get(wantHash[:])
		if buf == nil {
			return nil, fmt.Errorf("proof node (hash %064x) missing", wantHash)
		}
		// Emit the current node at the current path
		nodes = append(nodes, PathNode{Path: append([]byte{}, path...), Blob: append([]byte{}, buf...)})

		n, err := decodeNode(wantHash[:], buf)
		if err != nil {
			return nil, fmt.Errorf("bad proof node: %v", err)
		}

		// Walk embedded children (short/full) to the next hashed boundary or value,
		// updating the path accurately according to node structure and key.
		for {
			switch cn := n.(type) {
			case *shortNode:
				// Ensure prefix matches; if not, proof indicates non-existence; we can stop.
				if len(remain) < len(cn.Key) {
					return nodes, nil
				}
				for i := 0; i < len(cn.Key); i++ {
					if remain[i] != cn.Key[i] {
						return nodes, nil
					}
				}
				path = append(path, cn.Key...)
				remain = remain[len(cn.Key):]
				switch ch := cn.Val.(type) {
				case hashNode:
					copy(wantHash[:], ch)
					n = nil // signal to fetch next proof element
					break
				case valueNode:
					return nodes, nil
				default:
					// embedded child, continue walking
					n = ch
				}
			case *fullNode:
				if len(remain) == 0 {
					// No further path; stop
					return nodes, nil
				}
				idx := remain[0]
				path = append(path, idx)
				remain = remain[1:]
				ch := cn.Children[idx]
				switch chv := ch.(type) {
				case hashNode:
					copy(wantHash[:], chv)
					n = nil
					break
				case valueNode:
					return nodes, nil
				default:
					// embedded child, continue walking
					n = chv
				}
			case valueNode:
				return nodes, nil
			case hashNode:
				// shouldn't happen here
				return nodes, nil
			default:
				return nodes, nil
			}
			// If we set n=nil due to a hash boundary, break out to fetch next proof element
			if n == nil {
				break
			}
		}
		// Continue outer loop to fetch next node by wantHash; if remain exhausted, we may still fetch
		// the leaf container node, which VerifyProof ensured exists.
	}
}

// PathNodesForTrie walks the live trie and returns standalone (hashed) nodes along the path
// to the given key, emitting them root->leaf with accurate nibble paths (without terminator).
func PathNodesForTrie(t *Trie, key []byte) ([]PathNode, error) {
	var out []PathNode
	it := newNodeIterator(t, key)
	for it.Next(true) {
		blob := it.NodeBlob()
		if len(blob) == 0 {
			continue // embedded node; skip
		}
		path := it.Path()
		if hasTerm(path) {
			path = path[:len(path)-1]
		}
		out = append(out, PathNode{
			Path: append([]byte{}, path...),
			Blob: append([]byte{}, blob...),
		})
		if it.Leaf() {
			break
		}
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	return out, nil
}

// PathNodesForAccount walks the live state trie and returns the standalone nodes along the path.
func PathNodesForAccount(st *StateTrie, key []byte) ([]PathNode, error) {
	return PathNodesForTrie(&st.trie, key)
}

// PathNodesForStorage walks a live storage trie (wrapped in StateTrie) and returns path nodes.
func PathNodesForStorage(st *StateTrie, key []byte) ([]PathNode, error) {
	return PathNodesForTrie(&st.trie, key)
}
