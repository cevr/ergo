package lower

import (
	"go/constant"
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
// A package let of a literal, read only where Go's constant folding
// cannot fail, is a Go const read by its bare name (constFold).
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
//
// renameTail first reads the body's last line alone (tailMayAssign), and
// scans the whole body only when the last statement can be "res = X":
// most inlined bodies end otherwise, and the scan of each was 10 % of
// the lowering's bytes (SL9b, LW1).
func renameTail(body, res, resType string, iface bool) (string, bool) {
	if !tailMayAssign(body, res) {
		return body, false
	}
	return renameTailScan(body, res, resType, iface)
}

// tailMayAssign reports false only when body's last statement is not
// "res = X" by renameTailScan's test: its last four tokens are not res, =,
// an identifier and a semicolon. Without a raw string or a block comment
// no token spans two lines, so each line scans alone as it does in the
// whole body, and its last token decides its implied semicolon. The last
// line with a token is read: four or more tokens hold the last four; two
// (X;) can follow a line "res ="; three (A X;) cannot complete "res = X",
// since a line that ends with res ends its statement there. A body with
// a block comment is not judged here, nor one whose last line does not
// scan alone: the line that closes a raw string is such a line.
func tailMayAssign(body, res string) bool {
	if strings.Contains(body, "/*") {
		return true
	}
	rest := strings.TrimRight(body, "\n")
	for rest != "" {
		line := rest
		if i := strings.LastIndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[i+1:], rest[:i]
		} else {
			rest = ""
		}
		toks, ok := lineTokens(line)
		if !ok {
			return true
		}
		if len(toks) == 0 {
			continue
		}
		n := len(toks)
		switch {
		case n == 2:
			return toks[0].tok == token.IDENT && toks[1].tok == token.SEMICOLON
		case n >= 4:
			return toks[n-1].tok == token.SEMICOLON && toks[n-2].tok == token.IDENT && toks[n-3].tok == token.ASSIGN && toks[n-4].tok == token.IDENT && toks[n-4].lit == res
		}
		return false
	}
	return false
}

type lineTok struct {
	tok token.Token
	lit string
}

// lineTokens are the tokens of one line of Go, comments left out, and
// whether it scanned with no error.
func lineTokens(line string) ([]lineTok, bool) {
	fset := token.NewFileSet()
	src := []byte(line)
	file := fset.AddFile("", fset.Base(), len(src))
	ok := true
	var s scanner.Scanner
	s.Init(file, src, func(token.Position, string) { ok = false }, 0)
	var out []lineTok
	for {
		_, t, lit := s.Scan()
		if t == token.EOF {
			break
		}
		if t == token.ILLEGAL {
			ok = false
		}
		out = append(out, lineTok{t, lit})
	}
	return out, ok
}

// renameTailScan is renameTail's scan of the whole body.
func renameTailScan(body, res, resType string, iface bool) (string, bool) {
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
	if !fc.mapStoreForm(s) {
		return false
	}
	fc.l.stats.MapStores++
	return true
}

// mapStoreForm is mapStore's decision alone, which the IR builder reads
// too (ir_place.go, store): the stat counts the emitter's stores only.
func (fc *funcCtx) mapStoreForm(s *syntax.AssignStmt) bool {
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
	return true
}

// constFold reports whether the let v (a package let, or a local let:
// localConst), whose value is a checked constant, is written as a Go
// const. For a package let, Go then reads its value from the
// instruction, not from memory (lru's occupied mark, read at every
// generation check). Go folds an expression whose operands are all
// constants at compile time, where a division by zero, an overflow, a
// negative index or a conversion that does not fit is a build error and
// not the run's defect or wrap, so v is a const only when each of its
// uses, in every package of the program, is in a place where no such fold
// can fail (constUses).
func (l *lowerer) constFold(v *check.Var) bool {
	if !formsOn() || v == nil {
		return false
	}
	if l.letUses == nil {
		// One walk of every package counts each package let's uses and
		// its uses where a constant is safe.
		l.letUses = map[*check.Var][2]int{}
		saved := l.info
		defer func() { l.info = saved }()
		seen := map[*check.Package]bool{}
		for _, p := range append(l.libraries(), l.pkg) {
			if seen[p] {
				continue
			}
			seen[p] = true
			l.info = p.Info
			for id, o := range l.info.Uses {
				// Every name's reads: a package let's, and a local let's
				// (localConst), whose declaring name is a Uses entry too.
				if u, ok := o.(*check.Var); ok && l.info.Defs[id] == nil {
					c := l.letUses[u]
					c[0]++
					l.letUses[u] = c
				}
			}
			// A read is counted once, though it may be both a place's
			// operand and inside a larger constant expression in a place.
			safe := map[*syntax.Ident]bool{}
			for _, f := range p.Files() {
				syntax.Inspect(f, func(n syntax.Node) bool {
					for _, x := range l.constUses(n) {
						l.markSafe(x, safe)
					}
					return true
				})
			}
			for id := range safe {
				if u, ok := l.info.Uses[id].(*check.Var); ok {
					if c, counted := l.letUses[u]; counted {
						c[1]++
						l.letUses[u] = c
					}
				}
			}
		}
	}
	c := l.letUses[v]
	return c[0] == c[1]
}

// localConst reports whether the local let v is written as a Go const
// (funcCtx.let): a let, not a let mut, of one name whose initializer is a
// checked constant of a boolean, string or integer type (constLet's kinds),
// every read of it in a place where no Go fold can fail (constFold). gc
// propagates such a variable's value as it does a constant's, but counts
// its declaration against the inlining budget (`var k int = 3` costs 5,
// `const k int = 3` nothing: LOCAL-CONST red-check.md).
func (l *lowerer) localConst(v *check.Var) bool {
	if _, ok := l.localLets()[v]; !ok {
		return false
	}
	return l.constFold(v)
}

// localLets are the local lets of every package of the program that may be
// Go constants, by their values: constLike treats a read of one as a
// constant, as it treats a package let, whether or not it becomes one.
func (l *lowerer) localLets() map[*check.Var]constant.Value {
	if l.localVals != nil {
		return l.localVals
	}
	l.localVals = map[*check.Var]constant.Value{}
	if !formsOn() {
		return l.localVals // the OPT5 reference keeps every let a variable
	}
	saved := l.info
	defer func() { l.info = saved }()
	seen := map[*check.Package]bool{}
	for _, p := range append(l.libraries(), l.pkg) {
		if seen[p] {
			continue
		}
		seen[p] = true
		l.info = p.Info
		for _, f := range p.Files() {
			for _, d := range f.Decls {
				if _, pkgLet := d.(*syntax.VarDecl); pkgLet {
					continue
				}
				syntax.Inspect(d, func(n syntax.Node) bool {
					d, ok := n.(*syntax.VarDecl)
					if !ok || d.Mut.IsValid() {
						return true
					}
					for _, spec := range d.Specs {
						if len(spec.Names) != 1 {
							continue
						}
						if v, ok := l.info.Defs[spec.Names[0]].(*check.Var); ok && spec.Names[0].Name != "_" && l.constLet(spec, v) {
							l.localVals[v] = l.info.Values[spec.Values[0]]
						}
					}
					return true
				})
			}
		}
	}
	return l.localVals
}

// constUses are the operands of n that a constant may take with no
// compile-time fold that can fail: a comparison's operands, an operand of
// + - * & | ^ &^ whose other operand is not constant, a dividend, a
// divisor that is a constant other than zero under a variable dividend,
// a shifted constant under a variable count, a constant count in range
// under a variable shifted value, a constant string indexed by a
// variable, an interpolated value, an assigned, bound, returned or yielded value, a composite
// literal's element value and an argument of a call that is not a
// conversion. An operand that is a larger constant expression is a safe
// place for its lets only when no fold inside it can fail (markSafe).
func (l *lowerer) constUses(n syntax.Node) []syntax.Expr {
	switch n := n.(type) {
	case *syntax.BinaryExpr:
		switch n.Op {
		case syntax.EQL, syntax.NEQ, syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ:
			return []syntax.Expr{n.X, n.Y}
		case syntax.ADD, syntax.SUB, syntax.MUL, syntax.AND, syntax.OR, syntax.XOR, syntax.AND_NOT:
			var out []syntax.Expr
			if !l.constLike(n.Y) {
				out = append(out, n.X)
			}
			if !l.constLike(n.X) {
				out = append(out, n.Y)
			}
			return out
		case syntax.SHL, syntax.SHR:
			// A constant shifted by a variable count; a variable shifted
			// by a constant count in range (Go refuses a negative one).
			if !l.constLike(n.Y) {
				return []syntax.Expr{n.X}
			}
			if !l.constLike(n.X) && l.countInRange(n) {
				return []syntax.Expr{n.Y}
			}
		case syntax.QUO, syntax.REM:
			if !l.constLike(n.Y) {
				return []syntax.Expr{n.X}
			}
			// A variable divided by a constant that is not zero.
			if v := l.constValue(n.Y); !l.constLike(n.X) && v != nil && v.Kind() == constant.Int && constant.Sign(v) != 0 {
				return []syntax.Expr{n.Y}
			}
		}
	case *syntax.InterpolatedString:
		// A value's text: FormatInt of it, or the string itself joined
		// by + (funcCtx.text), neither of which a constant can fail.
		return n.Exprs
	case *syntax.IndexExpr:
		// A constant string indexed by a variable: only a constant index
		// out of range fails a fold.
		if b, ok := l.info.Types[n.X].(*check.Basic); ok && b.Kind == check.String && len(n.Indices) == 1 && !l.constLike(n.Indices[0]) {
			return []syntax.Expr{n.X}
		}
	case *syntax.AssignStmt:
		switch n.Tok {
		case syntax.ASSIGN, syntax.ADD_ASSIGN, syntax.SUB_ASSIGN, syntax.MUL_ASSIGN:
			return n.Rhs
		}
	case *syntax.VarDecl:
		var out []syntax.Expr
		for _, s := range n.Specs {
			out = append(out, s.Values...)
		}
		return out
	case *syntax.ReturnStmt:
		return n.Results
	case *syntax.ExprStmt:
		return []syntax.Expr{n.X}
	case *syntax.KeyValueExpr:
		return []syntax.Expr{n.Value}
	case *syntax.CompositeLit:
		var out []syntax.Expr
		for _, e := range n.Elts {
			if _, kv := e.(*syntax.KeyValueExpr); !kv {
				out = append(out, e)
			}
		}
		return out
	case *syntax.CallExpr:
		if c := l.info.Calls[n]; c != nil {
			switch c.Kind {
			case check.CallFunc, check.CallOp, check.CallProviderOp, check.CallFuncValue, check.CallLawOp:
				return n.Args
			}
		}
	}
	return nil
}

// constLike reports whether Go may see x as a constant: a checked
// constant, a package let, a local let of a constant (localLets), or an
// operator, len or conversion over such.
func (l *lowerer) constLike(x syntax.Expr) bool {
	if _, ok := l.info.Values[x]; ok {
		return true
	}
	switch x := x.(type) {
	case *syntax.Ident:
		v, ok := l.info.Uses[x].(*check.Var)
		_, pkgLet := l.vars[v]
		_, local := l.localLets()[v]
		return ok && (pkgLet || local)
	case *syntax.ParenExpr:
		return l.constLike(x.X)
	case *syntax.UnaryExpr:
		return l.constLike(x.X)
	case *syntax.BinaryExpr:
		return l.constLike(x.X) && l.constLike(x.Y)
	case *syntax.CallExpr:
		if c := l.info.Calls[x]; c != nil && (c.Kind == check.CallLen || c.Kind == check.CallConversion) {
			for _, a := range x.Args {
				if !l.constLike(a) {
					return false
				}
			}
			return true
		}
	}
	return false
}

// markSafe records the let reads of x, an operand in a place where a
// constant is safe (constUses): x itself when it is a name, else every
// name in x when x is a constant expression none of whose folds can fail
// (foldSafe), as `clsLetter|clsDigit` and `int(limit)` are.
func (l *lowerer) markSafe(x syntax.Expr, safe map[*syntax.Ident]bool) {
	x = unparen(x)
	if id, ok := x.(*syntax.Ident); ok {
		safe[id] = true
		return
	}
	if !l.constLike(x) || !l.foldSafe(x) {
		return
	}
	syntax.Inspect(x, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			safe[id] = true
		}
		return true
	})
}

// foldSafe reports whether Go's fold of the constant expression x cannot
// fail: its leaves are names and checked literals, and its operators are
// & | ^ &^ and comparisons, a right shift by a constant count in range,
// and a conversion of a value that fits its integer type (or of a bool or
// string to its own kind). Any other operator, as + - * / % << and a
// unary minus, may overflow or divide by zero and is not safe.
func (l *lowerer) foldSafe(x syntax.Expr) bool {
	switch x := x.(type) {
	case *syntax.Ident:
		return true
	case *syntax.BasicLit:
		_, ok := l.info.Values[x]
		return ok
	case *syntax.ParenExpr:
		return l.foldSafe(x.X)
	case *syntax.BinaryExpr:
		switch x.Op {
		case syntax.AND, syntax.OR, syntax.XOR, syntax.AND_NOT,
			syntax.EQL, syntax.NEQ, syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ:
			return l.foldSafe(x.X) && l.foldSafe(x.Y)
		case syntax.SHR:
			return l.foldSafe(x.X) && l.foldSafe(x.Y) && l.countInRange(x)
		}
	case *syntax.CallExpr:
		if c := l.info.Calls[x]; c != nil && c.Kind == check.CallConversion && len(x.Args) == 1 {
			return l.foldSafe(x.Args[0]) && l.fits(x.Args[0], l.info.Types[x])
		}
	}
	return false
}

// countInRange reports whether the shift s has a constant count in [0,
// the bit width of its shifted operand's type).
func (l *lowerer) countInRange(s *syntax.BinaryExpr) bool {
	bits := intBits(l.info.Types[s.X])
	c := l.constValue(s.Y)
	if bits == 0 || c == nil || c.Kind() != constant.Int {
		return false
	}
	n, exact := constant.Int64Val(c)
	return exact && n >= 0 && n < int64(bits)
}

// fits reports whether the constant x converts to the type to with no
// Go compile error: an integer whose value to holds, or a bool or string
// converted to its own kind.
func (l *lowerer) fits(x syntax.Expr, to check.Type) bool {
	from, ok := l.info.Types[x].(*check.Basic)
	dst, ok2 := to.(*check.Basic)
	if !ok || !ok2 {
		return false
	}
	switch {
	case from.Kind == check.Bool || from.Kind == check.String:
		return dst.Kind == from.Kind
	case intBits(from) == 0 || intBits(dst) == 0:
		return false
	}
	v := l.constValue(x)
	if v == nil || v.Kind() != constant.Int {
		return false
	}
	bits := intBits(dst)
	lo, hi := constant.MakeInt64(0), constant.Shift(constant.MakeUint64(1), token.SHL, uint(bits))
	if dst.Kind >= check.Int && dst.Kind <= check.Int64 {
		half := constant.Shift(constant.MakeUint64(1), token.SHL, uint(bits-1))
		lo, hi = constant.UnaryOp(token.SUB, half, 0), half
	}
	return constant.Compare(v, token.GEQ, lo) && constant.Compare(v, token.LSS, hi)
}

// intBits is the bit width of the integer type t (int and uint are 64
// bits on every Go target Ergo builds for), or 0.
func intBits(t check.Type) int {
	b, ok := t.(*check.Basic)
	if !ok {
		return 0
	}
	switch b.Kind {
	case check.Int8, check.Uint8:
		return 8
	case check.Int16, check.Uint16:
		return 16
	case check.Int32, check.Uint32:
		return 32
	case check.Int, check.Int64, check.Uint, check.Uint64, check.Uintptr:
		return 64
	}
	return 0
}

// constValue is the exact value of the constant expression x when the
// lowering knows it: a checked constant, a let lowered as a Go constant,
// or a foldSafe operator over such; else nil.
func (l *lowerer) constValue(x syntax.Expr) constant.Value {
	x = unparen(x)
	if v, ok := l.info.Values[x]; ok {
		return v
	}
	switch x := x.(type) {
	case *syntax.Ident:
		if v, ok := l.info.Uses[x].(*check.Var); ok && l.consts[v] {
			return l.constVals[v]
		} else if ok {
			return l.localLets()[v] // nil for any other name
		}
	case *syntax.BinaryExpr:
		a, b := l.constValue(x.X), l.constValue(x.Y)
		if a == nil || b == nil || a.Kind() != constant.Int || b.Kind() != constant.Int {
			return nil
		}
		switch x.Op {
		case syntax.AND:
			return constant.BinaryOp(a, token.AND, b)
		case syntax.OR:
			return constant.BinaryOp(a, token.OR, b)
		case syntax.XOR:
			return constant.BinaryOp(a, token.XOR, b)
		case syntax.AND_NOT:
			return constant.BinaryOp(a, token.AND_NOT, b)
		case syntax.SHR:
			if l.countInRange(x) {
				n, _ := constant.Uint64Val(b)
				return constant.Shift(a, token.SHR, uint(n))
			}
		}
	case *syntax.CallExpr:
		if c := l.info.Calls[x]; c != nil && c.Kind == check.CallConversion && len(x.Args) == 1 && l.fits(x.Args[0], l.info.Types[x]) {
			return l.constValue(x.Args[0])
		}
	}
	return nil
}
