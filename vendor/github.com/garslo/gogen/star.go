package gogen

import "go/ast"

type Star struct {
	Value Expression
}

func (me Star) Expression() ast.Expr {
	return &ast.StarExpr{
		X: me.Value.Expression(),
	}
}
