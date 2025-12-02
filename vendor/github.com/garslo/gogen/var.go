package gogen

import (
	"fmt"
	"go/ast"
	"strconv"
)

type Var struct {
	Name string
}

func (me Var) Expression() ast.Expr {
	return &ast.Ident{
		Name: me.Name,
		Obj: &ast.Object{
			Kind: ast.Var,
			Name: me.Name,
		},
	}
}

// Things that are like Var but either deserve their own name, or have
// slightly different behaviors

type String struct {
	Value string
}

func (me String) Expression() ast.Expr {
	return Var{fmt.Sprintf(`"%s"`, me.Value)}.Expression()
}

func Int(value int) Var {
	return Var{strconv.Itoa(value)}
}

func Pkg(value string) Var {
	return Var{value}
}

func Name(value string) Var {
	return Var{value}
}
