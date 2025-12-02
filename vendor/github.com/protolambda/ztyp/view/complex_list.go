package view

import (
	"encoding/binary"
	"fmt"
	"github.com/protolambda/ztyp/codec"
	. "github.com/protolambda/ztyp/tree"
)

type ComplexListTypeDef struct {
	ElemType  TypeDef
	ListLimit uint64
	ComplexTypeBase
}

func ComplexListType(elemType TypeDef, limit uint64) *ComplexListTypeDef {
	maxSize := uint64(0)
	if elemType.IsFixedByteLength() {
		maxSize = limit * elemType.TypeByteLength()
	} else {
		maxSize = limit * (elemType.MaxByteLength() + OffsetByteLength)
	}
	return &ComplexListTypeDef{
		ElemType:  elemType,
		ListLimit: limit,
		ComplexTypeBase: ComplexTypeBase{
			MinSize:     0,
			MaxSize:     maxSize,
			Size:        0,
			IsFixedSize: false,
		},
	}
}

func (td *ComplexListTypeDef) FromElements(v ...View) (*ComplexListView, error) {
	if uint64(len(v)) > td.ListLimit {
		return nil, fmt.Errorf("expected no more than %d elements, got %d", td.ListLimit, len(v))
	}
	nodes := make([]Node, len(v), len(v))
	for i, el := range v {
		nodes[i] = el.Backing()
	}
	depth := CoverDepth(td.ListLimit)
	contentsRootNode, _ := SubtreeFillToContents(nodes, depth)
	rootNode := &PairNode{LeftChild: contentsRootNode, RightChild: Uint64View(len(v)).Backing()}
	vecView, _ := td.ViewFromBacking(rootNode, nil)
	return vecView.(*ComplexListView), nil
}

func (td *ComplexListTypeDef) ElementType() TypeDef {
	return td.ElemType
}

func (td *ComplexListTypeDef) Limit() uint64 {
	return td.ListLimit
}

func (td *ComplexListTypeDef) DefaultNode() Node {
	depth := CoverDepth(td.ListLimit)
	// zeroed tree with zero mix-in
	return &PairNode{LeftChild: &ZeroHashes[depth], RightChild: &ZeroHashes[0]}
}

func (td *ComplexListTypeDef) ViewFromBacking(node Node, hook BackingHook) (View, error) {
	depth := CoverDepth(td.ListLimit)
	return &ComplexListView{
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
		ComplexListTypeDef: td,
	}, nil
}

func (td *ComplexListTypeDef) Default(hook BackingHook) View {
	v, _ := td.ViewFromBacking(td.DefaultNode(), hook)
	return v
}

func (td *ComplexListTypeDef) New() *ComplexListView {
	return td.Default(nil).(*ComplexListView)
}

func (td *ComplexListTypeDef) Deserialize(dr *codec.DecodingReader) (View, error) {
	scope := dr.Scope()
	if scope == 0 {
		return td.New(), nil
	}
	if td.ElemType.IsFixedByteLength() {
		elemSize := td.ElemType.TypeByteLength()
		length := scope / elemSize
		if length > td.ListLimit {
			return nil, fmt.Errorf("too many items, limit %d but got %d", td.ListLimit, length)
		}
		if expected := length * elemSize; expected != scope {
			return nil, fmt.Errorf("scope %d does not align to elem size %d", scope, elemSize)
		}
		elements := make([]View, length, length)
		for i := uint64(0); i < length; i++ {
			sub, err := dr.SubScope(elemSize)
			if err != nil {
				return nil, err
			}
			el, err := td.ElemType.Deserialize(sub)
			if err != nil {
				return nil, err
			}
			elements[i] = el
		}
		return td.FromElements(elements...)
	} else {
		firstOffset, err := dr.ReadOffset()
		if err != nil {
			return nil, err
		}
		if firstOffset%OffsetByteLength != 0 {
			return nil, fmt.Errorf("first offset %d does not align to offset length %d", firstOffset, OffsetByteLength)
		}
		length := uint64(firstOffset) / OffsetByteLength
		if length > td.ListLimit {
			return nil, fmt.Errorf("too many items, limit %d but got %d", td.ListLimit, length)
		}
		offsets := make([]uint32, length, length)
		offsets[0] = firstOffset
		prevOffset := firstOffset
		for i := uint64(1); i < length; i++ {
			offset, err := dr.ReadOffset()
			if err != nil {
				return nil, err
			}
			if offset < prevOffset {
				return nil, fmt.Errorf("offset %d for element %d is smaller than previous offset %d", offset, i, prevOffset)
			}
			offsets[i] = offset
			prevOffset = offset
		}
		elements := make([]View, length, length)
		lastIndex := uint32(len(elements) - 1)
		for i := uint32(0); i < lastIndex; i++ {
			size := offsets[i+1] - offsets[i]
			sub, err := dr.SubScope(uint64(size))
			if err != nil {
				return nil, err
			}
			el, err := td.ElemType.Deserialize(sub)
			if err != nil {
				return nil, err
			}
			elements[i] = el
		}
		sub, err := dr.SubScope(scope - uint64(offsets[lastIndex]))
		if err != nil {
			return nil, err
		}
		el, err := td.ElemType.Deserialize(sub)
		if err != nil {
			return nil, err
		}
		elements[lastIndex] = el
		return td.FromElements(elements...)
	}
}

func (td *ComplexListTypeDef) String() string {
	return fmt.Sprintf("List[%s, %d]", td.ElemType.String(), td.ListLimit)
}

type ComplexListView struct {
	SubtreeView
	*ComplexListTypeDef
}

func AsComplexList(v View, err error) (*ComplexListView, error) {
	if err != nil {
		return nil, err
	}
	c, ok := v.(*ComplexListView)
	if !ok {
		return nil, fmt.Errorf("view is not a list: %v", v)
	}
	return c, nil
}

func (tv *ComplexListView) Append(v View) error {
	ll, err := tv.Length()
	if err != nil {
		return err
	}
	if ll >= tv.ListLimit {
		return fmt.Errorf("list length is %d and appending would exceed the list limit %d", ll, tv.ListLimit)
	}
	// Appending is done by setting the node at the index list_length. And expanding where necessary as it is being set.
	lastGindex, err := ToGindex64(ll, tv.depth)
	if err != nil {
		return err
	}
	setLast, err := tv.BackingNode.Setter(lastGindex, true)
	if err != nil {
		return fmt.Errorf("failed to get a setter to append an item: %v", err)
	}
	// Append the item by setting the newly allocated last item to it.
	// Update the view to the new tree containing this item.
	bNode, err := setLast(v.Backing())
	if err != nil {
		return err
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

func (tv *ComplexListView) Pop() error {
	ll, err := tv.Length()
	if err != nil {
		return err
	}
	if ll == 0 {
		return fmt.Errorf("list length is 0 and no item can be popped")
	}
	// Popping is done by setting the node at the index list_length - 1. And expanding where necessary as it is being set.
	lastGindex, err := ToGindex64(ll, tv.depth)
	if err != nil {
		return err
	}
	setLast, err := tv.BackingNode.Setter(lastGindex, true)
	if err != nil {
		return fmt.Errorf("failed to get a setter to pop an item: %v", err)
	}
	// Pop the item by setting it to the zero hash
	// Update the view to the new tree containing this item.
	bNode, err := setLast(&ZeroHashes[0])
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

func (tv *ComplexListView) CheckIndex(i uint64) error {
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

func (tv *ComplexListView) Get(i uint64) (View, error) {
	if err := tv.CheckIndex(i); err != nil {
		return nil, err
	}
	v, err := tv.SubtreeView.GetNode(i)
	if err != nil {
		return nil, err
	}
	return tv.ComplexListTypeDef.ElemType.ViewFromBacking(v, tv.ItemHook(i))
}

func (tv *ComplexListView) Set(i uint64, v View) error {
	return tv.setNode(i, v.Backing())
}

func (tv *ComplexListView) setNode(i uint64, b Node) error {
	if err := tv.CheckIndex(i); err != nil {
		return err
	}
	return tv.SubtreeView.SetNode(i, b)
}

func (tv *ComplexListView) ItemHook(i uint64) BackingHook {
	return func(b Node) error {
		return tv.setNode(i, b)
	}
}

func (tv *ComplexListView) Length() (uint64, error) {
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

func (tv *ComplexListView) Copy() (View, error) {
	tvCopy := *tv
	tvCopy.Hook = nil
	return &tvCopy, nil
}

func (tv *ComplexListView) Iter() ElemIter {
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

func (tv *ComplexListView) ReadonlyIter() ElemIter {
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
	return elemReadonlyIter(node, length, tv.depth-1, tv.ElemType)
}

func (tv *ComplexListView) ValueByteLength() (uint64, error) {
	length, err := tv.Length()
	if err != nil {
		return 0, err
	}
	if tv.ElemType.IsFixedByteLength() {
		return length * tv.ElemType.TypeByteLength(), nil
	} else {
		size := length * OffsetByteLength
		iter := tv.ReadonlyIter()
		for {
			elem, ok, err := iter.Next()
			if err != nil {
				return 0, err
			}
			if !ok {
				break
			}
			valSize, err := elem.ValueByteLength()
			if err != nil {
				return 0, err
			}
			size += valSize
		}
		return size, nil
	}
}

func (tv *ComplexListView) Serialize(w *codec.EncodingWriter) error {
	if tv.ElemType.IsFixedByteLength() {
		return serializeComplexFixElemSeries(tv.ReadonlyIter(), w)
	} else {
		length, err := tv.Length()
		if err != nil {
			return err
		}
		return serializeComplexVarElemSeries(length, tv.ReadonlyIter, w)
	}
}
