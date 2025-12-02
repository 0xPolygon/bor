package tree

import (
	"encoding/binary"
	"fmt"
)

type Gindex interface {
	// Subtree returns the same gindex, but with the anchor moved one bit to the right, to represent the subtree position.
	Subtree() Gindex
	// Anchor of the gindex: same depth, but with position zeroed out.
	Anchor() Gindex
	// Left child gindex
	Left() Gindex
	// Right child gindex
	Right() Gindex
	// Parent gindex
	Parent() Gindex
	// If the gindex points into the left subtree (2nd bit is 0)
	IsLeft() bool
	// If the gindex is the root (= 1)
	IsRoot() bool
	// If gindex is 2 or 3
	IsClose() bool
	// Get the depth of the gindex
	Depth() uint32
	// Iterate over the bits of the gindex
	// The depth is excl. the "root" bit
	BitIter() (iter GindexBitIter, depth uint32)
	// Encode the gindex as little-endian byte array. An invalid gindex (<= 0) must return nil.
	LittleEndian() []byte
	// Encode the gindex as big-endian byte array. An invalid gindex (<= 0) must return nil.
	BigEndian() []byte
	// LeftAlignedBigEndian returns the bits shifted such that the anchor bit is the left-most
	LeftAlignedBigEndian() (data []byte, bitLen uint32)
}

type GindexBitIter interface {
	// Next: move forward through gindex.
	// It returns bools for each bit after the "root" bit.
	// If "ok" is false, the bit cannot be read, none are remaining.
	// Subsequent calls are invalid.
	// For these extra calls, "ok" will be false, and "right" is undefined.
	Next() (right bool, ok bool)
}

// TODO: implement big int based gindex to automatically switch to whenever the uint64 is too small

type Gindex64 uint64

const RootGindex Gindex64 = 1
const LeftGindex Gindex64 = 2
const RightGindex Gindex64 = 3

func (v Gindex64) Subtree() Gindex {
	anchor := Gindex64(1 << BitIndex(uint64(v)))
	return v ^ anchor | (anchor >> 1)
}

func (v Gindex64) Anchor() Gindex {
	return Gindex64(1 << BitIndex(uint64(v)))
}

func (v Gindex64) Left() Gindex {
	return v << 1
}

func (v Gindex64) Right() Gindex {
	return v<<1 | 1
}

func (v Gindex64) Parent() Gindex {
	return v >> 1
}

func (v Gindex64) IsLeft() bool {
	pivot := Gindex64(1<<BitIndex(uint64(v))) >> 1
	return v&pivot == 0
}

func (v Gindex64) IsRoot() bool {
	return v == 1
}

func (v Gindex64) IsClose() bool {
	return v <= 3
}

func (v Gindex64) Depth() uint32 {
	return uint32(BitIndex(uint64(v)))
}

func (v Gindex64) BitIter() (iter GindexBitIter, depth uint32) {
	d := BitIndex(uint64(v))
	return &Gindex64BitIter{
		Marker: 1 << d,
		Gindex: uint64(v),
	}, uint32(d)
}

func (v Gindex64) LittleEndian() []byte {
	if v == 0 {
		return nil
	}
	var out [8]byte
	binary.LittleEndian.PutUint64(out[:], uint64(v))
	s := 1
	if v >= 1<<32 {
		v >>= 32
		s += 4
	}
	if v >= 1<<16 {
		v >>= 16
		s += 2
	}
	if v >= 1<<8 {
		v >>= 8
		s += 1
	}
	return out[:s]
}

func (v Gindex64) BigEndian() []byte {
	if v == 0 {
		return nil
	}
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], uint64(v))
	s := 7
	if v >= 1<<32 {
		v >>= 32
		s -= 4
	}
	if v >= 1<<16 {
		v >>= 16
		s -= 2
	}
	if v >= 1<<8 {
		v >>= 8
		s -= 1
	}
	return out[s:]
}

func (v Gindex64) LeftAlignedBigEndian() (data []byte, bitLen uint32) {
	if v == 0 {
		return nil, 0
	}
	bitLen8 := BitLength(uint64(v))
	leftAligned := uint64(v) << (64 - bitLen8)
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], leftAligned)
	return out[:(bitLen8+7)>>3], uint32(bitLen8)
}

func ToGindex64(index uint64, depth uint8) (Gindex64, error) {
	if depth >= 64 {
		return 0, fmt.Errorf("depth %d is too deep for Gindex64", depth)
	}
	anchor := uint64(1) << depth
	if index >= anchor {
		return 0, fmt.Errorf("index %d is larger than anchor %d derived from depth %d", index, anchor, depth)
	}
	return Gindex64(anchor | index), nil
}

type Gindex64BitIter struct {
	Marker uint64
	Gindex uint64
}

func (iter *Gindex64BitIter) Next() (right bool, ok bool) {
	iter.Marker >>= 1
	return iter.Gindex&iter.Marker != 0, iter.Marker != 0
}
