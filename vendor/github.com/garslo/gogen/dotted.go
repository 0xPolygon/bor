package gogen

import "go/ast"

type Dotted struct {
	Receiver Expression
	Name     string
}

func (me Dotted) Expression() ast.Expr {
	return &ast.SelectorExpr{
		X: me.Receiver.Expression(),
		Sel: &ast.Ident{
			Name: me.Name,
		},
	}
}
