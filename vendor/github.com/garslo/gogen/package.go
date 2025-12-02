package gogen

import (
	"go/ast"
	"go/format"
	"go/token"
	"io"
)

type Package struct {
	Name         string
	Declarations []Declaration
}

func (me *Package) Declare(decl Declaration) *Package {
	me.Declarations = append(me.Declarations, decl)
	return me
}

func (me *Package) Ast() ast.Node {
	decls := make([]ast.Decl, len(me.Declarations))
	for i, decl := range me.Declarations {
		decls[i] = decl.Declaration()
	}
	return &ast.File{
		Name: &ast.Ident{
			Name: me.Name,
		},
		Decls: decls,
	}
}

func (me *Package) WriteTo(w io.Writer) error {
	fset := token.NewFileSet()
	return format.Node(w, fset, me.Ast())
}
