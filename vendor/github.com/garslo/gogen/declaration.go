package gogen

import "go/ast"

type Declaration interface {
	Declaration() ast.Decl
}
