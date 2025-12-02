package gogen

import "go/ast"

type CallFunction struct {
	Func   Expression
	Params []Expression
}

func (me CallFunction) Statement() ast.Stmt {
	return &ast.ExprStmt{
		X: me.Expression(),
	}
}

func (me CallFunction) Expression() ast.Expr {
	params := make([]ast.Expr, len(me.Params))
	for i, param := range me.Params {
		params[i] = param.Expression()
	}
	return &ast.CallExpr{
		Fun:  me.Func.Expression(),
		Args: params,
	}
}

// TODO: Bad name, change it
type Functor struct {
	Func Expression
}

func (me Functor) Call(params ...Expression) CallFunction {
	return CallFunction{
		Func:   me.Func,
		Params: params,
	}
}
