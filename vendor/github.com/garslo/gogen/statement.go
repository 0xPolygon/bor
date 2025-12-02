package gogen

import "go/ast"

type Statement interface {
	Statement() ast.Stmt
}
