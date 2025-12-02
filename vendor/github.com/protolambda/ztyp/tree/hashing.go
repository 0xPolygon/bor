package tree

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"github.com/protolambda/ztyp/bitfields"
)

type HTR interface {
	HashTreeRoot(h HashFn) Root
}

type SeriesHTR func(i uint64) HTR

type ChunksHTR func(i uint64) Root

type HashFn func(a Root, b Root) Root

// HashTreeRoot is a small utility function to implement HTR for custom compound types easily.
// E.g. for a struct `x` with 3 fields, call hFn.HashTreeRoot(x.A, x.B, x.C)
func (h HashFn) HashTreeRoot(fields ...HTR) Root {
	// TODO; benchmark, may be worth hard-coding a few more common short-paths
	n := uint64(len(fields))
	switch n {
	case 0:
		return Root{}
	case 1:
		return fields[0].HashTreeRoot(h)
	case 2:
		return h(fields[0].HashTreeRoot(h), fields[1].HashTreeRoot(h))
	default:
		return Merkleize(h, uint64(len(fields)), uint64(len(fields)), func(i uint64) Root {
			return fields[i].HashTreeRoot(h)
		})
	}
}

func (h HashFn) ComplexVectorHTR(series SeriesHTR, length uint64) Root {
	// length is alos limit for vectors
	return Merkleize(h, length, length, func(i uint64) Root {
		htr := series(i)
		if htr == nil { // missing element? Fine, just like an empty node then
			return Root{}
		}
		return htr.HashTreeRoot(h)
	})
}

func (h HashFn) ComplexListHTR(series SeriesHTR, length uint64, limit uint64) Root {
	return h.Mixin(Merkleize(h, length, limit, func(i uint64) Root {
		htr := series(i)
		if htr == nil { // missing element? Fine, just like an empty node then
			return Root{}
		}
		return htr.HashTreeRoot(h)
	}), length)
}

func (h HashFn) Mixin(v Root, length uint64) Root {
	var mixin Root
	binary.LittleEndian.PutUint64(mixin[:], length)
	return h(v, mixin)
}

// ChunksHTR is like SeriesHTR, except that the items are chunked by the input,
// and chunks are merely merkleized to get the hash-tree-root.
// No length mixin is performed (required for a list/basic-list/bitlist hash-tree-root).
func (h HashFn) ChunksHTR(chunks ChunksHTR, length uint64, limit uint64) Root {
	return Merkleize(h, length, limit, chunks)
}

func (h HashFn) Uint8VectorHTR(v func(i uint64) uint8, length uint64) Root {
	// 32 items per chunk
	chunks := (length + 31) >> 5
	return h.ChunksHTR(func(i uint64) (out Root) {
		for x, j := 0, i<<5; x < 32 && j < length; j, x = j+1, x+1 {
			out[x] = v(j)
		}
		return
	}, chunks, chunks)
}

func (h HashFn) Uint8ListHTR(v func(i uint64) uint8, length uint64, limit uint64) Root {
	// 32 items per chunk
	chunks := (length + 31) >> 5
	return h.Mixin(h.ChunksHTR(func(i uint64) (out Root) {
		for x, j := 0, i<<5; x < 32 && j < length; j, x = j+1, x+1 {
			out[x] = v(j)
		}
		return
	}, chunks, (limit+31)>>5), length)
}

func (h HashFn) Uint64VectorHTR(v func(i uint64) uint64, length uint64) Root {
	// 4 items per chunk
	chunks := (length + 3) >> 2
	return h.ChunksHTR(func(i uint64) (out Root) {
		for x, j := 0, i<<2; x < 32 && j < length; j, x = j+1, x+8 {
			binary.LittleEndian.PutUint64(out[x:], v(j))
		}
		return
	}, chunks, chunks)
}

func (h HashFn) Uint64ListHTR(v func(i uint64) uint64, length uint64, limit uint64) Root {
	// 4 items per chunk
	chunks := (length + 3) >> 2
	return h.Mixin(h.ChunksHTR(func(i uint64) (out Root) {
		for x, j := 0, i<<2; x < 32 && j < length; j, x = j+1, x+8 {
			binary.LittleEndian.PutUint64(out[x:], v(j))
		}
		return
	}, chunks, (limit+3)>>2), length)
}

func (h HashFn) ByteVectorHTR(values []byte) Root {
	chunks := (uint64(len(values)) + 31) / 32
	return h.ChunksHTR(func(i uint64) (out Root) {
		copy(out[:], values[i<<5:])
		return
	}, chunks, chunks)
}

func (h HashFn) ByteListHTR(values []byte, limit uint64) Root {
	chunks := (uint64(len(values)) + 31) / 32
	chunkLimit := (limit + 31) / 32
	return h.Mixin(h.ChunksHTR(func(i uint64) (out Root) {
		copy(out[:], values[i<<5:])
		return
	}, chunks, chunkLimit), uint64(len(values)))
}

func (h HashFn) BitVectorHTR(bits []byte) Root {
	// it's a vector, chunks is also chunkLimit.
	// The bits are already packed in bytes, just divide by 32 (rounding up).
	chunks := (uint64(len(bits)) + 31) / 32
	return h.ChunksHTR(func(i uint64) (out Root) {
		if i < chunks {
			copy(out[:], bits[i<<5:])
			// no delimiter bits in bit vectors
		}
		return
	}, chunks, chunks)
}

func (h HashFn) BitListHTR(bits []byte, bitlimit uint64) Root {
	bitLen := bitfields.BitlistLen(bits)
	chunks := (bitLen + 0xff) >> 8
	chunkLimit := (bitlimit + 0xff) >> 8
	return h.Mixin(h.ChunksHTR(func(i uint64) (out Root) {
		if i < chunks {
			copy(out[:], bits[i<<5:])
			// mask out delimit bit if necessary
			if ((i + 1) << 8) > bitLen {
				out[(bitLen&0xff)>>3] &^= 1 << (bitLen & 0x7)
			}
		}
		return
	}, chunks, chunkLimit), bitLen)
}

func (h HashFn) Union(selector uint8, value HTR) Root {
	var selectorNode Root
	selectorNode[0] = selector
	if value == nil {
		return h(Root{}, selectorNode)
	} else {
		return h(value.HashTreeRoot(h), selectorNode)
	}
}

func sha256Combi(a Root, b Root) Root {
	v := [64]byte{}
	copy(v[:32], a[:])
	copy(v[32:], b[:])
	return sha256.Sum256(v[:])
}

func sha256CombiRepeat() HashFn {
	hash := sha256.New()
	v := [64]byte{}
	hashFn := func(a Root, b Root) (out Root) {
		hash.Reset()
		copy(v[:32], a[:])
		copy(v[32:], b[:])
		hash.Write(v[:])
		copy(out[:], hash.Sum(nil))
		return
	}
	return hashFn
}

type NewHashFn func() HashFn

// Get a hash-function that re-uses the hashing working-variables. Defaults to SHA-256.
var GetHashFn NewHashFn = sha256CombiRepeat

var Hash HashFn = sha256Combi

var ZeroHashes []Root

// initialize the zero-hashes pre-computed data with the given hash-function.
func InitZeroHashes(h HashFn, zeroHashesLevels uint) {
	ZeroHashes = make([]Root, zeroHashesLevels+1)
	for i := uint(0); i < zeroHashesLevels; i++ {
		ZeroHashes[i+1] = h(ZeroHashes[i], ZeroHashes[i])
	}
}

func init() {
	InitZeroHashes(sha256Combi, 64)
}

func ZeroNode(depth uint32) Node {
	if depth >= uint32(len(ZeroHashes)) {
		panic(fmt.Errorf("depth %d reaches deeper than available %d precomputed zero-hashes provide", depth, len(ZeroHashes)))
	}
	return &ZeroHashes[depth]
}
