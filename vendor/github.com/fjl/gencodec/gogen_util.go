// Copyright 2017 Felix Lange <fjl@twurst.com>.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"

	. "github.com/garslo/gogen"
)

func errCheck(expr Expression) If {
	err := Name("err")
	return If{
		Init:      DeclareAndAssign{Lhs: err, Rhs: expr},
		Condition: NotEqual{Lhs: err, Rhs: NIL},
		Body:      []Statement{Return{Values: []Expression{err}}},
	}
}

// makeCall creates a call like `make(typ, len(lenfrom))`.
func makeCall(typ types.Type, lenfrom Expression, qf types.Qualifier) Expression {
	return CallFunction{Func: Name("make"), Params: []Expression{
		Name(types.TypeString(typ, qf)),
		CallFunction{Func: Name("len"), Params: []Expression{lenfrom}},
	}}
}

// lenCall creates a call like `len(v)`.
func lenCall(v Expression) Expression {
	return CallFunction{Func: Name("len"), Params: []Expression{v}}
}

// errorsNewCall creates a call like `errors.New(errmsg)`.
func errorsNewCall(sc *fileScope, errmsg string) Expression {
	errors := sc.packageName("errors")
	return CallFunction{
		Func:   Dotted{Receiver: Name(errors), Name: "New"},
		Params: []Expression{stringLit{errmsg}},
	}
}

// hasSideEffects returns whether an expression may have side effects.
func hasSideEffects(expr Expression) bool {
	switch expr := expr.(type) {
	case Var:
		return false
	case Dotted:
		return hasSideEffects(expr.Receiver)
	case Star:
		return hasSideEffects(expr.Value)
	case Index:
		return hasSideEffects(expr.Index) && hasSideEffects(expr.Value)
	default:
		return true
	}
}

// stringLit is a string literal expression.
type stringLit struct {
	V string
}

func (l stringLit) Expression() ast.Expr {
	return &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(l.V)}
}

// declStmt is a declaration statement.
type declStmt struct {
	d Declaration
}

func (ds declStmt) Statement() ast.Stmt {
	return &ast.DeclStmt{Decl: ds.d.Declaration()}
}

// sliceExpr is a slicing expression Value[Low:High:Cap].
type sliceExpr struct {
	Value          Expression
	Low, High, Cap Expression
}

func (s sliceExpr) Expression() ast.Expr {
	sl := &ast.SliceExpr{X: s.Value.Expression()}
	if s.Low != nil {
		sl.Low = s.Low.Expression()
	}
	if s.High != nil {
		sl.High = s.High.Expression()
	}
	if s.Cap != nil {
		sl.Max = s.Cap.Expression()
	}
	return sl
}
