package syntax

// Nested or-patterns (FK7a). A case's top level lists its alternatives
// with commas, as Go's `case a, b:` does; inside a tuple element, a record
// pattern's field and parentheses, where a comma means the next element,
// `|` separates alternatives: `case (Plan | Supplies, Cancel):`. The
// parser expands such a case into the patterns it means, one per
// combination of alternatives, in order (`case (Plan, Cancel), (Supplies,
// Cancel):`), so the checker's rules for a case with several patterns
// (rows.md, Totality 1: each binds the same names, reachability,
// totality) and the lowering hold for it unchanged. The written patterns
// stay on the clause for the formatter.
//
// Prior art: OCaml, Rust (since 1.53) and F# allow `|` at any depth of a
// pattern; Gleam and Elixir allow alternatives only at the top of a clause
// (Gleam `1 | 2 ->`, Elixir none: a guard or a second clause); Go's switch
// lists them with commas. Rust's compiler also expands an or-pattern into
// its alternatives before building the decision tree (match lowering's
// candidate splitting), as here.

// maxAlternatives bounds the patterns one case expands to: the product of
// its alternatives grows with each nested |, and a case of more is
// clearer split.
const maxAlternatives = 64

// parseAlternatives parses `Pattern { "|" Pattern }` where a nested
// pattern stands. Outside a case's patterns a | is S001.
func (p *parser) parseAlternatives() Expr {
	first := p.parsePattern()
	if p.tok.tok != OR {
		return first
	}
	if !p.orAllowed {
		p.errorf("alternatives with | stand only in a case's patterns; a let or for pattern matches one shape")
		return first
	}
	or := &OrPattern{Alts: []Expr{first}}
	for p.tok.tok == OR {
		or.Bars = append(or.Bars, p.tok.pos)
		p.next()
		or.Alts = append(or.Alts, p.parsePattern())
	}
	return or
}

// expandCase is a case's patterns and, when one holds an OrPattern, the
// patterns as written: the first result is then the expansion.
func (p *parser) expandCase(list []Expr) ([]Expr, []Expr) {
	has := false
	for _, x := range list {
		if holdsOr(x) {
			has = true
			break
		}
	}
	if !has {
		return list, nil
	}
	var out []Expr
	for _, x := range list {
		out = append(out, expandPattern(x)...)
	}
	if len(out) > maxAlternatives {
		p.errorAt(list[0].Pos(), int(list[len(list)-1].End()-list[0].Pos()), "this case expands to %d patterns, more than %d: split it into several cases", len(out), maxAlternatives)
	}
	return out, list
}

// holdsOr reports whether the pattern x holds an OrPattern.
func holdsOr(x Expr) bool {
	found := false
	Inspect(x, func(n Node) bool {
		if _, ok := n.(*OrPattern); ok {
			found = true
		}
		return !found
	})
	return found
}

// expandPattern is the patterns x means, one per combination of its
// alternatives, left to right. A node that holds no OrPattern is shared,
// not copied; a node above one is copied with its expanded children.
func expandPattern(x Expr) []Expr {
	switch x := x.(type) {
	case *OrPattern:
		var out []Expr
		for _, a := range x.Alts {
			out = append(out, expandPattern(a)...)
		}
		return out
	case *TupleExpr:
		return product(x.Elts, func(elts []Expr) Expr {
			c := *x
			c.Elts = elts
			return &c
		}, x)
	case *CompositeLit:
		return product(x.Elts, func(elts []Expr) Expr {
			c := *x
			c.Elts = elts
			return &c
		}, x)
	case *KeyValueExpr:
		return each(x.Value, x, func(v Expr) Expr {
			c := *x
			c.Value = v
			return &c
		})
	case *AsPattern:
		return each(x.X, x, func(v Expr) Expr {
			c := *x
			c.X = v
			return &c
		})
	case *ParenExpr:
		return each(x.X, x, func(v Expr) Expr {
			c := *x
			c.X = v
			return &c
		})
	}
	return []Expr{x}
}

// each is x's copies, one per pattern its child means; x itself when the
// child holds no alternative.
func each(child, x Expr, mk func(Expr) Expr) []Expr {
	alts := expandPattern(child)
	if len(alts) == 1 && alts[0] == child {
		return []Expr{x}
	}
	out := make([]Expr, len(alts))
	for i, a := range alts {
		out[i] = mk(a)
	}
	return out
}

// product is one copy of x per combination of its elements' patterns,
// the first element varying slowest; x itself when no element holds an
// alternative.
func product(elts []Expr, mk func([]Expr) Expr, x Expr) []Expr {
	combos := [][]Expr{nil}
	same := true
	for _, e := range elts {
		alts := expandPattern(e)
		if len(alts) != 1 || alts[0] != e {
			same = false
		}
		var next [][]Expr
		for _, c := range combos {
			for _, a := range alts {
				next = append(next, append(append([]Expr(nil), c...), a))
			}
		}
		combos = next
	}
	if same {
		return []Expr{x}
	}
	out := make([]Expr, len(combos))
	for i, c := range combos {
		out[i] = mk(c)
	}
	return out
}
