package gogen

import (
	"go/ast"
	"go/token"
)

type Field struct {
	Name     string
	TypeName string
	Tag      string
}

func (me Field) Ast() *ast.Field {
	var tag *ast.BasicLit
	if me.Tag != "" {
		tag = &ast.BasicLit{
			Kind:  token.STRING,
			Value: "`" + me.Tag + "`",
		}
	}
	names := []*ast.Ident{}
	if me.Name != "" {
		names = []*ast.Ident{
			&ast.Ident{
				Name: me.Name,
				Obj: &ast.Object{
					Kind: ast.Var,
					Name: me.Name,
				},
			},
		}
	}
	return &ast.Field{
		Names: names,
		Type: &ast.Ident{
			Name: me.TypeName,
		},
		Tag: tag,
	}
}

type Fields []Field

func (me Fields) Ast() *ast.FieldList {
	fields := make([]*ast.Field, len(me))
	for i, field := range me {
		fields[i] = field.Ast()
	}
	return &ast.FieldList{
		List: fields,
	}
}

type Struct struct {
	Name        string
	Fields      Fields
	Methods     Functions
	FieldValues map[string]Expression
}

func (me Struct) Declaration() ast.Decl {
	return &ast.GenDecl{
		Tok: token.TYPE,
		Specs: []ast.Spec{
			&ast.TypeSpec{
				Name: &ast.Ident{
					Name: me.Name,
					Obj: &ast.Object{
						Kind: ast.Typ,
						Name: me.Name,
					},
				},
				Type: &ast.StructType{
					Fields: me.Fields.Ast(),
				},
			},
		},
	}
}

func (me Struct) WithValues(vals map[string]Expression) Struct {
	return Struct{
		Name:        me.Name,
		Fields:      me.Fields,
		Methods:     me.Methods,
		FieldValues: vals,
	}
}

func (me Struct) Expression() ast.Expr {
	elts := make([]ast.Expr, len(me.Fields))
	for i, field := range me.Fields {
		elts[i] = &ast.KeyValueExpr{
			Key: &ast.Ident{
				Name: field.Name,
			},
			Value: &ast.Ident{
			//Value: me.FieldValues[field.Name].Expression(),
			},
		}
	}
	return &ast.CompositeLit{
		Type: &ast.Ident{
			Name: me.Name,
		},
		Elts: elts,
	}
}

type Structs []Struct

func (me *Structs) Add(st Struct) {
	*me = append(*me, st)
}
