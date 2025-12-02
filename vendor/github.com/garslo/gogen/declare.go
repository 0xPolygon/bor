package gogen

import (
	"go/ast"
	"go/token"
)

type Declare struct {
	Name     string
	TypeName string
}

func (me Declare) Statement() ast.Stmt {
	return &ast.DeclStmt{
		Decl: &ast.GenDecl{
			Tok: token.VAR,
			Specs: []ast.Spec{
				&ast.ValueSpec{
					Names: []*ast.Ident{
						&ast.Ident{
							Name: me.Name,
							Obj: &ast.Object{
								Kind: ast.Var,
								Name: me.Name,
							},
						},
					},
					Type: &ast.Ident{
						Name: me.TypeName,
					},
				},
			},
		},
	}
}
