package view

import (
	"fmt"
	"github.com/protolambda/ztyp/codec"
	. "github.com/protolambda/ztyp/tree"
)

type BasicVectorTypeDef struct {
	ElemType     BasicTypeDef
	VectorLength uint64
	ComplexTypeBase
}

func BasicVectorType(elemType BasicTypeDef, length uint64) *BasicVectorTypeDef {
	size := length * elemType.TypeByteLength()
	return &BasicVectorTypeDef{
		ElemType:     elemType,
		VectorLength: length,
		ComplexTypeBase: ComplexTypeBase{
			MinSize:     size,
			MaxSize:     size,
			Size:        size,
			IsFixedSize: true,
		},
	}
}

func (td *BasicVectorTypeDef) FromElements(v ...BasicView) (*BasicVectorView, error) {
	length := uint64(len(v))
	if length > td.VectorLength {
		return nil, fmt.Errorf("expected no more than %d elements, got %d", td.VectorLength, length)
	}
	bottomNodes, err := td.ElemType.PackViews(v)
	if err != nil {
		return nil, err
	}
	depth := CoverDepth(td.BottomNodeLength())
	rootNode, _ := SubtreeFillToContents(bottomNodes, depth)
	listView, _ := td.ViewFromBacking(rootNode, nil)
	return listView.(*BasicVectorView), nil
}

func (td *BasicVectorTypeDef) ElementType() TypeDef {
	return td.ElemType
}

func (td *BasicVectorTypeDef) Length() uint64 {
	return td.VectorLength
}

func (td *BasicVectorTypeDef) DefaultNode() Node {
	depth := CoverDepth(td.BottomNodeLength())
	return SubtreeFillToDepth(&ZeroHashes[0], depth)
}

func (td *BasicVectorTypeDef) ViewFromBacking(node Node, hook BackingHook) (View, error) {
	depth := CoverDepth(td.BottomNodeLength())
	return &BasicVectorView{
		SubtreeView: SubtreeView{
			BackedView: BackedView{
				ViewBase: ViewBase{
					TypeDef: td,
				},
				Hook:        hook,
				BackingNode: node,
			},
			depth: depth,
		},
		BasicVectorTypeDef: td,
	}, nil
}

func (td *BasicVectorTypeDef) ElementsPerBottomNode() uint64 {
	return 32 / td.ElemType.TypeByteLength()
}

func (td *BasicVectorTypeDef) BottomNodeLength() uint64 {
	perNode := td.ElementsPerBottomNode()
	return (td.VectorLength + perNode - 1) / perNode
}

func (td *BasicVectorTypeDef) TranslateIndex(index uint64) (nodeIndex uint64, intraNodeIndex uint8) {
	perNode := td.ElementsPerBottomNode()
	return index / perNode, uint8(index & (perNode - 1))
}

func (td *BasicVectorTypeDef) Default(hook BackingHook) View {
	v, _ := td.ViewFromBacking(td.DefaultNode(), hook)
	return v
}

func (td *BasicVectorTypeDef) New() *BasicVectorView {
	return td.Default(nil).(*BasicVectorView)
}

func (td *BasicVectorTypeDef) Deserialize(dr *codec.DecodingReader) (View, error) {
	scope := dr.Scope()
	if td.Size != scope {
		return nil, fmt.Errorf("expected size %d does not match scope %d", td.Size, scope)
	}
	contents := make([]byte, scope, scope)
	if _, err := dr.Read(contents); err != nil {
		return nil, err
	}
	bottomNodes, err := BytesIntoNodes(contents)
	if err != nil {
		return nil, err
	}
	depth := CoverDepth(td.BottomNodeLength())
	rootNode, _ := SubtreeFillToContents(bottomNodes, depth)
	listView, _ := td.ViewFromBacking(rootNode, nil)
	return listView.(*BasicVectorView), nil
}

func (td *BasicVectorTypeDef) String() string {
	return fmt.Sprintf("Vector[%s, %d]", td.ElemType.String(), td.VectorLength)
}

type BasicVectorView struct {
	SubtreeView
	*BasicVectorTypeDef
}

func AsBasicVector(v View, err error) (*BasicVectorView, error) {
	if err != nil {
		return nil, err
	}
	bv, ok := v.(*BasicVectorView)
	if !ok {
		return nil, fmt.Errorf("view is not a basic vector: %v", v)
	}
	return bv, nil
}

func (tv *BasicVectorView) subviewNode(i uint64) (r *Root, bottomIndex uint64, subIndex uint8, err error) {
	bottomIndex, subIndex = tv.TranslateIndex(i)
	v, err := tv.SubtreeView.GetNode(bottomIndex)
	if err != nil {
		return nil, 0, 0, err
	}
	r, ok := v.(*Root)
	if !ok {
		return nil, 0, 0, fmt.Errorf("basic vector bottom node is not a root, at index %d", i)
	}
	return r, bottomIndex, subIndex, nil
}

func (tv *BasicVectorView) Get(i uint64) (BasicView, error) {
	if i >= tv.VectorLength {
		return nil, fmt.Errorf("basic vector has length %d, cannot get index %d", tv.VectorLength, i)
	}
	r, _, subIndex, err := tv.subviewNode(i)
	if err != nil {
		return nil, err
	}
	return tv.ElemType.BasicViewFromBacking(r, subIndex)
}

func (tv *BasicVectorView) Set(i uint64, v BasicView) error {
	if i >= tv.VectorLength {
		return fmt.Errorf("cannot set item at element index %d, basic vector only has %d elements", i, tv.VectorLength)
	}
	r, bottomIndex, subIndex, err := tv.subviewNode(i)
	if err != nil {
		return err
	}
	return tv.SubtreeView.SetNode(bottomIndex, v.BackingFromBase(r, subIndex))
}

func (tv *BasicVectorView) Copy() (View, error) {
	tvCopy := *tv
	tvCopy.Hook = nil
	return &tvCopy, nil
}

func (tv *BasicVectorView) Iter() ElemIter {
	i := uint64(0)
	return ElemIterFn(func() (elem View, ok bool, err error) {
		if i < tv.VectorLength {
			elem, err = tv.Get(i)
			ok = true
			i += 1
			return
		} else {
			return nil, false, nil
		}
	})
}

func (tv *BasicVectorView) ReadonlyIter() ElemIter {
	return basicElemReadonlyIter(tv.BackingNode, tv.VectorLength, tv.depth, tv.ElemType)
}

func (tv *BasicVectorView) ValueByteLength() (uint64, error) {
	return tv.Size, nil
}

func (tv *BasicVectorView) Serialize(w *codec.EncodingWriter) error {
	contents := make([]byte, tv.Size, tv.Size)
	if err := SubtreeIntoBytes(tv.BackingNode, tv.depth, tv.BottomNodeLength(), contents); err != nil {
		return err
	}
	return w.Write(contents)
}
