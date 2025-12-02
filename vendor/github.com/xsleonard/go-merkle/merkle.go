/* Copyright 2013 Steve Leonard <sleonard76@gmail.com>. All rights reserved.
Use of this source code is governed by the MIT license that can be found
in the LICENSE file.
*/

//Package merkle is a fixed merkle tree implementation
package merkle

import (
	"bytes"
	"errors"
	"hash"
)

// TreeOptions configures tree behavior
type TreeOptions struct {
	// EnableHashSorting modifies the tree's hash behavior to sort the hashes before concatenating them
	// to calculate the parent hash. This removes the capability of proving the position in the tree but
	// simplifies the proof format by removing the need to specify left/right.
	EnableHashSorting bool

	// DisableHashLeaves determines whether leaf nodes should be hashed or not. By doing disabling this behavior,
	// you can use a different hash function for leaves or generate a tree that contains already hashed
	// values. If this is disabled, a length of 32 bytes is enforced for all leaves.
	DisableHashLeaves bool

	// DoubleOddNodes repeats trailing nodes so they become their own
	// left/right pair and are hashed together. The default behavior is to
	// use a null hash as the missing pair.
	DoubleOddNodes bool
}

// Node in the merkle tree
type Node struct {
	Hash  []byte
	Left  *Node
	Right *Node
}

// NewNode creates a node given a hash function and data to hash. If the hash function is nil, the data
// will be added without being hashed.
func NewNode(h hash.Hash, block []byte) (Node, error) {
	if h == nil {
		return Node{
			Hash: block,
		}, nil
	}
	if block == nil {
		return Node{}, nil
	}
	defer h.Reset()
	_, err := h.Write(block[:])
	if err != nil {
		return Node{}, err
	}
	return Node{Hash: h.Sum(nil)}, nil
}

// Tree contains all nodes
type Tree struct {
	// All nodes, linear
	Nodes []Node
	// Points to each level in the node. The first level contains the root node
	Levels [][]Node
	// Any particular behavior changing option
	Options TreeOptions
}

// NewTreeWithOpts creates a Tree with custom options
func NewTreeWithOpts(options TreeOptions) Tree {
	tree := NewTree()
	tree.Options = options
	return tree
}

// NewTree creates a Tree with the default options
func NewTree() Tree {
	return Tree{
		Nodes:  nil,
		Levels: nil,
	}
}

// Leaves returns a slice of the leaf nodes in the tree, if available, else nil
func (tree *Tree) Leaves() []Node {
	if tree.Levels == nil {
		return nil
	}

	return tree.Levels[len(tree.Levels)-1]
}

// Root returns the root node of the tree, if available, else nil
func (tree *Tree) Root() *Node {
	if tree.Nodes == nil {
		return nil
	}

	return &tree.Levels[0][0]
}

// GetNodesAtHeight returns all nodes at a given height, where height 1 returns a 1-element
// slice containing the root node, and a height of tree.Height() returns
// the leaves
func (tree *Tree) GetNodesAtHeight(h uint64) []Node {
	if tree.Levels == nil || h == 0 || h > uint64(len(tree.Levels)) {
		return nil
	}

	return tree.Levels[h-1]
}

// Height returns the height of this tree
func (tree *Tree) Height() uint64 {
	return uint64(len(tree.Levels))
}

// Generate generates the tree nodes
func (tree *Tree) Generate(blocks [][]byte, hashf hash.Hash) error {
	blockCount := uint64(len(blocks))
	if blockCount == 0 {
		return errors.New("Empty tree")
	}
	height, nodeCount := CalculateHeightAndNodeCount(blockCount)
	levels := make([][]Node, height)
	nodes := make([]Node, nodeCount)

	// Create the leaf nodes
	for i, block := range blocks {
		var node Node
		var err error
		if tree.Options.DisableHashLeaves {
			node, err = NewNode(nil, block)
		} else {
			node, err = NewNode(hashf, block)
		}
		if err != nil {
			return err
		}
		nodes[i] = node
	}
	levels[height-1] = nodes[:len(blocks)]

	// Create each node level
	current := nodes[len(blocks):]
	h := height - 1
	for ; h > 0; h-- {
		below := levels[h]
		wrote, err := tree.generateNodeLevel(below, current, hashf)
		if err != nil {
			return err
		}
		levels[h-1] = current[:wrote]
		current = current[wrote:]
	}

	tree.Nodes = nodes
	tree.Levels = levels
	return nil
}

// Creates all the non-leaf nodes for a certain height. The number of nodes
// is calculated to be 1/2 the number of nodes in the lower rung.  The newly
// created nodes will reference their Left and Right children.
// Returns the number of nodes added to current
func (tree *Tree) generateNodeLevel(below []Node, current []Node, h hash.Hash) (uint64, error) {
	h.Reset()

	end := (len(below) + (len(below) % 2)) / 2
	for i := 0; i < end; i++ {
		// Concatenate the two children hashes and hash them, if both are
		// available, otherwise reuse the hash from the lone left node
		ileft := 2 * i
		iright := 2*i + 1
		left := &below[ileft]
		var right *Node
		var rightHash []byte
		if len(below) > iright {
			right = &below[iright]
			rightHash = right.Hash
		}
		node, err := tree.generateNode(below[ileft].Hash, rightHash, h)
		if err != nil {
			return 0, err
		}
		// Point the new node to its children and save
		node.Left = left
		node.Right = right
		current[i] = node

	}
	return uint64(end), nil
}

func (tree *Tree) generateNode(left, right []byte, h hash.Hash) (Node, error) {
	data := make([]byte, h.Size()*2)
	if right == nil {
		if !tree.Options.DoubleOddNodes {
			b := data[:h.Size()]
			copy(b, left)
			return Node{Hash: b}, nil
		}
		right = left
	}
	firstHalf := left
	secondHalf := right
	if tree.Options.EnableHashSorting && bytes.Compare(left, right) > 0 {
		firstHalf = right
		secondHalf = left
	}
	copy(data[:h.Size()], firstHalf)
	copy(data[h.Size():], secondHalf)

	return NewNode(h, data)
}

// CalculateHeightAndNodeCount returns the height and number of nodes in an unbalanced binary tree given
// number of leaves
func CalculateHeightAndNodeCount(leaves uint64) (height, nodeCount uint64) {
	height = calculateTreeHeight(leaves)
	nodeCount = calculateNodeCount(height, leaves)
	return
}

// Calculates the number of nodes in a binary tree unbalanced strictly on
// the right side.  Height is assumed to be equal to
// calculateTreeHeight(size)
func calculateNodeCount(height, size uint64) uint64 {
	if isPowerOfTwo(size) {
		return 2*size - 1
	}
	count := size
	prev := size
	i := uint64(1)
	for ; i < height; i++ {
		next := (prev + (prev % 2)) / 2
		count += next
		prev = next
	}
	return count
}

// Returns the height of a full, complete binary tree given nodeCount nodes
func calculateTreeHeight(nodeCount uint64) uint64 {
	if nodeCount == 0 {
		return 0
	}

	return logBaseTwo(nextPowerOfTwo(nodeCount)) + 1
}

// Returns true if n is a power of 2
func isPowerOfTwo(n uint64) bool {
	// http://graphics.stanford.edu/~seander/bithacks.html#DetermineIfPowerOf2
	return n != 0 && (n&(n-1)) == 0
}

// Returns the next highest power of 2 above n, if n is not already a
// power of 2
func nextPowerOfTwo(n uint64) uint64 {
	if n == 0 {
		return 1
	}
	// http://graphics.stanford.edu/~seander/bithacks.html#RoundUpPowerOf2
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	n++
	return n
}

// Lookup table for integer log2 implementation
var log2lookup = []uint64{
	0xFFFFFFFF00000000,
	0x00000000FFFF0000,
	0x000000000000FF00,
	0x00000000000000F0,
	0x000000000000000C,
	0x0000000000000002,
}

// Returns log2(n) assuming n is a power of 2
func logBaseTwo(x uint64) uint64 {
	if x == 0 {
		return 0
	}
	ct := uint64(0)
	for x != 0 {
		x >>= 1
		ct++
	}
	return ct - 1
}

// Returns the ceil'd log2 value of n
// See: http://stackoverflow.com/a/15327567
func ceilLogBaseTwo(x uint64) uint64 {
	y := uint64(1)
	if (x & (x - 1)) == 0 {
		y = 0
	}
	j := uint64(32)
	i := uint64(0)

	for ; i < 6; i++ {
		k := j
		if (x & log2lookup[i]) == 0 {
			k = 0
		}
		y += k
		x >>= k
		j >>= 1
	}

	return y
}
