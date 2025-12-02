package gogen

import "go/ast"

type If struct {
	Init      Statement
	Condition Expression
	Body      []Statement
}

func (me If) Statement() ast.Stmt {
	var (
		init ast.Stmt
	)
	if me.Init != nil {
		init = me.Init.Statement()
	}
	body := make([]ast.Stmt, len(me.Body))
	for j, stmt := range me.Body {
		body[j] = stmt.Statement()
	}
	return &ast.IfStmt{
		Init: init,
		Cond: me.Condition.Expression(),
		Body: &ast.BlockStmt{
			List: body,
		},
	}
}
