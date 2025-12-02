package view

import (
	"bytes"
	"fmt"
	"github.com/protolambda/ztyp/codec"
	. "github.com/protolambda/ztyp/tree"
)

type FieldDef struct {
	Name string
	Type TypeDef
}

type ContainerTypeDef struct {
	ComplexTypeBase
	ContainerName string
	Fields        []FieldDef
	OffsetsCount  uint64
	FixedPartSize uint64
}

func ContainerType(name string, fields []FieldDef) *ContainerTypeDef {
	minSize := uint64(0)
	maxSize := uint64(0)
	fixedPart := uint64(0)
	offsetsCount := uint64(0)
	for _, f := range fields {
		if f.Type.IsFixedByteLength() {
			size := f.Type.TypeByteLength()
			fixedPart += size
			minSize += size
			maxSize += size
		} else {
			offsetsCount += 1
			fixedPart += OffsetByteLength
			minSize += OffsetByteLength + f.Type.MinByteLength()
			maxSize += OffsetByteLength + f.Type.MaxByteLength()
		}
	}
	size := uint64(0)
	isFixedSize := offsetsCount == 0
	if isFixedSize {
		size = fixedPart
	}
	return &ContainerTypeDef{
		ComplexTypeBase: ComplexTypeBase{
			MinSize:     minSize,
			MaxSize:     maxSize,
			Size:        size,
			IsFixedSize: isFixedSize,
		},
		ContainerName: name,
		Fields:        fields,
		OffsetsCount:  offsetsCount,
		FixedPartSize: fixedPart,
	}
}

func (td *ContainerTypeDef) FromFields(v ...View) (*ContainerView, error) {
	if len(td.Fields) != len(v) {
		return nil, fmt.Errorf("expected %d fields, got %d", len(td.Fields), len(v))
	}
	nodes := make([]Node, len(td.Fields), len(td.Fields))
	for i, el := range v {
		nodes[i] = el.Backing()
	}
	depth := CoverDepth(td.FieldCount())
	rootNode, err := SubtreeFillToContents(nodes, depth)
	if err != nil {
		return nil, err
	}
	conView, err := td.ViewFromBacking(rootNode, nil)
	if err != nil {
		return nil, err
	}
	return conView.(*ContainerView), nil
}

func (td *ContainerTypeDef) DefaultNode() Node {
	fieldCount := td.FieldCount()
	depth := CoverDepth(fieldCount)
	nodes := make([]Node, fieldCount, fieldCount)
	for i, f := range td.Fields {
		nodes[i] = f.Type.DefaultNode()
	}
	// can ignore error, depth is derive from nodes count.
	rootNode, _ := SubtreeFillToContents(nodes, depth)
	return rootNode
}

func (td *ContainerTypeDef) ViewFromBacking(node Node, hook BackingHook) (View, error) {
	fieldCount := td.FieldCount()
	depth := CoverDepth(fieldCount)
	return &ContainerView{
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
		ContainerTypeDef: td,
	}, nil
}

func (td *ContainerTypeDef) Default(hook BackingHook) View {
	v, _ := td.ViewFromBacking(td.DefaultNode(), hook)
	return v
}

func (td *ContainerTypeDef) New() *ContainerView {
	return td.Default(nil).(*ContainerView)
}

func (td *ContainerTypeDef) FieldCount() uint64 {
	return uint64(len(td.Fields))
}

type offsetField struct {
	index  int
	offset uint32
}

func (td *ContainerTypeDef) Deserialize(dr *codec.DecodingReader) (View, error) {
	fields := make([]View, len(td.Fields), len(td.Fields))
	offsets := make([]offsetField, 0, td.OffsetsCount)
	prevOffset := uint32(td.FixedPartSize)
	scope := dr.Scope()
	if err := td.checkScope(scope); err != nil {
		return nil, err
	}
	// Deserialize the fixed part: fixed-size fields and offsets to dynamic fields
	for i, f := range td.Fields {
		if f.Type.IsFixedByteLength() {
			sub, err := dr.SubScope(f.Type.TypeByteLength())
			if err != nil {
				return nil, err
			}
			v, err := f.Type.Deserialize(sub)
			if err != nil {
				return nil, err
			}
			fields[i] = v
		} else {
			offset, err := dr.ReadOffset()
			if err != nil {
				return nil, err
			}
			if offset < prevOffset {
				return nil, fmt.Errorf("offset %d of field %d is smaller than prev offset %d", offset, i, prevOffset)
			}
			if uint64(offset) > scope {
				return nil, fmt.Errorf("offset %d of field %d is too big for scope %d", offset, i, scope)
			}
			prevOffset = offset
			offsets = append(offsets, offsetField{index: i, offset: offset})
		}
	}
	// Deserialize the dynamic part: for each offset, get the size and deserialize the element
	for i, item := range offsets {
		var size uint32
		if i+1 == len(offsets) {
			size = uint32(scope) - item.offset
		} else {
			size = offsets[i+1].offset - item.offset
		}
		sub, err := dr.SubScope(uint64(size))
		if err != nil {
			return nil, err
		}
		v, err := td.Fields[item.index].Type.Deserialize(sub)
		if err != nil {
			return nil, err
		}
		fields[item.index] = v
	}
	// Collected all elements, now construct the tree in one go
	return td.FromFields(fields...)
}

func (td *ContainerTypeDef) String() string {
	return td.ContainerName
}

func (td *ContainerTypeDef) TypeRepr() string {
	var buf bytes.Buffer
	buf.WriteString(td.ContainerName)
	buf.WriteString("(Container):")
	for _, f := range td.Fields {
		buf.WriteString("    ")
		buf.WriteString(f.Name)
		buf.WriteString(": ")
		buf.WriteString(f.Type.String())
		buf.WriteRune('\n')
	}
	return buf.String()
}

type ContainerView struct {
	SubtreeView
	*ContainerTypeDef
}

func AsContainer(v View, err error) (*ContainerView, error) {
	if err != nil {
		return nil, err
	}
	c, ok := v.(*ContainerView)
	if !ok {
		return nil, fmt.Errorf("view is not a container: %v", v)
	}
	return c, nil
}

func (tv *ContainerView) Copy() (View, error) {
	tvCopy := *tv
	tvCopy.Hook = nil
	return &tvCopy, nil
}

func (tv *ContainerView) ValueByteLength() (uint64, error) {
	if tv.IsFixedSize {
		return tv.Size, nil
	}
	out := uint64(0)
	for i, f := range tv.Fields {
		if f.Type.IsFixedByteLength() {
			out += f.Type.TypeByteLength()
		} else {
			v, err := tv.Get(uint64(i))
			if err != nil {
				return 0, err
			}
			vSize, err := v.ValueByteLength()
			if err != nil {
				return 0, err
			}
			out += vSize + OffsetByteLength
		}
	}
	return out, nil
}

func (tv *ContainerView) Serialize(w *codec.EncodingWriter) error {
	fieldIter := tv.ReadonlyIter()
	var dynFields []View
	if !tv.IsFixedSize {
		dynFields = make([]View, 0, tv.OffsetsCount)
	}
	// the previous offset, to calculate a new offset from, starting after the fixed data.
	prevOffset := tv.FixedPartSize

	// span of the previous var-size element
	prevSize := uint64(0)
	for {
		f, ok, err := fieldIter.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		fType := f.Type()
		if fType.IsFixedByteLength() {
			if err := f.Serialize(w); err != nil {
				return err
			}
		} else {
			fieldValSize, err := f.ValueByteLength()
			if err != nil {
				return err
			}
			prevOffset, err = w.WriteOffset(prevOffset, prevSize)
			if err != nil {
				return err
			}
			prevSize = fieldValSize
			// Queue the actual element to be encoded after the fixed part of the container is encoded.
			dynFields = append(dynFields, f)
		}
	}
	if !tv.IsFixedSize {
		for _, v := range dynFields {
			if err := v.Serialize(w); err != nil {
				return err
			}
		}
	}
	return nil
}

func (tv *ContainerView) Iter() ElemIter {
	i := uint64(0)
	fieldCount := tv.FieldCount()
	return ElemIterFn(func() (elem View, ok bool, err error) {
		if i < fieldCount {
			elem, err = tv.Get(i)
			ok = true
			i += 1
			return
		} else {
			return nil, false, nil
		}
	})
}

func (tv *ContainerView) FieldValues() ([]View, error) {
	values := make([]View, len(tv.Fields), len(tv.Fields))
	iter := tv.ReadonlyIter()
	i := 0
	for {
		el, ok, err := iter.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		values[i] = el
		i++
	}
	return values, nil
}

func (tv *ContainerView) ReadonlyIter() ElemIter {
	return fieldReadonlyIter(tv.BackingNode, tv.depth, tv.Fields)
}

func (tv *ContainerView) Get(i uint64) (View, error) {
	if count := tv.ContainerTypeDef.FieldCount(); i >= count {
		return nil, fmt.Errorf("cannot get item at field index %d, container only has %d fields", i, count)
	}
	v, err := tv.SubtreeView.GetNode(i)
	if err != nil {
		return nil, err
	}
	return tv.ContainerTypeDef.Fields[i].Type.ViewFromBacking(v, tv.ItemHook(i))
}

func (tv *ContainerView) Set(i uint64, v View) error {
	return tv.setNode(i, v.Backing())
}

func (tv *ContainerView) setNode(i uint64, b Node) error {
	if fieldCount := tv.ContainerTypeDef.FieldCount(); i >= fieldCount {
		return fmt.Errorf("cannot set item at field index %d, container only has %d fields", i, fieldCount)
	}
	return tv.SubtreeView.SetNode(i, b)
}

func (tv *ContainerView) ItemHook(i uint64) BackingHook {
	return func(b Node) error {
		return tv.setNode(i, b)
	}
}
