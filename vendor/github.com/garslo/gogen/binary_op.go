package gogen

import (
	"go/ast"
	"go/token"
)

type LessThan struct {
	Lhs Expression
	Rhs Expression
}

func (me LessThan) Expression() ast.Expr {
	return &ast.BinaryExpr{
		Op: token.LSS,
		X:  me.Lhs.Expression(),
		Y:  me.Rhs.Expression(),
	}
}

type LessThanOrEqual struct {
	Lhs Expression
	Rhs Expression
}

func (me LessThanOrEqual) Expression() ast.Expr {
	return &ast.BinaryExpr{
		Op: token.LEQ,
		X:  me.Lhs.Expression(),
		Y:  me.Rhs.Expression(),
	}
}

type GreaterThan struct {
	Lhs Expression
	Rhs Expression
}

func (me GreaterThan) Expression() ast.Expr {
	return &ast.BinaryExpr{
		Op: token.GTR,
		X:  me.Lhs.Expression(),
		Y:  me.Rhs.Expression(),
	}
}

type GreaterThanOrEqual struct {
	Lhs Expression
	Rhs Expression
}

func (me GreaterThanOrEqual) Expression() ast.Expr {
	return &ast.BinaryExpr{
		Op: token.GEQ,
		X:  me.Lhs.Expression(),
		Y:  me.Rhs.Expression(),
	}
}

type Equals struct {
	Lhs Expression
	Rhs Expression
}

func (me Equals) Expression() ast.Expr {
	return &ast.BinaryExpr{
		Op: token.EQL,
		X:  me.Lhs.Expression(),
		Y:  me.Rhs.Expression(),
	}
}

type NotEqual struct {
	Lhs Expression
	Rhs Expression
}

func (me NotEqual) Expression() ast.Expr {
	return &ast.BinaryExpr{
		Op: token.NEQ,
		X:  me.Lhs.Expression(),
		Y:  me.Rhs.Expression(),
	}
}
