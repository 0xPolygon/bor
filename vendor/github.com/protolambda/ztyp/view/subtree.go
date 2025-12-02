package view

import (
	"errors"
	. "github.com/protolambda/ztyp/tree"
)

type SubtreeView struct {
	BackedView
	depth uint8
}

func (stv *SubtreeView) GetNode(i uint64) (Node, error) {
	g, err := ToGindex64(i, stv.depth)
	if err != nil {
		return nil, err
	}
	return stv.BackingNode.Getter(g)
}

func (stv *SubtreeView) SetNode(i uint64, node Node) error {
	g, err := ToGindex64(i, stv.depth)
	if err != nil {
		return err
	}
	s, err := stv.BackingNode.Setter(g, false)
	if err != nil {
		return err
	}
	newBacking, err := s(node)
	if err != nil {
		return err
	}
	return stv.SetBacking(newBacking)
}

// Copy over the roots at the bottom of the subtree from left to right into dest (until dest is full)
func SubtreeIntoBytes(anchor Node, depth uint8, length uint64, dest []byte) error {
	copyChunk := func(node Node, dest []byte) error {
		r, ok := node.(*Root)
		if !ok {
			return errors.New("bottom node is not a root")
		}
		// limits to smallest part: cap at 32 (the root), and dest (may be smaller than 32)
		copy(dest, r[:])
		return nil
	}
	iter := nodeReadonlyIter(anchor, length, depth)
	for i := uint64(0); i < length; i++ {
		node, ok, err := iter.Next()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("unexpected early iter end")
		}
		if err := copyChunk(node, dest[i<<5:]); err != nil {
			return err
		}
	}
	return nil
}

func BytesIntoNodes(contents []byte) ([]Node, error) {
	chunkCount := (len(contents) + 31) / 32
	if chunkCount == 0 {
		return nil, nil
	}
	chunks := make([]Node, chunkCount, chunkCount)
	offset := 0
	for i := 0; i < chunkCount; i++ {
		root := &Root{}
		// copy as much as possible
		// (root is limited to 32 bytes, contents slice may be smaller than 32 at the end)
		copy(root[:], contents[offset:])
		chunks[i] = root
		offset += 32
	}
	return chunks, nil
}
