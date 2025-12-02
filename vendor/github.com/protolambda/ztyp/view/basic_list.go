package view

import (
	"encoding/binary"
	"fmt"
	"github.com/protolambda/ztyp/codec"
	. "github.com/protolambda/ztyp/tree"
)

type BasicListTypeDef struct {
	ElemType  BasicTypeDef
	ListLimit uint64
	ComplexTypeBase
}

func BasicListType(elemType BasicTypeDef, limit uint64) *BasicListTypeDef {
	return &BasicListTypeDef{
		ElemType:  elemType,
		ListLimit: limit,
		ComplexTypeBase: ComplexTypeBase{
			MinSize:     0,
			MaxSize:     limit * elemType.TypeByteLength(),
			Size:        0,
			IsFixedSize: false,
		},
	}
}

func (td *BasicListTypeDef) FromElements(v ...BasicView) (*BasicListView, error) {
	length := uint64(len(v))
	if length > td.ListLimit {
		return nil, fmt.Errorf("expected no more than %d elements, got %d", td.ListLimit, len(v))
	}
	bottomNodes, err := td.ElemType.PackViews(v)
	if err != nil {
		return nil, err
	}
	depth := CoverDepth(td.BottomNodeLimit())
	contentsRootNode, _ := SubtreeFillToContents(bottomNodes, depth)
	rootNode := &PairNode{LeftChild: contentsRootNode, RightChild: Uint64View(len(v)).Backing()}
	listView, _ := td.ViewFromBacking(rootNode, nil)
	return listView.(*BasicListView), nil
}

func (td *BasicListTypeDef) ElementType() TypeDef {
	return td.ElemType
}

func (td *BasicListTypeDef) Limit() uint64 {
	return td.ListLimit
}

func (td *BasicListTypeDef) DefaultNode() Node {
	depth := CoverDepth(td.BottomNodeLimit())
	return &PairNode{LeftChild: &ZeroHashes[depth], RightChild: &ZeroHashes[0]}
}

func (td *BasicListTypeDef) ViewFromBacking(node Node, hook BackingHook) (View, error) {
	depth := CoverDepth(td.BottomNodeLimit())
	return &BasicListView{
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
		BasicListTypeDef: td,
	}, nil
}

func (td *BasicListTypeDef) ElementsPerBottomNode() uint64 {
	return 32 / td.ElemType.TypeByteLength()
}

func (td *BasicListTypeDef) BottomNodeLimit() uint64 {
	perNode := td.ElementsPerBottomNode()
	return (td.ListLimit + perNode - 1) / perNode
}

func (td *BasicListTypeDef) TranslateIndex(index uint64) (nodeIndex uint64, intraNodeIndex uint8) {
	perNode := td.ElementsPerBottomNode()
	return index / perNode, uint8(index & (perNode - 1))
}

func (td *BasicListTypeDef) Default(hook BackingHook) View {
	v, _ := td.ViewFromBacking(td.DefaultNode(), hook)
	return v
}

func (td *BasicListTypeDef) New() *BasicListView {
	return td.Default(nil).(*BasicListView)
}

func (td *BasicListTypeDef) Deserialize(dr *codec.DecodingReader) (View, error) {
	elemSize := td.ElemType.TypeByteLength()
	scope := dr.Scope()
	length := scope / elemSize
	if length > td.ListLimit {
		return nil, fmt.Errorf("too many items, limit %d but got %d", td.ListLimit, length)
	}
	if expected := length * elemSize; expected != scope {
		return nil, fmt.Errorf("scope %d does not align to elem size %d", scope, elemSize)
	}
	if length == 0 {
		return td.New(), nil
	}
	contents := make([]byte, scope, scope)
	if _, err := dr.Read(contents); err != nil {
		return nil, err
	}
	bottomNodes, err := BytesIntoNodes(contents)
	if err != nil {
		return nil, err
	}
	depth := CoverDepth(td.BottomNodeLimit())
	contentsRootNode, _ := SubtreeFillToContents(bottomNodes, depth)
	rootNode := &PairNode{LeftChild: contentsRootNode, RightChild: Uint64View(length).Backing()}
	listView, _ := td.ViewFromBacking(rootNode, nil)
	return listView.(*BasicListView), nil
}

func (td *BasicListTypeDef) String() string {
	return fmt.Sprintf("List[%s, %d]", td.ElemType.String(), td.ListLimit)
}

type BasicListView struct {
	SubtreeView
	*BasicListTypeDef
}

func AsBasicList(v View, err error) (*BasicListView, error) {
	if err != nil {
		return nil, err
	}
	bv, ok := v.(*BasicListView)
	if !ok {
		return nil, fmt.Errorf("view is not a basic list: %v", v)
	}
	return bv, nil
}

func (tv *BasicListView) ViewRoot(h HashFn) Root {
	return tv.BackingNode.MerkleRoot(h)
}

func (tv *BasicListView) Append(view BasicView) error {
	ll, err := tv.Length()
	if err != nil {
		return err
	}
	if ll >= tv.ListLimit {
		return fmt.Errorf("list length is %d and appending would exceed the list limit %d", ll, tv.ListLimit)
	}
	perNode := tv.ElementsPerBottomNode()
	// Appending is done by modifying the bottom node at the index list_length. And expanding where necessary as it is being set.
	lastGindex, err := ToGindex64(ll/perNode, tv.depth)
	if err != nil {
		return err
	}
	setLast, err := tv.SubtreeView.BackingNode.Setter(lastGindex, true)
	if err != nil {
		return fmt.Errorf("failed to get a setter to append an item")
	}
	var bNode Node
	if ll%perNode == 0 {
		// New bottom node
		bNode, err = setLast(view.BackingFromBase(&ZeroHashes[0], 0))
		if err != nil {
			return err
		}
	} else {
		// Apply to existing partially zeroed bottom node
		r, _, subIndex, err := tv.subviewNode(ll)
		if err != nil {
			return err
		}
		bNode, err = setLast(view.BackingFromBase(r, subIndex))
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
	return tv.SetBacking(bNode)
}

func (tv *BasicListView) Pop() error {
	ll, err := tv.Length()
	if err != nil {
		return err
	}
	if ll == 0 {
		return fmt.Errorf("list length is 0 and no item can be popped")
	}
	perNode := tv.ElementsPerBottomNode()
	// Popping is done by modifying the bottom node at the index list_length - 1. And expanding where necessary as it is being set.
	lastGindex, err := ToGindex64((ll-1)/perNode, tv.depth)
	if err != nil {
		return err
	}
	setLast, err := tv.SubtreeView.BackingNode.Setter(lastGindex, true)
	if err != nil {
		return fmt.Errorf("failed to get a setter to pop an item")
	}
	// Get the subview to erase
	r, _, subIndex, err := tv.subviewNode(ll - 1)
	if err != nil {
		return err
	}
	// Pop the item by setting it to the default
	// Update the view to the new tree containing this item.
	defaultElement, err := tv.ElemType.BasicViewFromBacking(&ZeroHashes[0], subIndex)
	if err != nil {
		return err
	}
	bNode, err := setLast(defaultElement.BackingFromBase(r, subIndex))
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

func (tv *BasicListView) CheckIndex(i uint64) error {
	ll, err := tv.Length()
	if err != nil {
		return err
	}
	if i >= ll {
		return fmt.Errorf("cannot handle item at element index %d, list only has %d elements", i, ll)
	}
	if i >= tv.ListLimit {
		return fmt.Errorf("list has a an invalid length of %d and cannot handle an element at index %d because of a limit of %d elements", ll, i, tv.ListLimit)
	}
	return nil
}

func (tv *BasicListView) subviewNode(i uint64) (r *Root, bottomIndex uint64, subIndex uint8, err error) {
	bottomIndex, subIndex = tv.TranslateIndex(i)
	v, err := tv.SubtreeView.GetNode(bottomIndex)
	if err != nil {
		return nil, 0, 0, err
	}
	r, ok := v.(*Root)
	if !ok {
		return nil, 0, 0, fmt.Errorf("basic list bottom node is not a root, at index %d", i)
	}
	return r, bottomIndex, subIndex, nil
}

func (tv *BasicListView) Get(i uint64) (BasicView, error) {
	if err := tv.CheckIndex(i); err != nil {
		return nil, err
	}
	r, _, subIndex, err := tv.subviewNode(i)
	if err != nil {
		return nil, err
	}
	return tv.ElemType.BasicViewFromBacking(r, subIndex)
}

func (tv *BasicListView) Set(i uint64, v BasicView) error {
	if err := tv.CheckIndex(i); err != nil {
		return err
	}
	r, bottomIndex, subIndex, err := tv.subviewNode(i)
	if err != nil {
		return err
	}
	return tv.SubtreeView.SetNode(bottomIndex, v.BackingFromBase(r, subIndex))
}

func (tv *BasicListView) Length() (uint64, error) {
	v, err := tv.SubtreeView.BackingNode.Getter(RightGindex)
	if err != nil {
		return 0, err
	}
	llBytes, ok := v.(*Root)
	if !ok {
		return 0, fmt.Errorf("cannot read node %v as list-length", v)
	}
	ll := binary.LittleEndian.Uint64(llBytes[:8])
	if ll > tv.ListLimit {
		return 0, fmt.Errorf("cannot read list length, length appears to be bigger than limit allows")
	}
	return ll, nil
}

func (tv *BasicListView) Copy() (View, error) {
	tvCopy := *tv
	tvCopy.Hook = nil
	return &tvCopy, nil
}

func (tv *BasicListView) Iter() ElemIter {
	length, err := tv.Length()
	if err != nil {
		return ErrElemIter{err}
	}
	i := uint64(0)
	return ElemIterFn(func() (elem View, ok bool, err error) {
		if i < length {
			elem, err = tv.Get(i)
			ok = true
			i += 1
			return
		} else {
			return nil, false, nil
		}
	})
}

func (tv *BasicListView) ReadonlyIter() ElemIter {
	length, err := tv.Length()
	if err != nil {
		return ErrElemIter{err}
	}
	// get contents subtree, to traverse with the stack
	node, err := tv.BackingNode.Left()
	if err != nil {
		return ErrElemIter{err}
	}
	// ignore length mixin in stack
	return basicElemReadonlyIter(node, length, tv.depth-1, tv.ElemType)
}

func (tv *BasicListView) ValueByteLength() (uint64, error) {
	length, err := tv.Length()
	if err != nil {
		return 0, err
	}
	return length * tv.ElemType.TypeByteLength(), nil
}

func (tv *BasicListView) Serialize(w *codec.EncodingWriter) error {
	contentsAnchor, err := tv.BackingNode.Getter(LeftGindex)
	if err != nil {
		return err
	}
	length, err := tv.Length()
	if err != nil {
		return err
	}
	elemSize := tv.ElemType.TypeByteLength()
	byteLength := length * elemSize
	contents := make([]byte, byteLength, byteLength)
	// one less depth, ignore length mix-in
	perNode := 32 / elemSize
	nodeCount := (length + perNode - 1) / perNode
	if err := SubtreeIntoBytes(contentsAnchor, tv.depth-1, nodeCount, contents); err != nil {
		return err
	}
	return w.Write(contents)
}
