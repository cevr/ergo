package lower

import (
	"go/scanner"
	"go/token"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Value forms (OPT5). Go copies a struct of more than four words through
// memory at every assignment, and the lowering wrote one assignment per
// step of a value's way: the inlined read, the catch around it, the
// inlined helper's return, the let that names it. lru copied its 48-byte
// node four times per hit where hand Go copies it at most once
// (attribution, wf10 OPT4). The forms below remove copies and stores that
// no Ergo program can observe; every Ergo value keeps its value semantics.
//
// A fresh temporary is a Go local the lowering declares for one value (a
// call's result, an inlined call's result, a catch's value) and writes no
// more once that value is complete. Then:
//   - a let (not let mut) of a fresh temporary names the temporary: the let
//     is a second name of one value no write reaches (aliasLet);
//   - a catch whose arms all leave (a return, a failure, an Unreachable)
//     has the value of its operand, a fresh temporary of the same Go type:
//     no temporary of its own (catchAlias);
//   - an inlined body whose last statement returns a temporary it declared
//     at its top, with no other return of a value, writes that value into
//     the call's result variable itself (renameTail).
//
// A constant package let of an integer, bool or string type is a Go const,
// read through v1.Let so Go never folds an expression over it at build time
// (constLet, decls.go; wf10 1BRC and OPT5 as one rule).
//
// The reference is formLowering "reference": the forms as they were before
// OPT5. It is set by a test, or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.formLowering=reference.
// TestFormsAgree runs generated programs under both.
//
// Prior art: OCaml's flambda and GHC's simplifier substitute a let-bound
// variable whose value is a variable (copy propagation) before code
// generation; ReScript writes a let of an immutable value as a JavaScript
// const of the same value. Go's own SSA does it only for structs of at most
// four words, which is why the lowering does it for the rest.
var formLowering string

// formsOn reports whether the OPT5 forms are on.
func formsOn() bool { return formLowering != "reference" }

// freshKey is a fresh temporary: its name in its Go function's namer.
type freshKey struct {
	scope *namer
	name  string
}

// markFresh records name, a temporary fc just wrote, as fresh.
func (fc *funcCtx) markFresh(name string) {
	if name == "" || !formsOn() {
		return
	}
	if fc.l.fresh == nil {
		fc.l.fresh = map[freshKey]bool{}
	}
	fc.l.fresh[freshKey{fc.scope, name}] = true
}

// isFresh reports whether val is a fresh temporary of fc's Go function.
func (fc *funcCtx) isFresh(val string) bool {
	return formsOn() && fc.l.fresh[freshKey{fc.scope, val}]
}

// aliasLet names v by val, the let's lowered value, when val is a fresh
// temporary, and reports whether it did. The let is the temporary's one
// reader: it is fresh no more, so a later let of v copies it, and a let
// mut that names it writes a value no one else sees. A conversion of the
// value (Info.Implicit) or the value rule's copy makes val no bare
// temporary.
func (fc *funcCtx) aliasLet(v *check.Var, val string) bool {
	if !fc.isFresh(val) {
		return false
	}
	delete(fc.l.fresh, freshKey{fc.scope, val})
	fc.locals[v] = val
	fc.l.stats.Aliased++
	return true
}

// catchAlias reports whether the catch x of operand o has o's value: every
// arm leaves, so the catch's type is its operand's, and o.val is a fresh
// temporary.
func (fc *funcCtx) catchAlias(x *syntax.CatchExpr, m *check.Match, o operand) bool {
	if m.Result == nil || o.val == "" || !fc.isFresh(o.val) {
		return false
	}
	for _, a := range x.Arms {
		if !fc.leaves(a.Body) {
			return false
		}
	}
	fc.l.stats.Aliased++
	return true
}

// leaves reports whether list ends in a statement that leaves its block: a
// return, or a call of Unreachable.
func (fc *funcCtx) leaves(list []syntax.Stmt) bool {
	if len(list) == 0 {
		return false
	}
	switch s := list[len(list)-1].(type) {
	case *syntax.ReturnStmt:
		return true
	case *syntax.ExprStmt:
		call, ok := unparen(s.X).(*syntax.CallExpr)
		return ok && check.IsUnreachable(fc.l.info.Calls[call])
	}
	return false
}

// renameTail rewrites body, an inlined body whose result variable res has
// the Go type resType, when its last statement is "res = X" and X a
// variable the body declares at its top ("var X T" with T resType, or
// "X := e" when res is not an interface, to which Go assigns a variant
// with no conversion): X becomes res, its declaration an assignment, and
// the last statement goes. Another return of the body sets res (or, for a failure, the error
// alone, after which the caller does not read res) and jumps past the
// block, so no path reads res after a value of X went in. It does not
// rename when the body has a function literal, which could keep X past
// the body, or a key or label spelled X, and it renames no field or
// method spelled X.
func renameTail(body, res, resType string, iface bool) (string, bool) {
	fset := token.NewFileSet()
	src := []byte(body)
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	type tok struct {
		off   int
		tok   token.Token
		lit   string
		depth int
	}
	var toks []tok
	depth := 0
	for {
		pos, t, lit := s.Scan()
		if t == token.EOF {
			break
		}
		if t == token.RBRACE {
			depth--
		}
		toks = append(toks, tok{file.Offset(pos), t, lit, depth})
		if t == token.LBRACE {
			depth++
		}
		if t == token.FUNC || t == token.ILLEGAL {
			return body, false
		}
	}
	n := len(toks)
	if n < 4 || toks[n-1].tok != token.SEMICOLON || toks[n-2].tok != token.IDENT || toks[n-3].tok != token.ASSIGN || toks[n-4].tok != token.IDENT || toks[n-4].lit != res || toks[n-4].depth != 0 {
		return body, false
	}
	if n > 4 && toks[n-5].tok != token.SEMICOLON {
		return body, false
	}
	x := toks[n-2].lit
	if x == res {
		return body, false
	}
	decl := -1 // the index of X's declaring token (VAR, or X before :=)
	for i, t := range toks[:n-4] {
		if t.tok != token.IDENT || t.lit != x || (i > 0 && toks[i-1].tok == token.PERIOD) {
			continue // a selector names a field or method, not X
		}
		if toks[i+1].tok == token.COLON {
			return body, false // a key or label spelled X
		}
		start := i == 0 || toks[i-1].tok == token.SEMICOLON
		switch {
		case i > 0 && toks[i-1].tok == token.VAR && t.depth == 0:
			if decl >= 0 {
				return body, false
			}
			decl = i - 1
		case start && toks[i+1].tok == token.DEFINE && t.depth == 0 && !iface:
			if decl >= 0 {
				return body, false
			}
			decl = i
		}
	}
	if decl < 0 {
		return body, false
	}
	type edit struct {
		from, to int
		text     string
	}
	var edits []edit
	if toks[decl].tok == token.VAR {
		// var X T, or var X T = e: the type is the text up to = or the end.
		end := decl + 2
		for end < n && toks[end].tok != token.ASSIGN && toks[end].tok != token.SEMICOLON {
			end++
		}
		if end == n || strings.TrimSpace(body[toks[decl+2].off:toks[end].off]) != resType {
			return body, false
		}
		if toks[end].tok == token.ASSIGN {
			edits = append(edits, edit{toks[decl].off, toks[end].off, res + " "})
		} else {
			edits = append(edits, edit{toks[decl].off, toks[end].off, ""})
		}
	} else {
		edits = append(edits, edit{toks[decl].off, toks[decl+1].off + len(":="), res + " ="})
	}
	for i, t := range toks[:n-4] {
		if t.tok == token.IDENT && t.lit == x && i != decl && !(toks[decl].tok == token.VAR && i == decl+1) && (i == 0 || toks[i-1].tok != token.PERIOD) {
			edits = append(edits, edit{t.off, t.off + len(x), res})
		}
	}
	// The last statement, up to its line's end.
	last := toks[n-4].off
	end := strings.IndexByte(body[last:], '\n')
	if end < 0 {
		end = len(body) - last
	}
	edits = append(edits, edit{last, last + end, ""})
	var b strings.Builder
	at := 0
	sort.Slice(edits, func(i, j int) bool { return edits[i].from < edits[j].from })
	for _, e := range edits {
		b.WriteString(body[at:e.from])
		b.WriteString(e.text)
		at = e.to
	}
	b.WriteString(body[at:])
	return b.String(), true
}

// soleFailure reports whether the failure row of the catch x's operand, a
// direct call of an Ergo function, is the one variant that the catch's
// one arm names, and the callee's frame has an empty footprint: a closed
// row of one variant, so the operand's error, when not nil, is that
// variant, and the arm needs no type switch (the error's nil test is the
// flag) and the catch no default for what passes through. An interruption
// can come back as an error value (a task's Wait, a queue's Push, a
// clock's Sleep return rt.Interrupted), so a callee with a capability, a
// task start or an opaque call keeps the switch and its re-panicking
// default (rows.md, Catch 6); a frame with an empty footprint runs none
// of them, at any depth, and its defects leave as panics.
func (fc *funcCtx) soleFailure(x *syntax.CatchExpr) bool {
	if !formsOn() {
		return false
	}
	call, ok := unparen(x.X).(*syntax.CallExpr)
	if !ok {
		return false
	}
	c := fc.l.info.Calls[call]
	if c == nil || c.Fails == nil || c.StartsTask || check.IsUnreachable(c) {
		return false
	}
	f := c.Func
	if f == nil || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		return false // an extern, an operation, a function value
	}
	if fp, ok := f.Pkg.Info.Footprints[f.Decl]; !ok || !fp.Empty() {
		return false // an interruption may come back as its error
	}
	if len(c.Fails.Elems()) != 1 {
		return false // the checker keeps an arm's type in the row
	}
	if n, ok := c.Fails.Elems()[0].(*check.Named); ok && check.IsGoError(n) {
		// go.Error is any Go error; the switch and its default stay, as
		// catch writes them for an Ergo call (TestPlainCatchAgrees).
		return false
	}
	fc.l.stats.SoleFailures++
	return true
}

// mapStore reports whether the assignment s is p = p.f(…) where p is a Map
// place written as a variable and its fields, and the call one the value
// rule lets write p in place (values.go, inPlace): the Go map it returns is p's
// own map, whose header an in-place write keeps, so storing it back into
// p changes nothing and the call is written as a statement. lru's eviction
// stored its index back, a write-barrier check per eviction.
func (fc *funcCtx) mapStore(s *syntax.AssignStmt) bool {
	if !formsOn() || s.Tok != syntax.ASSIGN || len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return false
	}
	call, ok := unparen(s.Rhs[0]).(*syntax.CallExpr)
	if !ok || !fc.l.valueFacts().inPlace[call] {
		return false
	}
	c := fc.l.info.Calls[call]
	recv := c.Recv
	if recv == nil && len(call.Args) > 0 {
		recv = call.Args[0]
	}
	if recv == nil || !samePlace(s.Lhs[0], recv) {
		return false // the call writes another map, which p takes
	}
	if _, isMap := fc.typeOf(s.Lhs[0]).(*check.Map); !isMap {
		return false // a slice's header changes: its store back stays
	}
	// samePlace admits a variable and its fields only, never an element,
	// whose store into a Map may add its entry.
	fc.l.stats.MapStores++
	return true
}
