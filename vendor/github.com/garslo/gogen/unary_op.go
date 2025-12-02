package gogen

import (
	"go/ast"
	"go/token"
)

type Increment struct {
	Value Expression
}

func (me Increment) Statement() ast.Stmt {
	return &ast.IncDecStmt{
		X:   me.Value.Expression(),
		Tok: token.INC,
	}
}

type Decrement struct {
	Value Expression
}

func (me Decrement) Statement() ast.Stmt {
	return &ast.IncDecStmt{
		X:   me.Value.Expression(),
		Tok: token.DEC,
	}
}

type Not struct {
	Value Expression
}

func (me Not) Expression() ast.Expr {
	return &ast.UnaryExpr{
		X:  me.Value.Expression(),
		Op: token.NOT,
	}
}

type AddressOf struct {
	Value Expression
}

func (me AddressOf) Expression() ast.Expr {
	return &ast.UnaryExpr{
		X:  me.Value.Expression(),
		Op: token.AND,
	}
}

type Index struct {
	Value, Index Expression
}

func (me Index) Expression() ast.Expr {
	return &ast.IndexExpr{
		X:     me.Value.Expression(),
		Index: me.Index.Expression(),
	}
}
