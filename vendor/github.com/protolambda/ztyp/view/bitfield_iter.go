package view

import (
	"fmt"
	. "github.com/protolambda/ztyp/tree"
)

type ErrBitIter struct {
	error
}

func (e ErrBitIter) Next() (elem bool, ok bool, err error) {
	return false, false, e.error
}

type BitIterFn func() (elem bool, ok bool, err error)

func (f BitIterFn) Next() (elem bool, ok bool, err error) {
	return f()
}

type BitIter interface {
	// Next gets the next element, ok is true if it actually exists.
	// An error may occur if data is missing or corrupt.
	Next() (elem bool, ok bool, err error)
}

func bitReadonlyIter(anchor Node, length uint64, depth uint8) BitIter {
	if limit := (uint64(1) << depth) << 8; limit < length {
		return ErrBitIter{fmt.Errorf("cannot handle iterate length %d bits in subtree of depth %d deep (limit %d bits)", length, depth, limit)}
	}
	stack := make([]Node, depth, depth)

	i := uint64(0)
	// max 256 per node. 0 = no action.
	j := uint8(i)
	var currentRoot Root
	rootIndex := uint64(0)
	return BitIterFn(func() (elem bool, ok bool, err error) {
		// done yet?
		if i >= length {
			return false, false, nil
		}
		// in the middle of a node currently? finish that first
		if j > 0 {
			elByte := currentRoot[j>>3]
			elem = ((elByte >> (j & 7)) & 1) == 1
			// overflow is a feature here: no more than 256 bits
			// (255 here, 1 at index 0 at the entry of the bottom node)
			j += 1
			i += 1
			return elem, true, nil
		}
		var node Node
		stackIndex := uint8(0)
		if rootIndex != 0 {
			// XOR current index with previous index
			// Result: highest bit matches amount we have to backtrack up the stack
			s := rootIndex ^ (rootIndex - 1)
			stackIndex = depth
			for s != 0 {
				s >>= 1
				stackIndex -= 1
			}
			// then move to the right from that upper previously remembered left-hand node
			node = stack[stackIndex]
			node, err = node.Right()
			if err != nil {
				return false, false, err
			}
			stackIndex += 1
		} else {
			node = anchor
		}
		// and move down left into this new subtree
		for ; stackIndex < depth; stackIndex++ {
			// remember left-hand nodes, we may revisit them
			stack[stackIndex] = node

			node, err = node.Left()
			if err != nil {
				return false, false, err
			}
		}

		// Get leaf node as a root
		r, leafIsRoot := node.(*Root)
		if !leafIsRoot {
			return false, false, fmt.Errorf("expected leaf node %d to be a Root type", i)
		}
		// remember the root, we need it for more bits
		currentRoot = *r

		// get the first bit
		el := currentRoot[0]&1 == 1
		// indicate that we have done one bit, and need to read more
		j = 1
		// And that the next root will be 1 after
		rootIndex += 1
		// And we progress the general element counter
		i += 1

		// Return the actual element
		return el, true, nil
	})
}

func bitsToBytes(bits []bool) []byte {
	byteLen := (len(bits) + 7) / 8
	out := make([]byte, byteLen, byteLen)
	for i, b := range bits {
		if b {
			out[i>>3] |= 1 << (i & 0b111)
		}
	}
	return out
}
