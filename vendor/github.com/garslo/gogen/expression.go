package gogen

import "go/ast"

type Expression interface {
	Expression() ast.Expr
}
