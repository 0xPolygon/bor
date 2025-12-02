// Copyright 2017 Felix Lange <fjl@twurst.com>.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

package main

import (
	"fmt"
	"go/printer"
	"go/token"
	"go/types"
	"io"
	"strings"

	. "github.com/garslo/gogen"
)

var (
	NIL     = Name("nil")
	intType = types.Universe.Lookup("int").Type()
)

type marshalMethod struct {
	mtyp        *marshalerType
	scope       *funcScope
	isUnmarshal bool
	// cached identifiers for map, slice conversions
	iterKey, iterVal Var
}

func newMarshalMethod(mtyp *marshalerType, isUnmarshal bool) *marshalMethod {
	s := newFuncScope(mtyp.scope)
	return &marshalMethod{
		mtyp:        mtyp,
		scope:       newFuncScope(mtyp.scope),
		isUnmarshal: isUnmarshal,
		iterKey:     Name(s.newIdent("k")),
		iterVal:     Name(s.newIdent("v")),
	}
}

func writeFunction(w io.Writer, fs *token.FileSet, fn Function) {
	printer.Fprint(w, fs, fn.Declaration())
	fmt.Fprintln(w)
}

// genUnmarshalJSON generates the UnmarshalJSON method.
func genUnmarshalJSON(mtyp *marshalerType) Function {
	var (
		m        = newMarshalMethod(mtyp, true)
		recv     = m.receiver()
		input    = Name(m.scope.newIdent("input"))
		intertyp = m.intermediateType(m.scope.newIdent(m.mtyp.orig.Obj().Name()))
		dec      = Name(m.scope.newIdent("dec"))
		json     = Name(m.scope.parent.packageName("encoding/json"))
	)
	fn := Function{
		Receiver:    recv,
		Name:        "UnmarshalJSON",
		ReturnTypes: Types{{TypeName: "error"}},
		Parameters:  Types{{Name: input.Name, TypeName: "[]byte"}},
		Body: []Statement{
			declStmt{intertyp},
			Declare{Name: dec.Name, TypeName: intertyp.Name},
			errCheck(CallFunction{
				Func:   Dotted{Receiver: json, Name: "Unmarshal"},
				Params: []Expression{input, AddressOf{Value: dec}},
			}),
		},
	}
	fn.Body = append(fn.Body, m.unmarshalConversions(dec, Name(recv.Name), "json")...)
	fn.Body = append(fn.Body, Return{Values: []Expression{NIL}})
	return fn
}

// genMarshalJSON generates the MarshalJSON method.
func genMarshalJSON(mtyp *marshalerType) Function {
	var (
		m        = newMarshalMethod(mtyp, false)
		recv     = m.receiver()
		intertyp = m.intermediateType(m.scope.newIdent(m.mtyp.orig.Obj().Name()))
		enc      = Name(m.scope.newIdent("enc"))
		json     = Name(m.scope.parent.packageName("encoding/json"))
	)
	fn := Function{
		Receiver:    recv,
		Name:        "MarshalJSON",
		ReturnTypes: Types{{TypeName: "[]byte"}, {TypeName: "error"}},
		Body: []Statement{
			declStmt{intertyp},
			Declare{Name: enc.Name, TypeName: intertyp.Name},
		},
	}
	fn.Body = append(fn.Body, m.marshalConversions(Name(recv.Name), enc, "json")...)
	fn.Body = append(fn.Body, Return{Values: []Expression{
		CallFunction{
			Func:   Dotted{Receiver: json, Name: "Marshal"},
			Params: []Expression{AddressOf{Value: enc}},
		},
	}})
	return fn
}

// genUnmarshalYAML generates the UnmarshalYAML method.
func genUnmarshalYAML(mtyp *marshalerType) Function {
	return genUnmarshalLikeYAML(mtyp, "YAML")
}

// genUnmarshalTOML generates the UnmarshalTOML method.
func genUnmarshalTOML(mtyp *marshalerType) Function {
	return genUnmarshalLikeYAML(mtyp, "TOML")
}

func genUnmarshalLikeYAML(mtyp *marshalerType, name string) Function {
	var (
		m         = newMarshalMethod(mtyp, true)
		recv      = m.receiver()
		unmarshal = Name(m.scope.newIdent("unmarshal"))
		intertyp  = m.intermediateType(m.scope.newIdent(m.mtyp.orig.Obj().Name()))
		dec       = Name(m.scope.newIdent("dec"))
		tag       = strings.ToLower(name)
	)
	fn := Function{
		Receiver:    recv,
		Name:        "Unmarshal" + name,
		ReturnTypes: Types{{TypeName: "error"}},
		Parameters:  Types{{Name: unmarshal.Name, TypeName: "func (interface{}) error"}},
		Body: []Statement{
			declStmt{intertyp},
			Declare{Name: dec.Name, TypeName: intertyp.Name},
			errCheck(CallFunction{Func: unmarshal, Params: []Expression{AddressOf{Value: dec}}}),
		},
	}
	fn.Body = append(fn.Body, m.unmarshalConversions(dec, Name(recv.Name), tag)...)
	fn.Body = append(fn.Body, Return{Values: []Expression{NIL}})
	return fn
}

// genMarshalYAML generates the MarshalYAML method.
func genMarshalYAML(mtyp *marshalerType) Function {
	return genMarshalLikeYAML(mtyp, "YAML")
}

// genMarshalTOML generates the MarshalTOML method.
func genMarshalTOML(mtyp *marshalerType) Function {
	return genMarshalLikeYAML(mtyp, "TOML")
}

func genMarshalLikeYAML(mtyp *marshalerType, name string) Function {
	var (
		m        = newMarshalMethod(mtyp, false)
		recv     = m.receiver()
		intertyp = m.intermediateType(m.scope.newIdent(m.mtyp.orig.Obj().Name()))
		enc      = Name(m.scope.newIdent("enc"))
		tag      = strings.ToLower(name)
	)
	fn := Function{
		Receiver:    recv,
		Name:        "Marshal" + name,
		ReturnTypes: Types{{TypeName: "interface{}"}, {TypeName: "error"}},
		Body: []Statement{
			declStmt{intertyp},
			Declare{Name: enc.Name, TypeName: intertyp.Name},
		},
	}
	fn.Body = append(fn.Body, m.marshalConversions(Name(recv.Name), enc, tag)...)
	fn.Body = append(fn.Body, Return{Values: []Expression{AddressOf{Value: enc}, NIL}})
	return fn
}

func (m *marshalMethod) receiver() Receiver {
	letter := strings.ToLower(m.mtyp.name[:1])
	r := Receiver{Name: m.scope.newIdent(letter), Type: Name(m.mtyp.name)}
	if m.isUnmarshal {
		r.Type = Star{Value: r.Type}
	}
	return r
}

func (m *marshalMethod) intermediateType(name string) Struct {
	s := Struct{Name: name}
	for _, f := range m.mtyp.Fields {
		if m.isUnmarshal && f.function != nil {
			continue // fields generated from functions cannot be assigned on unmarshal
		}
		typ := f.typ
		if m.isUnmarshal {
			typ = ensureNilCheckable(typ)
		} else if isNonEmptyInterface(f.origTyp) {
			// Non-empty interface is left as-is for Marshal*, i.e. we let the
			// interface value handle its own marshaling.
			typ = f.origTyp
		}
		s.Fields = append(s.Fields, Field{
			Name:     f.name,
			TypeName: types.TypeString(typ, m.mtyp.scope.qualify),
			Tag:      f.tag,
		})
	}
	return s
}

func (m *marshalMethod) unmarshalConversions(from, to Var, format string) (s []Statement) {
	for _, f := range m.mtyp.Fields {
		if f.function != nil {
			continue // fields generated from functions cannot be assigned
		}

		fieldName := f.encodedName(format)
		accessFrom := Dotted{Receiver: from, Name: f.name}
		accessTo := Dotted{Receiver: to, Name: f.name}
		typ := ensureNilCheckable(f.typ)
		if !f.isRequired(format) {
			s = append(s, If{
				Condition: NotEqual{Lhs: accessFrom, Rhs: NIL},
				Body:      m.convert(accessFrom, accessTo, typ, f.origTyp, fieldName),
			})
		} else {
			err := fmt.Sprintf("missing required field '%s' for %s", fieldName, m.mtyp.name)
			s = append(s, If{
				Condition: Equals{Lhs: accessFrom, Rhs: NIL},
				Body: []Statement{
					Return{
						Values: []Expression{
							errorsNewCall(m.scope.parent, err),
						},
					},
				},
			})
			s = append(s, m.convert(accessFrom, accessTo, typ, f.origTyp, fieldName)...)
		}
	}
	return s
}

func (m *marshalMethod) marshalConversions(from, to Var, fieldName string) (s []Statement) {
	for _, f := range m.mtyp.Fields {
		accessFrom := Dotted{Receiver: from, Name: f.name}
		accessTo := Dotted{Receiver: to, Name: f.name}
		var value Expression = accessFrom
		if f.function != nil {
			value = CallFunction{Func: accessFrom}
		}
		// Non-empty interface values are handled differently between Marshal* and Unmarshal*.
		// The conversion is only applied in the Unmarshal* method.
		// For Marshal*, we let the value handle its own encoding, i.e. conversion is skipped.
		var fieldType = f.typ
		if isNonEmptyInterface(f.origTyp) {
			fieldType = f.origTyp
		}
		s = append(s, m.convert(value, accessTo, f.origTyp, fieldType, fieldName)...)
	}
	return s
}

func (m *marshalMethod) convert(from, to Expression, fromtyp, totyp types.Type, fieldName string) (s []Statement) {
	// Remove pointer introduced by ensureNilCheckable during field building.
	if isPointer(fromtyp) && !isPointer(totyp) && !isInterface(totyp) {
		from = Star{Value: from}
		fromtyp = fromtyp.(*types.Pointer).Elem()
	} else if !isPointer(fromtyp) && isPointer(totyp) {
		from = AddressOf{Value: from}
		fromtyp = types.NewPointer(fromtyp)
	}

	qf := m.mtyp.scope.qualify
	switch {
	// Array -> slice (with [:] syntax)
	case underlyingArray(fromtyp) != nil && underlyingSlice(totyp) != nil:
		s = append(s, m.convertArrayToSlice(from, to, fromtyp, totyp)...)

	// Slice -> array (with loop)
	case underlyingSlice(fromtyp) != nil && underlyingArray(totyp) != nil:
		s = append(s, m.convertSliceToArray(from, to, fromtyp, totyp, fieldName)...)

	// Simple conversion `totyp(from)`
	case types.ConvertibleTo(fromtyp, totyp):
		s = append(s, Assign{Lhs: to, Rhs: convertSimple(from, fromtyp, totyp, qf)})

	// slice/slice and map/map (with loop)
	case underlyingSlice(fromtyp) != nil:
		s = append(s, m.convertLoop(from, to, sliceKV(fromtyp), sliceKV(totyp))...)
	case underlyingMap(fromtyp) != nil:
		s = append(s, m.convertLoop(from, to, mapKV(fromtyp), mapKV(totyp))...)

	default:
		invalidConv(fromtyp, totyp, qf)
	}
	return s
}

type kvType struct {
	Type      types.Type
	Key, Elem types.Type
}

func mapKV(typ types.Type) kvType {
	maptyp := underlyingMap(typ)
	return kvType{typ, maptyp.Key(), maptyp.Elem()}
}

func sliceKV(typ types.Type) kvType {
	slicetyp := underlyingSlice(typ)
	return kvType{typ, intType, slicetyp.Elem()}
}

// convertLoop creates type conversion code between two slice/map types.
func (m *marshalMethod) convertLoop(from, to Expression, fromTyp, toTyp kvType) (conv []Statement) {
	if hasSideEffects(from) {
		orig := from
		from = Name(m.scope.newIdent("tmp"))
		conv = []Statement{DeclareAndAssign{Lhs: from, Rhs: orig}}
	}
	// The actual conversion is a loop that assigns each element.
	inner := []Statement{
		Assign{Lhs: to, Rhs: makeCall(toTyp.Type, from, m.scope.parent.qualify)},
		Range{
			Key:        m.iterKey,
			Value:      m.iterVal,
			RangeValue: from,
			Body: []Statement{Assign{
				Lhs: Index{Value: to, Index: convertSimple(m.iterKey, fromTyp.Key, toTyp.Key, m.scope.parent.qualify)},
				Rhs: convertSimple(m.iterVal, fromTyp.Elem, toTyp.Elem, m.scope.parent.qualify),
			}},
		},
	}
	// Preserve nil maps and slices when marshaling. This is not required for unmarshaling
	// methods because the field is already nil-checked earlier.
	if !m.isUnmarshal {
		inner = []Statement{If{
			Condition: NotEqual{Lhs: from, Rhs: NIL},
			Body:      inner,
		}}
	}
	return append(conv, inner...)
}

// arrayToSliceConv converts a slice value to an array.
func (m *marshalMethod) convertArrayToSlice(from Expression, to Expression, fromtyp, totyp types.Type) (conv []Statement) {
	if hasSideEffects(from) {
		orig := from
		from = Name(m.scope.newIdent("tmp"))
		conv = []Statement{DeclareAndAssign{Lhs: from, Rhs: orig}}
	}

	fromEtype := underlyingArray(fromtyp).Elem()
	toEtype := underlyingSlice(totyp).Elem()

	if fromEtype == toEtype {
		// For identical element types, we can just slice the array.
		return append(conv, Assign{Lhs: to, Rhs: sliceExpr{Value: from}})
	}

	// For different element types, we need to convert each element.
	return append(conv,
		Assign{
			Lhs: to,
			Rhs: makeCall(totyp, from, m.scope.parent.qualify),
		},
		Range{
			Key:        m.iterKey,
			Value:      m.iterVal,
			RangeValue: from,
			Body: []Statement{Assign{
				Lhs: Index{Value: to, Index: m.iterKey},
				Rhs: convertSimple(m.iterVal, fromEtype, toEtype, m.scope.parent.qualify),
			}},
		},
	)
}

// sliceToArrayConv converts an array value to a slice.
func (m *marshalMethod) convertSliceToArray(from Expression, to Expression, fromtyp, totyp types.Type, format string) (conv []Statement) {
	if hasSideEffects(from) {
		orig := from
		from = Name(m.scope.newIdent("tmp"))
		conv = []Statement{DeclareAndAssign{Lhs: from, Rhs: orig}}
	}

	fromEtype := underlyingSlice(fromtyp).Elem()
	toArray := underlyingArray(totyp)
	toEtype := toArray.Elem()

	// Check length of input slice matches the array size.
	if m.isUnmarshal {
		errormsg := fmt.Sprintf("field '%s' has wrong length, need %d items", format, toArray.Len())
		conv = append(conv, If{
			Condition: NotEqual{Lhs: lenCall(from), Rhs: lenCall(to)},
			Body: []Statement{
				Return{Values: []Expression{
					errorsNewCall(m.scope.parent, errormsg),
				}},
			},
		})
	}

	if fromEtype == toEtype {
		// Copy can be used when element types are identical.
		conv = append(conv, CallFunction{
			Func: Name("copy"), Params: []Expression{
				sliceExpr{Value: to},
				from,
			},
		})
	} else {
		// Otherwise the conversion is a loop that assigns each element.
		conv = append(conv, Range{
			Key:        m.iterKey,
			Value:      m.iterVal,
			RangeValue: from,
			Body: []Statement{Assign{
				Lhs: Index{Value: to, Index: m.iterKey},
				Rhs: convertSimple(m.iterVal, fromEtype, toEtype, m.scope.parent.qualify),
			}},
		})
	}
	return conv
}

func convertSimple(from Expression, fromtyp, totyp types.Type, qf types.Qualifier) Expression {
	if types.AssignableTo(fromtyp, totyp) {
		return from
	}
	if !types.ConvertibleTo(fromtyp, totyp) {
		invalidConv(fromtyp, totyp, qf)
	}
	toname := types.TypeString(totyp, qf)
	if isPointer(totyp) {
		toname = "(" + toname + ")" // hack alert!
	}
	return CallFunction{Func: Name(toname), Params: []Expression{from}}
}

func invalidConv(from, to types.Type, qf types.Qualifier) {
	panic(fmt.Errorf("BUG: invalid conversion %s -> %s", types.TypeString(from, qf), types.TypeString(to, qf)))
}
