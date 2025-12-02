package gogen

import "go/ast"

type For struct {
	Init      Statement
	Condition Expression
	Post      Statement
	Body      []Statement
}

func (me For) Statement() ast.Stmt {
	body := make([]ast.Stmt, len(me.Body))
	for i, bodyPart := range me.Body {
		body[i] = bodyPart.Statement()
	}
	var (
		init ast.Stmt
		cond ast.Expr
		post ast.Stmt
	)
	if me.Init != nil {
		init = me.Init.Statement()
	}
	if me.Condition != nil {
		cond = me.Condition.Expression()
	}
	if me.Post != nil {
		post = me.Post.Statement()
	}

	return &ast.ForStmt{
		Init: init,
		Cond: cond,
		Post: post,
		Body: &ast.BlockStmt{
			List: body,
		},
	}
}
