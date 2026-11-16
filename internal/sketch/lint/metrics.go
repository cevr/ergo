package lint

import "ergo.local/prototype/internal/sketch/syntax"

// The complexity metrics of the strict preset (q149), over one function
// declaration's body, function literals included. They count syntax, as
// the metrics are defined; no rule matches a name through them.

// cyclomatic is McCabe's number as ESLint's complexity rule counts it: 1,
// plus each if (an else-if is an if; a plain else adds nothing), each
// loop, each match case, each catch arm and each && and ||.
func cyclomatic(body *syntax.BlockStmt) float64 {
	n := 1.0
	syntax.Inspect(body, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.IfStmt, *syntax.ForStmt, *syntax.RangeStmt, *syntax.CaseClause, *syntax.CatchArm:
			n++
		case *syntax.BinaryExpr:
			if node.Op == syntax.LAND || node.Op == syntax.LOR {
				n++
			}
		}
		return true
	})
	return n
}

// cognitive is SonarSource's cognitive complexity (G. Ann Campbell, 2018)
// on Ergo's syntax: +1 plus the nesting level for each if, match, loop and
// catch; +1 for each else-if and each else; +1 for each run of like
// boolean operators (a && b && c is 1, a && b || c is 2). The bodies of
// if, else, match, loops, catch and function literals nest one level
// deeper. Ergo has no labels and no goto, so break and continue add
// nothing.
func cognitive(body *syntax.BlockStmt) float64 {
	total := 0
	var stmts func(list []syntax.Stmt, nesting int)
	var node func(n syntax.Node, nesting int)
	var ifChain func(s *syntax.IfStmt, nesting int, first bool)
	ifChain = func(s *syntax.IfStmt, nesting int, first bool) {
		if first {
			total += 1 + nesting
		} else {
			total++ // else if
		}
		node(s.Cond, nesting)
		stmts(s.Body.List, nesting+1)
		switch e := s.Else.(type) {
		case *syntax.IfStmt:
			ifChain(e, nesting, false)
		case *syntax.BlockStmt:
			total++ // else
			stmts(e.List, nesting+1)
		}
	}
	stmts = func(list []syntax.Stmt, nesting int) {
		for _, s := range list {
			node(s, nesting)
		}
	}
	node = func(n syntax.Node, nesting int) {
		syntax.Inspect(n, func(x syntax.Node) bool {
			switch x := x.(type) {
			case *syntax.IfStmt:
				ifChain(x, nesting, true)
				return false
			case *syntax.SwitchStmt:
				total += 1 + nesting
				node(x.Tag, nesting)
				for _, c := range x.Clauses {
					stmts(c.Body, nesting+1)
				}
				return false
			case *syntax.TypeSwitchStmt:
				total += 1 + nesting
				for _, g := range x.Subjects {
					node(g.X, nesting)
				}
				for _, c := range x.Clauses {
					stmts(c.Body, nesting+1)
				}
				return false
			case *syntax.ForStmt:
				total += 1 + nesting
				if x.Cond != nil {
					node(x.Cond, nesting)
				}
				stmts(x.Body.List, nesting+1)
				return false
			case *syntax.RangeStmt:
				total += 1 + nesting
				node(x.X, nesting)
				stmts(x.Body.List, nesting+1)
				return false
			case *syntax.CatchExpr:
				total += 1 + nesting
				node(x.X, nesting)
				for _, a := range x.Arms {
					stmts(a.Body, nesting+1)
				}
				return false
			case *syntax.FuncLit:
				if x.Body != nil {
					stmts(x.Body.List, nesting+1)
				}
				return false
			case *syntax.BinaryExpr:
				if x.Op == syntax.LAND || x.Op == syntax.LOR {
					var ops []syntax.Token
					var operands []syntax.Expr
					booleanRun(x, &ops, &operands)
					total++
					for i := 1; i < len(ops); i++ {
						if ops[i] != ops[i-1] {
							total++
						}
					}
					for _, o := range operands {
						node(o, nesting)
					}
					return false
				}
			}
			return true
		})
	}
	stmts(body.List, 0)
	return float64(total)
}

// booleanRun lists the && and || operators of a boolean expression in
// source order, through parentheses, and the operands that are not one.
func booleanRun(e syntax.Expr, ops *[]syntax.Token, operands *[]syntax.Expr) {
	switch x := e.(type) {
	case *syntax.ParenExpr:
		booleanRun(x.X, ops, operands)
		return
	case *syntax.BinaryExpr:
		if x.Op == syntax.LAND || x.Op == syntax.LOR {
			booleanRun(x.X, ops, operands)
			*ops = append(*ops, x.Op)
			booleanRun(x.Y, ops, operands)
			return
		}
	}
	*operands = append(*operands, e)
}

// halstead is Halstead's difficulty, (n1 / 2) × (N2 / n2): n1 distinct
// operators, n2 distinct operands and N2 operands in all. Operands are
// identifiers and literals, distinct by their text. Operators are the
// operator tokens of unary, binary and assignment forms, and one operator
// each for if, loop, match, case, catch, try, return, break and continue,
// let, a call, an index, a selector, a composite literal and a function
// literal. 0 when the body has no operand.
func halstead(body *syntax.BlockStmt) float64 {
	operators := map[string]bool{}
	operands := map[string]bool{}
	total := 0
	op := func(name string) { operators[name] = true }
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Ident:
			operands["id "+n.Name] = true
			total++
		case *syntax.BasicLit:
			operands["lit "+n.Value] = true
			total++
		case *syntax.BinaryExpr:
			op(n.Op.String())
		case *syntax.UnaryExpr:
			op("unary " + n.Op.String())
		case *syntax.AssignStmt:
			op(n.Tok.String())
		case *syntax.IfStmt:
			op("if")
		case *syntax.ForStmt, *syntax.RangeStmt:
			op("for")
		case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
			op("match")
		case *syntax.CaseClause, *syntax.CatchArm:
			op("case")
		case *syntax.CatchExpr:
			op("catch")
		case *syntax.TryExpr:
			op("try")
		case *syntax.ReturnStmt:
			op("return")
		case *syntax.BranchStmt:
			op(n.Tok.String())
		case *syntax.DeclStmt:
			op("let")
		case *syntax.CallExpr:
			op("()")
		case *syntax.IndexExpr:
			op("[]")
		case *syntax.SelectorExpr:
			op(".")
		case *syntax.CompositeLit:
			op("{}")
		case *syntax.FuncLit:
			op("func")
		}
		return true
	})
	if len(operands) == 0 {
		return 0
	}
	return float64(len(operators)) / 2 * float64(total) / float64(len(operands))
}
