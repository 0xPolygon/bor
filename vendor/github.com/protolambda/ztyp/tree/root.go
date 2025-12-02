package tree

import (
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/conv"
)

type Root [32]byte

func (r Root) MarshalText() ([]byte, error) {
	return conv.BytesMarshalText(r[:])
}

func (r Root) String() string {
	return "0x" + hex.EncodeToString(r[:])
}

func (r *Root) Deserialize(dr *codec.DecodingReader) error {
	if r == nil {
		return errors.New("nil root")
	}
	_, err := dr.Read(r[:])
	return err
}

func (r Root) Serialize(w *codec.EncodingWriter) error {
	return w.Write(r[:])
}

func (Root) ByteLength() uint64 {
	return 32
}

func (Root) ValueByteLength() (uint64, error) {
	return 32, nil
}

func (r Root) HashTreeRoot(_ HashFn) Root {
	return r
}

func (r *Root) UnmarshalText(text []byte) error {
	return conv.FixedBytesUnmarshalText(r[:], text)
}

func (r *Root) Left() (Node, error) {
	return nil, NavigationError
}

func (r *Root) Right() (Node, error) {
	return nil, NavigationError
}

func (r *Root) IsLeaf() bool {
	return true
}

func (r *Root) RebindLeft(v Node) (Node, error) {
	return nil, NavigationError
}

func (r *Root) RebindRight(v Node) (Node, error) {
	return nil, NavigationError
}

func (r *Root) Getter(target Gindex) (Node, error) {
	if target.IsRoot() {
		return r, nil
	} else {
		return nil, NavigationError
	}
}

func (r *Root) Setter(target Gindex, expand bool) (Link, error) {
	if target.IsRoot() {
		return Identity, nil
	}
	if expand {
		child := ZeroNode(target.Depth())
		p := NewPairNode(child, child)
		return p.Setter(target, expand)
	} else {
		return nil, NavigationError
	}
}

func (Root) FixedLength() uint64 {
	return 32
}

func (r *Root) SummarizeInto(target Gindex, h HashFn) (SummaryLink, error) {
	if target.IsRoot() {
		return func() (Node, error) {
			return r, nil
		}, nil
	} else {
		return nil, NavigationError
	}
}

func (r *Root) MerkleRoot(h HashFn) Root {
	if r == nil {
		return Root{}
	}
	return *r
}

func ReadRoots(dr *codec.DecodingReader, roots *[]Root, length uint64) error {
	if uint64(len(*roots)) != length {
		// re-use space if available (for recycling old state objects)
		if uint64(cap(*roots)) >= length {
			*roots = (*roots)[:length]
		} else {
			*roots = make([]Root, length, length)
		}
	}
	dst := *roots
	for i := uint64(0); i < length; i++ {
		if _, err := dr.Read(dst[i][:]); err != nil {
			return err
		}
	}
	return nil
}

func ReadRootsLimited(dr *codec.DecodingReader, roots *[]Root, limit uint64) error {
	scope := dr.Scope()
	if scope%32 != 0 {
		return fmt.Errorf("bad deserialization scope, cannot decode roots list")
	}
	length := scope / 32
	if length > limit {
		return fmt.Errorf("too many roots: %d > %d", length, limit)
	}
	return ReadRoots(dr, roots, length)
}

// WriteRoots serialization, efficient version of List/Vector serialization
func WriteRoots(ew *codec.EncodingWriter, roots []Root) error {
	for i := range roots {
		if err := ew.Write(roots[i][:]); err != nil {
			return err
		}
	}
	return nil
}
