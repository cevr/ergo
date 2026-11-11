package lower

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Tail results (196r; check-v1 D-C1 and D-C2). A body yields its last
// statement, and a final if, match or bare block yields through its
// branches (Info.Tails). One walk writes every such body toward a
// destination: the frame's return, a Go variable that holds the value of an
// if or match in an expression, or plain statements. A branch that ends in
// return, break or continue writes that statement and yields nothing.

type destKind int

const (
	// destStmt writes the list as statements: the value is not used.
	destStmt destKind = iota
	// destReturn returns the value from the frame: a function body's tail
	// (retExpr), or a trailing block's when block is set (retTail).
	destReturn
	// destAssign assigns the value to the Go variable name.
	destAssign
)

// dest is where a body's last value goes. A with block's tails assign its
// value to name (none when the with has no value) and the failure a tail
// passes on to err (rows.md, With 6).
type dest struct {
	kind  destKind
	block bool
	name  string
	err   string
	typ   check.Type // the type of name's value, for destAssign
}

// assignedTo is x's value put into a Go variable of the checked type to: x
// with the implicit conversion Info records at x, else, when to is a flat
// union and x one of its variants (an arm of a match or catch whose value
// joins its arms' types), that variant put into it (flatunion.go). A
// sealed interface takes a variant as Go assigns it.
func (fc *funcCtx) assignedTo(x syntax.Expr, val string, to check.Type) string {
	if _, ok := fc.l.info.Implicit[x]; ok || to == nil {
		return fc.convertAt(x, val)
	}
	v, _ := fc.flatVariantValue(fc.typeOf(x), to, val, x)
	return v
}

// yieldList writes a statement list whose last statement yields to d and
// reports whether its Go form terminates.
func (fc *funcCtx) yieldList(list []syntax.Stmt, d dest) bool {
	if d.kind == destStmt || len(list) == 0 {
		return fc.stmts(list)
	}
	fc.stmts(list[:len(list)-1])
	return fc.yieldStmt(list[len(list)-1], d)
}

// yieldStmt writes the last statement of a body toward d.
func (fc *funcCtx) yieldStmt(s syntax.Stmt, d dest) bool {
	switch s := s.(type) {
	case *syntax.ExprStmt:
		fc.at(s)
		if call := fc.unreachableCall(s.X); call != nil {
			// A defect leaves the branch: no value, as a return (defect.go).
			fc.unreachableStmt(call)
			return true
		}
		switch {
		case d.kind == destReturn && d.block:
			fc.retTail(s.X)
			return true
		case d.kind == destReturn:
			fc.retExpr(s.X)
			return true
		}
		if _, tail := fc.l.info.Tails[s]; !tail {
			fc.discard(s.X)
			return false
		}
		if d.err != "" && fc.fallibleExpr(s.X) {
			o := fc.expr(s.X)
			if d.name != "" && o.val != "" {
				fc.line(d.name + ", " + d.err + " = " + fc.assignedTo(s.X, o.val, d.typ) + ", " + o.err)
				return false
			}
			fc.line(d.err + " = " + o.err)
			if o.val != "" {
				// The body's value is discarded (D-C1); its failure is not.
				fc.line("_ = " + o.val)
			}
			return false
		}
		if d.name == "" {
			fc.discard(s.X)
			return false
		}
		fc.line(d.name + " = " + fc.assignedTo(s.X, fc.value(s.X), d.typ))
		return false
	case *syntax.IfStmt:
		fc.at(s)
		return fc.ifYield(s, d)
	case *syntax.TypeSwitchStmt:
		fc.at(s)
		return fc.typeMatch(s, d)
	case *syntax.SwitchStmt:
		fc.at(s)
		return fc.valueMatch(s, d)
	case *syntax.BlockStmt:
		fc.at(s)
		fc.line("{")
		fc.depth++
		t := fc.yieldList(s.List, d)
		fc.depth--
		fc.line("}")
		return t
	}
	return fc.stmt(s)
}

// ifYield lowers if/else with each branch yielding to d. A condition that
// needs statements of its own is evaluated in the else branch of the
// previous condition, never before it.
func (fc *funcCtx) ifYield(s *syntax.IfStmt, d dest) bool {
	cond := fc.headerValue(s.Cond)
	fc.line("if " + cond + " {")
	fc.depth++
	then := fc.yieldList(s.Body.List, d)
	fc.depth--
	switch e := s.Else.(type) {
	case nil:
		fc.line("}")
		return false
	case *syntax.BlockStmt:
		fc.line("} else {")
		fc.depth++
		els := fc.yieldList(e.List, d)
		fc.depth--
		fc.line("}")
		return then && els
	case *syntax.IfStmt:
		fc.line("} else {")
		fc.depth++
		fc.at(e)
		els := fc.ifYield(e, d)
		fc.depth--
		fc.line("}")
		return then && els
	}
	unreachable(s.Else, ruleParser, "an else branch that is neither a block nor an if")
	return false
}

// branchExpr lowers an if or match in value position: a Go variable of the
// checked type, assigned by every branch that yields (D-C2), and a Go error
// variable when a branch's tail can fail, which the expression passes on to
// its enclosing try or catch, as a with block does (review O2). A branch
// that leaves (return, break, continue) assigns nothing.
func (fc *funcCtx) branchExpr(x syntax.Expr) operand {
	s := x.(syntax.Stmt)
	var res operand
	if !fc.noValue(x) {
		res.val = fc.temp("v")
		fc.line("var " + res.val + " " + fc.goType(fc.typeOf(x)))
	}
	if fc.tailFallible(s) {
		res.err = fc.temp("err")
		fc.line("var " + res.err + " error")
	}
	if res.val == "" && res.err == "" {
		fc.yieldStmt(s, dest{})
		return res
	}
	fc.yieldStmt(s, dest{kind: destAssign, name: res.val, err: res.err, typ: fc.typeOf(x)})
	return res
}

// tailFallible reports whether a tail that s yields can fail: a fallible
// expression statement Info.Tails records, or a branch of a final if, match
// or block whose own tail can.
func (fc *funcCtx) tailFallible(s syntax.Stmt) bool {
	last := func(list []syntax.Stmt) bool {
		return len(list) > 0 && fc.tailFallible(list[len(list)-1])
	}
	switch s := s.(type) {
	case *syntax.ExprStmt:
		_, tail := fc.l.info.Tails[s]
		return tail && fc.fallibleExpr(s.X)
	case *syntax.IfStmt:
		if last(s.Body.List) {
			return true
		}
		switch e := s.Else.(type) {
		case *syntax.BlockStmt:
			return last(e.List)
		case *syntax.IfStmt:
			return fc.tailFallible(e)
		}
	case *syntax.SwitchStmt:
		for _, cl := range s.Clauses {
			if last(cl.Body) {
				return true
			}
		}
	case *syntax.TypeSwitchStmt:
		for _, cl := range s.Clauses {
			if last(cl.Body) {
				return true
			}
		}
	case *syntax.BlockStmt:
		return last(s.List)
	}
	return false
}

// noCase writes the default arm Go needs after a match the checker proved
// total: it panics with a named defect, never yields a zero value (check-v1
// ruling, 2026-10-30). Only a value Go can hold and Ergo cannot write, such
// as a nil interface, reaches it.
//
// A match of the program names its line and column, which the defect's
// Ergo frames place in a file; a match of a std body, whose frames are
// not the program's, names its std file too (G1, batch 11).
func (fc *funcCtx) noCase(n syntax.Node) {
	path, line, col := fc.l.locate(n, n.Pos())
	at := strconv.Itoa(line) + ":" + strconv.Itoa(col)
	if _, ok := fc.l.sourceOf(n); !ok {
		at = path + ":" + at
	}
	fc.line("\tpanic(" + strconv.Quote("ergo: defect: no case of the match at "+at+" matched") + ")")
}

// blockBody writes a trailing block's body as its literal's frame: the block
// yields its last statement (Info.Tails).
func (fc *funcCtx) blockBody(list []syntax.Stmt) {
	if fc.yieldList(list, dest{kind: destReturn, block: true}) {
		return
	}
	if fc.frame.result != nil {
		fc.line("panic(\"ergo: unreachable: the block's end\")")
		return
	}
	fc.line(fc.errReturn("nil"))
}

// needsDefault reports whether a total match without case _ gets the
// defect default (noCase): when its value is returned, so Go sees the
// function end; and when its value is assigned and some arm yields one, so
// no path keeps a zero value. A statement match needs none: nothing is
// hidden, and Go would report the code after an all-returning match
// unreachable.
func (d dest) needsDefault(allTerminate bool) bool {
	switch d.kind {
	case destReturn:
		return true
	case destAssign:
		return !allTerminate
	}
	return false
}
