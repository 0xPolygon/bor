package gogen

import (
	"go/ast"
	"go/token"
)

type DeclareAndAssign struct {
	Lhs Expression
	Rhs Expression
}

func (me DeclareAndAssign) Statement() ast.Stmt {
	return &ast.AssignStmt{
		Tok: token.DEFINE,
		Lhs: []ast.Expr{me.Lhs.Expression()},
		Rhs: []ast.Expr{me.Rhs.Expression()},
	}
}
