package check

import (
	"errors"
	"math/big"
	"strconv"

	"ergo.local/prototype/internal/sketch/syntax"
)

// A where's pattern (types.md, Refinements 11): `matches(it, `…`)`, a
// top-level conjunct of the where of a string, at most one. matches is a
// contextual word of a where, as where itself is: a function named
// matches stays a name everywhere else. The checker compiles the pattern
// (pattern.go) when it decides the where, and the calls in the derived
// ParseN and NWhere are CallPattern calls that the lowering writes as the
// compiled matcher.

// isMatchesCall reports a call of the contextual matches.
func isMatchesCall(x *syntax.CallExpr) bool {
	id, ok := x.Fun.(*syntax.Ident)
	return ok && id.Name == "matches"
}

// conjuncts are the operands of the top-level && chain of x.
func conjuncts(x syntax.Expr) []syntax.Expr {
	x = unparen(x)
	if b, ok := x.(*syntax.BinaryExpr); ok && b.Op == syntax.LAND {
		return append(conjuncts(b.X), conjuncts(b.Y)...)
	}
	return []syntax.Expr{x}
}

// pattern compiles the where's pattern conjunct, if any, and marks it the
// one set admits; false after a diagnostic.
func (r *refiner) pattern(where syntax.Expr, t Type) (*Matcher, bool) {
	var found *Matcher
	for _, x := range conjuncts(where) {
		call, ok := x.(*syntax.CallExpr)
		if !ok || !isMatchesCall(call) {
			continue
		}
		if r.allowed != nil {
			r.c.errorf(call, CodeType, "a where takes one pattern; join the two in one (types.md, Refinements 11)")
			return nil, false
		}
		r.allowed = call
		if b, ok := under(t).(*Basic); !ok || !isString(b) {
			r.c.errorf(call, CodeType, "a pattern refines a string; %s is not one (types.md, Refinements 11)", t)
			return nil, false
		}
		m, ok := r.c.compileWherePattern(call, r.subject)
		if !ok {
			return nil, false
		}
		found = m
	}
	return found, true
}

// compileWherePattern compiles the pattern of the call matches(subject,
// `…`), reporting a pattern outside the subset at its offset in the
// literal.
func (c *checker) compileWherePattern(call *syntax.CallExpr, subject string) (*Matcher, bool) {
	shape := "a pattern is matches(" + subject + ", `…`): the subject and a string literal (types.md, Refinements 11)"
	if len(call.Args) != 2 || call.Trailing != nil {
		c.errorf(call, CodeType, "%s", shape)
		return nil, false
	}
	if id, ok := unparen(call.Args[0]).(*syntax.Ident); !ok || id.Name != subject {
		c.errorf(call.Args[0], CodeType, "%s", shape)
		return nil, false
	}
	lit, ok := unparen(call.Args[1]).(*syntax.BasicLit)
	if !ok || lit.Kind != syntax.STRING {
		c.errorf(call.Args[1], CodeType, "%s", shape)
		return nil, false
	}
	src, err := strconv.Unquote(lit.Value)
	if err != nil {
		c.errorf(lit, CodeType, "%s", shape)
		return nil, false
	}
	m, err := CompilePattern(src)
	var pe *PatternError
	if errors.As(err, &pe) {
		at := literalPos(lit, pe.Offset)
		end := at + 1
		if end > lit.End() {
			end = at
		}
		c.errorIn(lit, at, end, CodeType, "%s (types.md, Refinements 11)", pe.Msg)
		return nil, false
	}
	return m, true
}

// literalPos is the position in the string literal lit of the byte at
// offset off of its decoded value.
func literalPos(lit *syntax.BasicLit, off int) syntax.Pos {
	v := lit.Value
	if len(v) > 0 && v[0] == '`' {
		return lit.Pos() + 1 + syntax.Pos(off)
	}
	// An interpreted literal: walk its escapes.
	at, decoded := 1, 0
	for at < len(v)-1 && decoded < off {
		r, multi, tail, err := strconv.UnquoteChar(v[at:len(v)-1], '"')
		if err != nil {
			break
		}
		if r < 0x80 || !multi {
			decoded++
		} else {
			decoded += len(string(r))
		}
		at = len(v) - 1 - len(tail)
	}
	return lit.Pos() + syntax.Pos(at)
}

// notePatternCalls records the matches calls of the declarations the
// where of d derives (its ParseN and NWhere), whose compiled pattern is m.
func (c *checker) notePatternCalls(f *fileInfo, d *syntax.TypeDecl, m *Matcher) {
	if c.patternCalls == nil {
		c.patternCalls = map[*syntax.CallExpr]*Matcher{}
	}
	for _, decl := range f.syntax.Decls {
		var derived *syntax.TypeDecl
		switch decl := decl.(type) {
		case *syntax.FuncDecl:
			derived = decl.Derived
		case *syntax.LawDecl:
			derived = decl.Derived
		}
		if derived != d {
			continue
		}
		syntax.Inspect(decl, func(n syntax.Node) bool {
			if call, ok := n.(*syntax.CallExpr); ok && isMatchesCall(call) {
				c.patternCalls[call] = m
			}
			return true
		})
	}
}

// patternCall checks a matches call of a derived declaration: true when
// m matches the whole of its first argument, a string.
func (c *checker) patternCall(x *syntax.CallExpr, m *Matcher) operand {
	arg := c.valueExpr(x.Args[0], nil)
	if arg.invalid() {
		return bad(x)
	}
	if b, ok := under(arg.typ).(*Basic); !ok || !isString(b) {
		panic("check: internal compiler error: a derived pattern call of a value that is no string")
	}
	c.valueExpr(x.Args[1], nil)
	call := c.callInfo(x)
	call.Kind, call.Pattern = CallPattern, m
	return operand{mode: modeValue, typ: Typ[Bool], expr: x}
}

// GenLengths are the byte lengths a generator draws for a refinement with
// a pattern (Refinements 9 and 11): in each span that starts at most
// 4096, from the first length the pattern reaches, at most 1024 past the
// span's start, to cap past that length, the lengths the pattern reaches,
// as closed spans. Empty when a span holds none.
func (r *Refinement) GenLengths(cap int) [][2]int64 {
	const ahead = 1024
	top := 0
	for _, s := range r.Spans {
		if s.Lo.IsInt64() && s.Lo.Int64() <= 4096 {
			top = max(top, int(s.Lo.Int64())+ahead+cap)
		}
	}
	reach := r.Pattern.Reach(top)
	var out [][2]int64
	for _, s := range r.Spans {
		if !s.Lo.IsInt64() || s.Lo.Int64() > 4096 {
			continue
		}
		lo := int(s.Lo.Int64())
		hi := top
		if s.Hi.IsInt64() && s.Hi.Int64() < int64(hi) {
			hi = int(s.Hi.Int64())
		}
		first := -1
		for k := lo; k <= min(hi, lo+ahead); k++ {
			if reach[k][0] {
				first = k
				break
			}
		}
		if first < 0 {
			continue
		}
		for k := first; k <= min(hi, first+cap); k++ {
			if !reach[k][0] {
				continue
			}
			if n := len(out); n > 0 && out[n-1][1] == int64(k-1) {
				out[n-1][1] = int64(k)
				continue
			}
			out = append(out, [2]int64{int64(k), int64(k)})
		}
	}
	return out
}

// patternFeasible reports some value whose length lies in set and that m
// matches: a length up to 1024 bytes past the low end of a span that
// starts at most 4096.
func patternFeasible(m *Matcher, set spanSet) bool {
	const ahead = 1024
	top := 0
	for _, s := range set {
		if s.Lo.IsInt64() && s.Lo.Int64() <= 4096 {
			top = max(top, int(s.Lo.Int64())+ahead)
		}
	}
	reach := m.Reach(top)
	for _, s := range set {
		for k := 0; k <= top; k++ {
			if s.Lo.Cmp(big.NewInt(int64(k))) <= 0 && s.Hi.Cmp(big.NewInt(int64(k))) >= 0 && reach[k][0] {
				return true
			}
		}
	}
	return false
}
