package tree

import (
	"errors"
)

// An immutable (L, R) pair with a link to the holding node.
// If L or R changes, the link is used to bind a new (L, *R) or (*L, R) pair in the holding value.
type PairNode struct {
	Value      Root
	LeftChild  Node
	RightChild Node
}

func NewPairNode(a Node, b Node) *PairNode {
	return &PairNode{LeftChild: a, RightChild: b}
}

func (c *PairNode) Left() (Node, error) {
	return c.LeftChild, nil
}

func (c *PairNode) Right() (Node, error) {
	return c.RightChild, nil
}

func (c *PairNode) IsLeaf() bool {
	return false
}

func (c *PairNode) RebindLeft(v Node) (Node, error) {
	return NewPairNode(v, c.RightChild), nil
}

func (c *PairNode) RebindRight(v Node) (Node, error) {
	return NewPairNode(c.LeftChild, v), nil
}

func (c *PairNode) Getter(target Gindex) (Node, error) {
	if target.IsRoot() {
		return c, nil
	}
	if target.IsClose() {
		if target.IsLeft() {
			return c.LeftChild, nil
		} else {
			return c.RightChild, nil
		}
	}
	iter, _ := target.BitIter()
	var node Node = c
	var err error
	for {
		right, ok := iter.Next()
		if !ok {
			break
		}
		if right {
			node, err = node.Right()
		} else {
			node, err = node.Left()
		}
		if err != nil {
			return nil, err
		}
	}
	return node, nil
}

func (c *PairNode) Setter(target Gindex, expand bool) (Link, error) {
	if target.IsRoot() {
		return Identity, nil
	}
	if target.IsClose() {
		if target.IsLeft() {
			return c.RebindLeft, nil
		} else {
			return c.RebindRight, nil
		}
	}
	if target.IsLeft() {
		return DeeperSetter(c.RebindLeft, c.LeftChild, target, expand)
	} else {
		return DeeperSetter(c.RebindRight, c.RightChild, target, expand)
	}
}

// target.IsRoot() and target.IsClose() must both be false.
// The link and node args are the first step into the path, i.e. at depth 1, not the anchor node.
func DeeperSetter(link Link, node Node, target Gindex, expand bool) (Link, error) {
	iter, depth := target.BitIter()
	if depth < 2 {
		panic("expected depth to be at least 2 since IsClose() is false")
	}
	// ignore first "ok": we already know we are not "close" (one bit child)
	// This is the first child bit: gindex 2 vs 3
	_, _ = iter.Next()

	depth -= 1
	// now on to the second child bit: gindex 4/6 vs 5/7
	var err error
	for {
		right, ok := iter.Next()
		if !ok {
			break
		}
		depth -= 1
		if node.IsLeaf() {
			if !expand {
				return nil, NavigationError
			}
			child := ZeroNode(depth)
			node = NewPairNode(child, child)
		}
		if right {
			link = link.Wrap(node.RebindRight)
			node, err = node.Right()
		} else {
			link = link.Wrap(node.RebindLeft)
			node, err = node.Left()
		}
		if err != nil {
			return nil, err
		}
	}
	if depth != 0 {
		panic("expected 0 depth at end of setter depth traversal")
	}
	return link, nil
}

func (c *PairNode) SummarizeInto(target Gindex, h HashFn) (SummaryLink, error) {
	return SummaryInto(c, target, h)
}

func (c *PairNode) MerkleRoot(h HashFn) Root {
	if c.Value != (Root{}) {
		return c.Value
	}
	if c.LeftChild == nil || c.RightChild == nil {
		panic("invalid state, cannot have left without right")
	}
	c.Value = h(c.LeftChild.MerkleRoot(h), c.RightChild.MerkleRoot(h))
	return c.Value
}

func SubtreeFillToDepth(bottom Node, depth uint8) Node {
	node := bottom
	for i := uint64(0); i < uint64(depth); i++ {
		node = NewPairNode(node, node)
	}
	return node
}

func SubtreeFillToLength(bottom Node, depth uint8, length uint64) (Node, error) {
	anchor := uint64(1) << depth
	if length > anchor {
		return nil, errors.New("too many nodes")
	}
	if length == anchor {
		return SubtreeFillToDepth(bottom, depth), nil
	}
	if depth == 1 {
		if length > 1 {
			return NewPairNode(bottom, bottom), nil
		} else {
			return NewPairNode(bottom, &ZeroHashes[0]), nil
		}
	}
	pivot := anchor >> 1
	if length <= pivot {
		left, err := SubtreeFillToLength(bottom, depth-1, length)
		if err != nil {
			return nil, err
		}
		return NewPairNode(left, &ZeroHashes[depth-1]), nil
	} else {
		left := SubtreeFillToDepth(bottom, depth-1)
		right, err := SubtreeFillToLength(bottom, depth-1, length-pivot)
		if err != nil {
			return nil, err
		}
		return NewPairNode(left, right), nil
	}
}

func SubtreeFillToContents(nodes []Node, depth uint8) (Node, error) {
	if len(nodes) == 0 {
		return nil, errors.New("no nodes to fill subtree with")
	}
	anchor := uint64(1) << depth
	if uint64(len(nodes)) > anchor {
		return nil, errors.New("too many nodes")
	}
	if depth == 0 {
		return nodes[0], nil
	}
	if depth == 1 {
		if len(nodes) > 1 {
			return NewPairNode(nodes[0], nodes[1]), nil
		} else {
			return NewPairNode(nodes[0], &ZeroHashes[0]), nil
		}
	}
	pivot := anchor >> 1
	if uint64(len(nodes)) <= pivot {
		left, err := SubtreeFillToContents(nodes, depth-1)
		if err != nil {
			return nil, err
		}
		return NewPairNode(left, &ZeroHashes[depth-1]), nil
	} else {
		left, err := SubtreeFillToContents(nodes[:pivot], depth-1)
		if err != nil {
			return nil, err
		}
		right, err := SubtreeFillToContents(nodes[pivot:], depth-1)
		if err != nil {
			return nil, err
		}
		return NewPairNode(left, right), nil
	}
}
