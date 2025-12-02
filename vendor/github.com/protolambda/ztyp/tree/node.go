package tree

import "errors"

// A link is called to rebind a value, and retrieve the new root node.
type Link func(v Node) (Node, error)

func Identity(v Node) (Node, error) {
	return v, nil
}

func (outer Link) Wrap(inner Link) Link {
	return func(v Node) (Node, error) {
		res, err := inner(v)
		if err != nil {
			return nil, err
		}
		return outer(res)
	}
}

type SummaryLink func() (Node, error)

// Node of a binary merkle tree
type Node interface {
	Left() (Node, error)
	Right() (Node, error)
	IsLeaf() bool
	RebindLeft(v Node) (Node, error)
	RebindRight(v Node) (Node, error)
	Getter(target Gindex) (Node, error)
	Setter(target Gindex, expand bool) (Link, error)
	SummarizeInto(target Gindex, h HashFn) (SummaryLink, error)
	MerkleRoot(h HashFn) Root
}

// SummaryInto creates a SummaryLink that collapses the subtree at the target Gindex into just the root
func SummaryInto(n Node, target Gindex, h HashFn) (SummaryLink, error) {
	setter, err := n.Setter(target, false)
	if err != nil {
		return nil, err
	}
	node, err := n.Getter(target)
	if err != nil {
		return nil, err
	}
	return func() (Node, error) {
		root := node.MerkleRoot(h)
		return setter(&root)
	}, nil
}

var NavigationError = errors.New("navigation error")
