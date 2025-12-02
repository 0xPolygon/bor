package gogen

import "go/ast"

type Thunk struct {
	Expr ast.Expr
	Stmt ast.Stmt
	Decl ast.Decl
}

func (me Thunk) Expression() ast.Expr {
	return me.Expr
}

func (me Thunk) Statement() ast.Stmt {
	return me.Stmt
}

func (me Thunk) Declaration() ast.Decl {
	return me.Decl
}
