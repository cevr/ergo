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
//     emitter writes it;
//   - the positions where a block, a loop, an if or a switch opens and
//     closes a scope increase through the body, in the order Go's unified
//     writer meets them (gc's dwarfgen ScopeMarker: "non-monotonic scope
//     positions" is an internal compiler error). The builder places a
//     statement the lowering adds after a nested one (a scatter after the
//     match it takes apart) at its source statement's start, which is
//     before that nested statement's end; irScopesIncrease moves such a
//     position up to the last one before it (IRFIX-SCOPE).

// irTerminates reports a terminating statement.
func irTerminates(s *irStmt) bool {
	switch s.K {
	case "ret", "fail", "panic", "goto":
		return true
	case "if":
		return len(s.Else) > 0 && irListTerminates(s.Then) && irListTerminates(s.Else)
	case "switch", "tswitch", "match": // match: an IR form (ir_forms.go)
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
		nested := label == "" && (s.K == "while" || s.K == "range" || s.K == "switch" || s.K == "tswitch" || s.K == "match")
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

// irUseLocals drops body's unreachable statements (irReachable), then puts
// `_ = x` after the declaration of each local x of body that no expression
// reads. Every body's end passes here: an IR form still in it is erased
// first (ir_forms_erase.go).
func irUseLocals(body []*irStmt) []*irStmt {
	body = irReachable(eraseForms(body, nil))
	// A label left off can leave statements after a terminating one
	// unreachable, and dropping them can leave another label unnamed.
	for dropped := true; dropped; {
		body, dropped = irLabelsNamed(body)
		if dropped {
			body = irReachable(body)
		}
	}
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
			case "def2", "mapget":
				declared = []int{s.Local}
				if s.Val != nil {
					declared = append(declared, *s.Val)
				}
			case "tdef":
				for _, l := range s.Ls {
					if l >= 0 {
						declared = append(declared, l)
					}
				}
			case "range":
				// A range variable is read in the body, or there.
				var use []*irStmt
				for _, l := range []*int{s.Key, s.Val} {
					if l != nil && !read[*l] {
						use = append(use, &irStmt{K: "discard", Pos: s.Pos, X: &irExpr{K: "local", Pos: s.Pos, I: *l}})
					}
				}
				s.Body = append(use, s.Body...)
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

// irLabelsNamed is body with the labels no break, continue or goto names
// left off (a function literal's labels are its own): irReachable drops a
// goto after a statement that terminates, as an inlined body's jump to
// its end (inl…) or a failure's to its arm (fj…), and the label it named
// would be one Go refuses, defined and not used, in the body's Go text.
// dropped reports a label left off.
func irLabelsNamed(body []*irStmt) (_ []*irStmt, dropped bool) {
	named := map[string]bool{}
	var walk func(list []*irStmt, f func(*irStmt))
	walk = func(list []*irStmt, f func(*irStmt)) {
		for _, s := range list {
			f(s)
			walk(s.Then, f)
			walk(s.Else, f)
			walk(s.Body, f)
			for _, c := range s.Cases {
				walk(c.Body, f)
			}
		}
	}
	walk(body, func(s *irStmt) {
		if s.K == "break" || s.K == "continue" || s.K == "goto" {
			named[s.Label] = true
		}
	})
	walk(body, func(s *irStmt) {
		if s.Label != "" && s.K != "break" && s.K != "continue" && s.K != "goto" && !named[s.Label] {
			s.Label, dropped = "", true
		}
		irLiterals(s, func(e *irExpr) {
			var d bool
			e.Body, d = irLabelsNamed(e.Body)
			dropped = dropped || d
		})
	})
	return body, dropped
}

// irReachable is list without the statements after one that terminates,
// up to a labeled one, which a goto reaches: they never run, the Go text
// of the body would be unreachable code to go vet, and the decoder would
// compile them all the same (a return after an endless loop).
func irReachable(list []*irStmt) []*irStmt {
	out := list[:0]
	dead := false
	for _, s := range list {
		if dead && (s.Label == "" || s.K == "break" || s.K == "continue" || s.K == "goto") {
			continue
		}
		s.Then, s.Else, s.Body = irReachable(s.Then), irReachable(s.Else), irReachable(s.Body)
		irLiterals(s, func(e *irExpr) { e.Body = irReachable(e.Body) })
		for _, c := range s.Cases {
			c.Body = irReachable(c.Body)
		}
		out = append(out, s)
		dead = irTerminates(s)
	}
	return out
}

// irScopesIncrease makes the scope positions of body increase, in the
// decoder's order: an if opens at Pos, its then block ends at End and its
// else block runs ElsePos..ElseEnd; a switch opens at Pos, each clause at
// its Pos (a default without a case of its own at ElsePos, last) and it
// closes at End; a loop or a block runs Pos..End. A position before the
// last one met, in the same file, becomes that one. A function literal's
// body is a function of its own (its own scope marks).
func irScopesIncrease(body []*irStmt, start irPos) []*irStmt {
	return irScopesIncreaseTagged(body, nil, start)
}

// irScopesIncreaseTagged is irScopesIncrease where the files tagged are an
// inlined body's (ir_forms_inline.go): Go orders positions of two files by
// their names, and a tagged name sorts after its caller's, so each scope
// position in a tagged file takes the last one before it (start before
// any, or a literal's own position in its body).
func irScopesIncreaseTagged(body []*irStmt, tagged map[int]bool, start irPos) []*irStmt {
	var last irPos
	// place gives a node the lowering made with no position of its own
	// (an else, a type switch's default, a statement or an operand it
	// synthesized) the last scope position met, or start, the function's
	// or the literal's: Go's compile stops at an unknown scope position
	// and files an error at no place (JOBS-ICE). Every body passes here
	// (irPlaced refuses what is still unplaced).
	place := func(p *irPos) {
		if p[1] > 0 {
			return
		}
		if last[1] > 0 {
			*p = last
		} else {
			*p = start
		}
	}
	var operands func(e *irExpr, at irPos)
	operands = func(e *irExpr, at irPos) {
		if e == nil {
			return
		}
		if e.Pos[1] <= 0 {
			e.Pos = at
		}
		operands(e.X, e.Pos)
		operands(e.Y, e.Pos)
		for _, a := range e.Args {
			operands(a, e.Pos)
		}
		for _, a := range e.Elems {
			operands(a, e.Pos)
		}
	}
	at := func(p *irPos) {
		place(p)
		if tagged[p[0]] {
			if last == (irPos{}) {
				*p = start
			} else {
				*p = last
			}
		}
		if p[0] == last[0] && (p[1] < last[1] || p[1] == last[1] && p[2] < last[2]) {
			*p = last
		}
		last = *p
	}
	var stmts func(list []*irStmt)
	stmts = func(list []*irStmt) {
		for _, s := range list {
			place(&s.Pos)
			operands(s.X, s.Pos)
			operands(s.Y, s.Pos)
			for _, c := range s.Cases {
				for _, v := range c.Vals {
					operands(v, s.Pos) // the clause is placed below (at)
				}
			}
			irLiterals(s, func(e *irExpr) {
				saved, savedStart := last, start
				last, start = irPos{}, e.Pos
				stmts(e.Body)
				last, start = saved, savedStart
			})
			switch s.K {
			case "if":
				at(&s.Pos)
				stmts(s.Then)
				at(&s.End)
				if s.Else != nil {
					at(&s.ElsePos)
					stmts(s.Else)
					at(&s.ElseEnd)
				}
			case "switch", "tswitch":
				at(&s.Pos)
				def := false
				for _, c := range s.Cases {
					at(&c.Pos)
					def = def || c.Def
					stmts(c.Body)
				}
				if !def {
					at(&s.ElsePos)
					stmts(s.Else)
				}
				at(&s.End)
			case "while", "count", "range", "block":
				at(&s.Pos)
				stmts(s.Body)
				at(&s.End)
			default:
				stmts(s.Then)
				stmts(s.Else)
				stmts(s.Body)
			}
		}
	}
	stmts(body)
	return body
}
