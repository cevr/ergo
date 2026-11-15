package lower

// IR builder: what Go's own rules ask of a body (IRG3b, with TXT's text
// view). See ir.go.
//
// The decoder hands the body to Go's back end, and TXT prints it as Go text
// that go vet and types2 check. Two rules of Go reach the IR:
//
//   - a body with a result ends in a terminating statement (the Go spec's
//     list: a return, a panic, a goto, an if with both branches
//     terminating, a switch with a default whose clauses all terminate and
//     that no break leaves, a block whose last statement terminates, a for
//     with no condition that no break leaves). irTerminates is that list on
//     the IR; the decoder's ergoTerminates is the same list on the syntax.
//     The builder writes the emitter's unreachable panic only after a body
//     whose last statement does not terminate, so no statement follows a
//     terminating one (go vet: unreachable code);
//   - every local is read: a local declared and only written (a match's
//     binding that one case does not read, a catch's value a let _
//     drops, a scalar slot) gets `_ = x` after its declaration, as the
//     emitter writes it.

// irTerminates reports a terminating statement.
func irTerminates(s *irStmt) bool {
	switch s.K {
	case "ret", "fail", "panic", "goto":
		return true
	case "if":
		return len(s.Else) > 0 && irListTerminates(s.Then) && irListTerminates(s.Else)
	case "switch", "tswitch":
		def := len(s.Else) > 0
		if def && !irListTerminates(s.Else) {
			return false
		}
		for _, c := range s.Cases {
			def = def || c.Def
			if !irListTerminates(c.Body) || irBreaks(c.Body, "") {
				return false
			}
		}
		return def && !irBreaks(s.Else, "")
	case "block":
		return irListTerminates(s.Body) && (s.Label == "" || !irBreaks(s.Body, s.Label))
	case "while":
		return s.X == nil && !irBreaks(s.Body, "") && (s.Label == "" || !irBreaks(s.Body, s.Label))
	}
	return false
}

// irListTerminates reports a list whose last statement terminates.
func irListTerminates(list []*irStmt) bool {
	return len(list) > 0 && irTerminates(list[len(list)-1])
}

// irBreaks reports a break in list that leaves the statement list belongs
// to: one naming label, or with label "" an unlabeled one outside any
// nested loop or switch.
func irBreaks(list []*irStmt, label string) bool {
	for _, s := range list {
		if s.K == "break" && s.Label == label {
			return true
		}
		nested := label == "" && (s.K == "while" || s.K == "range" || s.K == "switch" || s.K == "tswitch")
		if nested {
			continue
		}
		if irBreaks(s.Then, label) || irBreaks(s.Else, label) || irBreaks(s.Body, label) {
			return true
		}
		for _, c := range s.Cases {
			if irBreaks(c.Body, label) {
				return true
			}
		}
	}
	return false
}

// irUseLocals puts `_ = x` after the declaration of each local x of body
// that no expression reads.
func irUseLocals(body []*irStmt) []*irStmt {
	read := map[int]bool{}
	var stmts func(list []*irStmt)
	var expr func(e *irExpr)
	expr = func(e *irExpr) {
		if e == nil {
			return
		}
		if e.K == "local" {
			read[e.I] = true
		}
		expr(e.X)
		expr(e.Y)
		for _, a := range e.Args {
			expr(a)
		}
		for _, a := range e.Elems {
			expr(a)
		}
		stmts(e.Body) // a function literal reads what its body reads
	}
	stmts = func(list []*irStmt) {
		for _, s := range list {
			expr(s.X)
			expr(s.Y)
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
			for _, c := range s.Cases {
				for _, v := range c.Vals {
					expr(v)
				}
				stmts(c.Body)
			}
		}
	}
	stmts(body)
	var mark func(list []*irStmt) []*irStmt
	mark = func(list []*irStmt) []*irStmt {
		var out []*irStmt
		for _, s := range list {
			s.Then, s.Else, s.Body = mark(s.Then), mark(s.Else), mark(s.Body)
			irLiterals(s, func(e *irExpr) { e.Body = mark(e.Body) })
			for _, c := range s.Cases {
				c.Body = mark(c.Body)
			}
			out = append(out, s)
			var declared []int
			switch s.K {
			case "def", "var":
				declared = []int{s.Local}
			case "def2":
				declared = []int{s.Local}
				if s.Val != nil {
					declared = append(declared, *s.Val)
				}
			}
			for _, l := range declared {
				if !read[l] {
					out = append(out, &irStmt{K: "discard", Pos: s.Pos, X: &irExpr{K: "local", Pos: s.Pos, I: l}})
				}
			}
		}
		return out
	}
	return mark(body)
}
