package view

import (
	"encoding/binary"
	"fmt"
	"github.com/protolambda/ztyp/codec"
	. "github.com/protolambda/ztyp/tree"
)

type BitListTypeDef struct {
	BitLimit uint64
	ComplexTypeBase
}

func BitListType(limit uint64) *BitListTypeDef {
	return &BitListTypeDef{
		BitLimit: limit,
		ComplexTypeBase: ComplexTypeBase{
			MinSize:     1, // 1 byte, to do the 1 delimiting bit
			MaxSize:     (limit + 7 + 1) / 8,
			Size:        0,
			IsFixedSize: false,
		},
	}
}

func (td *BitListTypeDef) FromBits(bits []bool) (*BitListView, error) {
	if uint64(len(bits)) > td.BitLimit {
		return nil, fmt.Errorf("got %d bits, expected no more than %d bits", len(bits), td.BitLimit)
	}
	contents := bitsToBytes(bits)
	bottomNodes, err := BytesIntoNodes(contents)
	if err != nil {
		return nil, err
	}
	depth := CoverDepth(td.BottomNodeLimit())
	contentsRootNode, _ := SubtreeFillToContents(bottomNodes, depth)
	rootNode := &PairNode{LeftChild: contentsRootNode, RightChild: Uint64View(len(bits)).Backing()}
	view, _ := td.ViewFromBacking(rootNode, nil)
	return view.(*BitListView), nil
}

func (td *BitListTypeDef) Limit() uint64 {
	return td.BitLimit
}

func (td *BitListTypeDef) DefaultNode() Node {
	depth := CoverDepth(td.BottomNodeLimit())
	return &PairNode{LeftChild: &ZeroHashes[depth], RightChild: &ZeroHashes[0]}
}

func (td *BitListTypeDef) ViewFromBacking(node Node, hook BackingHook) (View, error) {
	depth := CoverDepth(td.BottomNodeLimit())
	return &BitListView{
		SubtreeView: SubtreeView{
			BackedView: BackedView{
				ViewBase: ViewBase{
					TypeDef: td,
				},
				Hook:        hook,
				BackingNode: node,
			},
			depth: depth + 1, // +1 for length mix-in
		},
		BitListTypeDef: td,
	}, nil
}

func (td *BitListTypeDef) BottomNodeLimit() uint64 {
	return (td.BitLimit + 0xff) >> 8
}

func (td *BitListTypeDef) Default(hook BackingHook) View {
	v, _ := td.ViewFromBacking(td.DefaultNode(), hook)
	return v
}

func (td *BitListTypeDef) New() *BitListView {
	return td.Default(nil).(*BitListView)
}

func (td *BitListTypeDef) Deserialize(dr *codec.DecodingReader) (View, error) {
	scope := dr.Scope()
	if scope == 0 {
		return nil, fmt.Errorf("expected at least a delimit bit, bitlist scope cannot be 0")
	}
	if scope > td.MaxSize {
		return nil, fmt.Errorf("bitlist has too many bytes, bitlimit %d (byte size %d) but got scope %d", td.BitLimit, td.MaxSize, scope)
	}
	contents := make([]byte, scope, scope)
	if _, err := dr.Read(contents); err != nil {
		return nil, err
	}
	lastByte := contents[scope-1]
	if lastByte == 0 {
		return nil, fmt.Errorf("bitlist last byte must not be zero, delimit bit is missing")
	}
	if scope == 1 && lastByte == 1 {
		// only a delimit bit, return empty bitlist
		return td.New(), nil
	}
	delimitBitIndex := ByteBitIndex(lastByte)
	bitLen := ((scope - 1) << 3) + delimitBitIndex
	if bitLen > td.BitLimit {
		return nil, fmt.Errorf("bitlist has too many bits set in last byte, got bit length %d, limit is %d", bitLen, td.BitLimit)
	}
	// Remove delimit bit
	if delimitBitIndex == 0 {
		// remove complete last byte, it only has a delimit bit
		contents = contents[:scope-1]
	} else {
		// remove delimit bit from last byte, but leave other bits
		contents[scope-1] = lastByte ^ (1 << delimitBitIndex)
	}
	bottomNodes, err := BytesIntoNodes(contents)
	if err != nil {
		return nil, err
	}
	depth := CoverDepth(td.BottomNodeLimit())
	contentsRootNode, _ := SubtreeFillToContents(bottomNodes, depth)
	rootNode := &PairNode{LeftChild: contentsRootNode, RightChild: Uint64View(bitLen).Backing()}
	view, _ := td.ViewFromBacking(rootNode, nil)
	return view.(*BitListView), nil
}

// Get index of left-most 1 bit.
// 0 (incl.) to 8 (excl.)
func ByteBitIndex(v byte) (out uint64) {
	if v&0b11110000 != 0 { // 11110000
		out |= 4
		v >>= 4
	}
	if v&0b00001100 != 0 { // 00001100
		out |= 2
		v >>= 2
	}
	if v&0b00000010 != 0 { // 00000010
		out |= 1
		v >>= 1
	}
	return
}

func (td *BitListTypeDef) String() string {
	return fmt.Sprintf("Bitist[%d]", td.BitLimit)
}

type BitListView struct {
	SubtreeView
	*BitListTypeDef
}

func AsBitList(v View, err error) (*BitListView, error) {
	if err != nil {
		return nil, err
	}
	bv, ok := v.(*BitListView)
	if !ok {
		return nil, fmt.Errorf("view is not a bitlist: %v", v)
	}
	return bv, nil
}

func (tv *BitListView) Append(view BoolView) error {
	ll, err := tv.Length()
	if err != nil {
		return err
	}
	if ll >= tv.BitLimit {
		return fmt.Errorf("list length is %d and appending would exceed the list limit %d", ll, tv.BitLimit)
	}
	// Appending is done by modifying the bottom node at the index list_length. And expanding where necessary as it is being set.
	lastGindex, err := ToGindex64(ll>>8, tv.depth)
	if err != nil {
		return err
	}
	setLast, err := tv.SubtreeView.BackingNode.Setter(lastGindex, true)
	if err != nil {
		return fmt.Errorf("failed to get a setter to append an item")
	}
	var bNode Node
	if ll&0xff == 0 {
		// New bottom node
		bNode, err = setLast(view.BackingFromBitfieldBase(&ZeroHashes[0], 0))
		if err != nil {
			return err
		}
	} else {
		// Apply to existing partially zeroed bottom node
		r, _, subIndex, err := tv.subviewNode(ll)
		if err != nil {
			return err
		}
		bNode, err = setLast(view.BackingFromBitfieldBase(r, subIndex))
		if err != nil {
			return err
		}
	}
	// And update the list length
	setLength, err := bNode.Setter(RightGindex, false)
	if err != nil {
		return err
	}
	newLength := &Root{}
	binary.LittleEndian.PutUint64(newLength[:8], ll+1)
	bNode, err = setLength(newLength)
	if err != nil {
		return err
	}
	return tv.SetBacking(bNode)
}

func (tv *BitListView) Pop() error {
	ll, err := tv.Length()
	if err != nil {
		return err
	}
	if ll == 0 {
		return fmt.Errorf("list length is 0 and no bit can be popped")
	}
	// Popping is done by modifying the bottom node at the index list_length - 1. And expanding where necessary as it is being set.
	lastGindex, err := ToGindex64((ll-1)>>8, tv.depth)
	if err != nil {
		return err
	}
	setLast, err := tv.SubtreeView.BackingNode.Setter(lastGindex, true)
	if err != nil {
		return fmt.Errorf("failed to get a setter to pop a bit")
	}
	// Get the subview to erase
	r, _, subIndex, err := tv.subviewNode(ll - 1)
	if err != nil {
		return err
	}
	// Pop the bit by setting it to the default
	// Update the view to the new tree containing this item.
	bNode, err := setLast(BoolView(false).BackingFromBitfieldBase(r, subIndex))
	if err != nil {
		return err
	}
	// And update the list length
	setLength, err := bNode.Setter(RightGindex, false)
	if err != nil {
		return err
	}
	newLength := &Root{}
	binary.LittleEndian.PutUint64(newLength[:8], ll-1)
	bNode, err = setLength(newLength)
	if err != nil {
		return err
	}
	return tv.SetBacking(bNode)
}

func (tv *BitListView) CheckIndex(i uint64) error {
	ll, err := tv.Length()
	if err != nil {
		return err
	}
	if i >= ll {
		return fmt.Errorf("cannot handle item at element index %d, list only has %d bits", i, ll)
	}
	if i >= tv.BitLimit {
		return fmt.Errorf("bitlist has a an invalid length of %d and cannot handle a bit at index %d because of a limit of %d bits", ll, i, tv.BitLimit)
	}
	return nil
}

func (tv *BitListView) subviewNode(i uint64) (r *Root, bottomIndex uint64, subIndex uint8, err error) {
	bottomIndex, subIndex = i>>8, uint8(i)
	v, err := tv.SubtreeView.GetNode(bottomIndex)
	if err != nil {
		return nil, 0, 0, err
	}
	r, ok := v.(*Root)
	if !ok {
		return nil, 0, 0, fmt.Errorf("bitlist bottom node is not a root, at index %d", i)
	}
	return r, bottomIndex, subIndex, nil
}

func (tv *BitListView) Get(i uint64) (BoolView, error) {
	if err := tv.CheckIndex(i); err != nil {
		return false, err
	}
	r, _, subIndex, err := tv.subviewNode(i)
	if err != nil {
		return false, err
	}
	return BoolType.BoolViewFromBitfieldBacking(r, subIndex)
}

func (tv *BitListView) Set(i uint64, v BoolView) error {
	if err := tv.CheckIndex(i); err != nil {
		return err
	}
	r, bottomIndex, subIndex, err := tv.subviewNode(i)
	if err != nil {
		return err
	}
	return tv.SubtreeView.SetNode(bottomIndex, v.BackingFromBitfieldBase(r, subIndex))
}

func (tv *BitListView) Length() (uint64, error) {
	v, err := tv.SubtreeView.BackingNode.Getter(RightGindex)
	if err != nil {
		return 0, err
	}
	llBytes, ok := v.(*Root)
	if !ok {
		return 0, fmt.Errorf("cannot read node %v as list-length", v)
	}
	ll := binary.LittleEndian.Uint64(llBytes[:8])
	if ll > tv.BitLimit {
		return 0, fmt.Errorf("cannot read list length, length appears to be bigger than limit allows")
	}
	return ll, nil
}

func (tv *BitListView) Copy() (View, error) {
	tvCopy := *tv
	tvCopy.Hook = nil
	return &tvCopy, nil
}

func (tv *BitListView) Iter() BitIter {
	length, err := tv.Length()
	if err != nil {
		return ErrBitIter{err}
	}
	i := uint64(0)
	return BitIterFn(func() (elem bool, ok bool, err error) {
		if i < length {
			var item BoolView
			item, err = tv.Get(i)
			elem = bool(item)
			ok = true
			i += 1
			return
		} else {
			return false, false, nil
		}
	})
}

func (tv *BitListView) ReadonlyIter() BitIter {
	length, err := tv.Length()
	if err != nil {
		return ErrBitIter{err}
	}
	// get contents subtree, to traverse with the stack
	node, err := tv.BackingNode.Left()
	if err != nil {
		return ErrBitIter{err}
	}
	// ignore length mixin in stack
	return bitReadonlyIter(node, length, tv.depth-1)
}

func (tv *BitListView) ValueByteLength() (uint64, error) {
	length, err := tv.Length()
	if err != nil {
		return 0, err
	}
	return (length + 7 + 1) / 8, nil
}

func (tv *BitListView) Serialize(w *codec.EncodingWriter) error {
	contentsAnchor, err := tv.BackingNode.Getter(LeftGindex)
	if err != nil {
		return err
	}
	bitLength, err := tv.Length()
	if err != nil {
		return err
	}
	// round up, but also do not forget the delimit bit
	byteLength := (bitLength + 7 + 1) / 8
	contents := make([]byte, byteLength, byteLength)
	// one less depth, ignore length mix-in. Iteration length without bitlist delimit bit.
	if err := SubtreeIntoBytes(contentsAnchor, tv.depth-1, (bitLength+0xff)>>8, contents); err != nil {
		return err
	}
	// Add delimit bit
	contents[byteLength-1] |= 1 << (bitLength & 7)
	return w.Write(contents)
}
