package view

import (
	"bytes"
	"fmt"
	"github.com/protolambda/ztyp/codec"
	. "github.com/protolambda/ztyp/tree"
)

type UnionTypeDef struct {
	ComplexTypeBase
	// a "None" option is just a nil.
	Options []TypeDef
}

func UnionType(options []TypeDef) *UnionTypeDef {
	if len(options) == 0 {
		panic("union requires at least 1 option")
	}
	minSize := uint64(0)
	maxSize := uint64(0)

	if options[0] != nil {
		minSize = options[0].MinByteLength()
		maxSize = options[0].MaxByteLength()
	}

	for i, t := range options[1:] {
		if t == nil {
			panic(fmt.Errorf("union type option %d should not be nil, only option 0 can be nil", i))
		}
		min, max := t.MinByteLength(), t.MaxByteLength()
		if min < minSize {
			minSize = min
		}
		if max > maxSize {
			maxSize = max
		}
	}
	return &UnionTypeDef{
		ComplexTypeBase: ComplexTypeBase{
			// Add the selector length
			MinSize:     minSize + 1,
			MaxSize:     maxSize + 1,
			Size:        0,
			IsFixedSize: false,
		},
		Options: options,
	}
}

func (td *UnionTypeDef) FromView(selector uint8, v View) (*UnionView, error) {
	var selectorNode Root
	selectorNode[0] = selector
	var content Node
	if v == nil {
		content = new(Root)
	} else {
		content = v.Backing()
	}
	rootNode := NewPairNode(content, &selectorNode)
	conView, err := td.ViewFromBacking(rootNode, nil)
	if err != nil {
		return nil, err
	}
	return conView.(*UnionView), nil
}

func (td *UnionTypeDef) DefaultNode() Node {
	return NewPairNode(td.Options[0].DefaultNode(), new(Root))
}

func (td *UnionTypeDef) ViewFromBacking(node Node, hook BackingHook) (View, error) {
	return &UnionView{
		BackedView: BackedView{
			ViewBase: ViewBase{
				TypeDef: td,
			},
			Hook:        hook,
			BackingNode: node,
		},
		UnionTypeDef: td,
	}, nil
}

func (td *UnionTypeDef) Default(hook BackingHook) View {
	v, _ := td.ViewFromBacking(td.DefaultNode(), hook)
	return v
}

func (td *UnionTypeDef) New() *UnionView {
	return td.Default(nil).(*UnionView)
}

func (td *UnionTypeDef) Deserialize(dr *codec.DecodingReader) (View, error) {
	scope := dr.Scope()
	if scope == 0 {
		return nil, fmt.Errorf("scope must be non-zero to deserialize union")
	}
	selector, err := dr.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("failed to read selector: %v", err)
	}
	if selector >= uint8(len(td.Options)) {
		return nil, fmt.Errorf("type selector is too large: %d (%d options)", selector, len(td.Options))
	}
	option := td.Options[selector]
	if option == nil {
		return td.FromView(selector, nil)
	}
	subView, err := option.Deserialize(dr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode union element (selector %d, scope %d): %v", selector, scope, err)
	}
	return td.FromView(selector, subView)
}

func (td *UnionTypeDef) String() string {
	return td.TypeRepr()
}

func (td *UnionTypeDef) TypeRepr() string {
	var buf bytes.Buffer
	buf.WriteString("Union[")
	for _, f := range td.Options {
		buf.WriteString(f.String())
		buf.WriteString(", ")
	}
	buf.WriteRune(']')
	return buf.String()
}

type UnionView struct {
	BackedView
	*UnionTypeDef
}

func AsUnion(v View, err error) (*UnionView, error) {
	if err != nil {
		return nil, err
	}
	c, ok := v.(*UnionView)
	if !ok {
		return nil, fmt.Errorf("view is not a union: %v", v)
	}
	return c, nil
}

func (tv *UnionView) Copy() (View, error) {
	tvCopy := *tv
	tvCopy.Hook = nil
	return &tvCopy, nil
}

func (tv *UnionView) Selector() (uint8, error) {
	selectorNode, err := tv.BackingNode.Right()
	if err != nil {
		return 0, fmt.Errorf("union selector could not be read: %v", err)
	}
	root, ok := selectorNode.(*Root)
	if !ok {
		return 0, fmt.Errorf("expected Root node for union selector, but got type %T", selectorNode)
	}
	for i := 1; i < 32; i++ {
		if root[i] != 0 {
			return 0, fmt.Errorf("union selector node has invalid value: %x", root[:])
		}
	}
	if root[0] >= uint8(len(tv.Options)) {
		return 0, fmt.Errorf("out of range selector: %v (%d options)", root[0], len(tv.Options))
	}
	return root[0], nil
}

// Return the value. May be nil if it's a Union[None, T...]
func (tv *UnionView) Value() (View, error) {
	selector, err := tv.Selector()
	if err != nil {
		return nil, err
	}
	content, err := tv.BackingNode.Left()
	if err != nil {
		return nil, fmt.Errorf("could not access union content node: %v", err)
	}
	option := tv.Options[selector]
	if option == nil {
		return nil, nil
	}
	return option.ViewFromBacking(content, nil)
}

func (tv *UnionView) ValueByteLength() (uint64, error) {
	contentView, err := tv.Value()
	if err != nil {
		return 0, fmt.Errorf("failed to interpret content")
	}
	if contentView == nil {
		return 1, nil
	}
	contentSize, err := contentView.ValueByteLength()
	// add 1 for the selector byte
	return contentSize + 1, err
}

func (tv *UnionView) Serialize(w *codec.EncodingWriter) error {
	selector, err := tv.Selector()
	if err != nil {
		return err
	}
	if err := w.WriteByte(selector); err != nil {
		return fmt.Errorf("failed to write selector: %v", err)
	}
	content, err := tv.BackingNode.Left()
	if err != nil {
		return fmt.Errorf("could not access union content node: %v", err)
	}
	option := tv.Options[selector]
	if option == nil {
		return nil
	}
	v, err := option.ViewFromBacking(content, nil)
	if err != nil {
		return fmt.Errorf("invalid value content node: %v", err)
	}
	return v.Serialize(w)
}

// Changes the union selector and value. Does not check the view type.
// The value may be nil in the Union[None, T...] case.
func (tv *UnionView) Change(selector uint8, value View) error {
	if selector >= uint8(len(tv.Options)) {
		return fmt.Errorf("out of range selector: %v (%d options)", selector, len(tv.Options))
	}
	var selectorNode Root
	selectorNode[0] = selector
	var contentNode Node
	if value == nil {
		if selector != 0 {
			return fmt.Errorf("only the 0 selector can be used for nil values, got %d", selector)
		}
		contentNode = new(Root)
	} else {
		contentNode = value.Backing()
	}
	return tv.BackedView.SetBacking(NewPairNode(&selectorNode, contentNode))
}
