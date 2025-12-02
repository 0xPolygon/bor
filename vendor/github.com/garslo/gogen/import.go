package gogen

import (
	"fmt"
	"go/ast"
	"go/token"
)

type Import struct {
	Name string
}

func (me Import) Declaration() ast.Decl {
	return &ast.GenDecl{
		Tok: token.IMPORT,
		Specs: []ast.Spec{
			&ast.ImportSpec{
				Path: &ast.BasicLit{
					Kind:  token.STRING,
					Value: fmt.Sprintf(`"%s"`, me.Name),
				},
			},
		},
	}

}

type Imports []Import

func (me *Imports) Add(imp Import) {
	*me = append(*me, imp)
}
