package check

import (
	"fmt"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Test harnesses (runtime.md, Tests: harnesses; PROPERTY-TESTING P1b). An
// operation type D may declare a harness H, the function of its package a
// test whose body needs D runs under. The rule names no package: any
// library declares one, std's gen among them (std has no privileges).
//
//  1. H has the shape func H(run R, body func() fails F needs D) fails F:
//     R a record of D's package whose fields are exactly Name string,
//     Seed uint64, Site string and Source string, F a record of D's package with exactly
//     the field Message string, no type parameters and an empty needs
//     row. Any other shape is one S108 at the declaration (planHarness).
//  2. In a test with no parameters, a charge of a capability with a
//     harness is admitted, as a with would discharge it (charge); every
//     other capability is still S102. Two harnessed capabilities in one
//     body are S102: one harness runs a body. A law body or a test with
//     parameters reaching one is S102 as before (P1c).
//  3. A test whose body was admitted so, and checked clean, is rewritten
//     in place and checked again (rewriteHarnessed):
//
//     test T {
//     	H(R{Name: "T", Seed: ⟨seed⟩, Site: "file:line", Source: ⟨source⟩}, func() fails F needs D { BODY' }) catch {
//     	case F as failed: ⟨report⟩(failed.Message)
//     	}
//     }
//
//     BODY' is the body with each assert x turned into
//     if !(x) { return F{Message: "assert failed at file:line:col: x"} }.
//     file is ergo·file(), the test's file as the runner names it
//     (CallTestFile): a source of one file is checked with no name.
//     ⟨seed⟩ is the run's --seed, a built-in only the rewrite names
//     (CallTestSeed); ⟨source⟩ is the path ergo test was given (its
//     --source to the test binary, the file or package folder a replay
//     command names), or the test's file when the binary runs alone
//     (CallTestSource). ⟨report⟩ fails the test with the harness's
//     message as its whole report, the text after "FAIL T: " (CallTestReport),
//     so the replay command ends the report's last line. Both tiers then run the same Ergo; a rewrite bug is
//     a diagnostic of the second check, never wrong Go.
//
// The member is `test H` among the type's operations and laws (grammar.md,
// Declarations; syntax.InterfaceType.Harnesses).
//
// The rewrite's names are bound in the test's frame under names no source
// can spell (hiddenPrefix): the harness's package, so its H, R, F and D
// resolve whether or not the test's file imports it, and the seed
// built-in. A checker that meets an already rewritten body (a second
// runOnce, a cached tree) binds them again from the body itself.

// Harness is the harness an operation type declares (rule 1).
type Harness struct {
	Cap  *TypeName // D, the operation type that declares it
	Func *Func     // H
	Run  *Named    // R
	Fail *Named    // F
}

// hiddenPrefix starts the rewrite's names: `·` is no letter of an Ergo
// name, so no source binds or shadows them.
const hiddenPrefix = "ergo·"

// TestSeedName is the built-in the rewrite calls for the run's --seed.
const TestSeedName = hiddenPrefix + "seed"

// TestSourceName is the built-in the rewrite calls for the source path a
// replay command names: ergo·source(file), the run's --source or file.
const TestSourceName = hiddenPrefix + "source"

// TestFileName is the built-in the rewrite calls for the test's file as
// the runner names it in a report: its base name, the evaluator's and the
// test binary's alike (CallTestFile).
const TestFileName = hiddenPrefix + "file"

// TestReportName is the built-in a rewritten test calls with its
// harness's failure message: the test fails with that report (rule 3).
const TestReportName = hiddenPrefix + "report"

// TestShownName is the built-in the rewrite calls for a failed
// comparison's operands, as the runner shows them (CallTestShown).
const TestShownName = hiddenPrefix + "shown"

const harnessShapeText = "func %[1]s(run R, body func() fails F needs %[2]s) fails F, with R a record {Name string, Seed uint64, Site string, Source string} and F a record {Message string}, both of this package"

// planHarnesses plans the harness each operation type of the package
// declares (rule 1). It runs after the bodies converge, so H's inferred
// rows are final.
func (c *checker) planHarnesses() {
	for _, tn := range c.pkg.Types {
		if tn.Decl == nil {
			continue
		}
		it, _ := tn.Decl.Spec.(*syntax.InterfaceType)
		if it == nil || len(it.Harnesses) == 0 {
			continue
		}
		c.cur = tn.file
		for _, extra := range it.Harnesses[1:] {
			c.errorf(extra, CodeType, "%s declares a second test harness %s; an operation type has one", tn.Name, extra.Name.Name)
		}
		h := it.Harnesses[0]
		c.planHarness(tn, h, h.Name.Name)
		if tn.Harness != nil {
			c.recordUse(h.Name, tn.Harness.Func)
		}
	}
}

func (c *checker) planHarness(tn *TypeName, at syntax.Node, name string) {
	bad := func(why string) {
		c.errorf(at, CodeType, "test harness %s of %s must have the shape "+harnessShapeText+"; %s", name, tn.Name, why)
	}
	// The reasons name the part of the shape that differs, in words
	// compiler/check (diag/harness.ergo) writes from the declarations.
	d, _ := tn.Type.(*Named)
	if d == nil || len(d.TParams) != 0 {
		bad(tn.Name + " is generic")
		return
	}
	f, _ := c.pkg.Scope.names[name].(*Func)
	if f == nil || f.Sig == nil {
		bad("no function " + name + " is declared in this package")
		return
	}
	s := f.Sig
	why := ""
	var run, fail *Named
	switch {
	case len(s.TParams) != 0:
		why = name + " has type parameters"
	case len(s.Params) != 2 || len(s.Results) != 0:
		why = name + " does not take two parameters and give no result"
	case !s.Uses.Empty():
		why = name + "'s needs row is not empty"
	}
	if why == "" {
		run = c.harnessRecord(s.Params[0].Type, "Name", Typ[String], "Seed", Typ[Uint64], "Site", Typ[String], "Source", Typ[String])
		if run == nil {
			why = "its first parameter is not a record {Name string, Seed uint64, Site string, Source string} of this package"
		}
	}
	if why == "" {
		if es := s.Errors.Elems(); len(es) == 1 {
			fail = c.harnessRecord(es[0], "Message", Typ[String])
		}
		if fail == nil {
			why = "its failure row is not one record {Message string} of this package"
		}
	}
	if why == "" {
		b, _ := s.Params[1].Type.(*Signature)
		ok := b != nil && len(b.TParams) == 0 && len(b.Params) == 0 && len(b.Results) == 0
		if ok {
			es, us := b.Errors.Elems(), b.Uses.Elems()
			ok = len(es) == 1 && c.identical(es[0], fail) && len(us) == 1 && c.identical(us[0], d)
		}
		if !ok {
			why = "its second parameter is not func() fails " + fail.Obj.Name + " needs " + tn.Name
		}
	}
	if why != "" {
		bad(why)
		return
	}
	tn.Harness = &Harness{Cap: tn, Func: f, Run: run, Fail: fail}
}

// harnessRecord is t when it is a record of this package with exactly the
// fields of pairs (name, type, …), in any order; nil otherwise.
func (c *checker) harnessRecord(t Type, pairs ...any) *Named {
	n, _ := t.(*Named)
	if n == nil || n.Obj == nil || n.Obj.Pkg != c.pkg || len(n.TArgs) != 0 {
		return nil
	}
	st := asStruct(n)
	if st == nil || len(st.Fields) != len(pairs)/2 {
		return nil
	}
	for i := 0; i < len(pairs); i += 2 {
		name, typ := pairs[i].(string), pairs[i+1].(Type)
		found := false
		for _, f := range st.Fields {
			if f.Name == name && c.identical(f.Type, typ) {
				found = true
			}
		}
		if !found {
			return nil
		}
	}
	return n
}

// harnessOf is the harness of the capability cap, or nil.
func harnessOf(cap Type) *Harness {
	n, _ := cap.(*Named)
	if n == nil || n.Obj == nil || len(n.TArgs) != 0 {
		return nil
	}
	return n.Obj.Harness
}

// harnessUse is what a harnessable test body charged (rule 2): the one
// harness, its first site.
type harnessUse struct {
	h  *Harness
	at syntax.Node
}

// chargeHarness admits the charge of cap at n in a harnessable test body
// (rule 2), reporting a second harness; false when cap has none.
func (c *checker) chargeHarness(t *target, n syntax.Node, cap Type) bool {
	h := harnessOf(cap)
	if h == nil {
		return false
	}
	u := t.harness
	switch {
	case u.h == nil:
		u.h, u.at = h, n
	case u.h != h:
		c.errorf(n, CodeMissingCap, "a test body runs under one harness; %s needs %s (harness %s) and %s (harness %s)", t.name, u.h.Cap.Name, u.h.Func.Name, h.Cap.Name, h.Func.Name)
	}
	return true
}

// rewrittenHarness is the harness's package of a test body the rewrite
// made, its import path or "" for the test's own package, and whether the
// rewrite made body.
func rewrittenHarness(body *syntax.BlockStmt) (string, bool) {
	if body == nil || len(body.List) != 1 {
		return "", false
	}
	es, _ := body.List[0].(*syntax.ExprStmt)
	if es == nil {
		return "", false
	}
	caught, _ := es.X.(*syntax.CatchExpr)
	if caught == nil {
		return "", false
	}
	call, _ := caught.X.(*syntax.CallExpr)
	if call == nil {
		return "", false
	}
	sel, _ := call.Fun.(*syntax.SelectorExpr)
	if sel == nil {
		return "", false
	}
	id, _ := sel.X.(*syntax.Ident)
	if id == nil || !strings.HasPrefix(id.Name, hiddenPrefix) {
		return "", false
	}
	return strings.TrimPrefix(id.Name, hiddenPrefix), true
}

// bindHarnessNames binds, in the frame of a rewritten test, the hidden
// names its body uses: the harness's package, by its import path or ""
// for the package under check (no path names it, STD HAS NO PRIVILEGES),
// and the built-ins.
func (c *checker) bindHarnessNames(frame *Scope, pkgPath string) {
	frame.insert(TestSeedName, &Builtin{Name: TestSeedName})
	frame.insert(TestShownName, &Builtin{Name: TestShownName})
	frame.insert(TestSourceName, &Builtin{Name: TestSourceName})
	frame.insert(TestReportName, &Builtin{Name: TestReportName})
	frame.insert(TestFileName, &Builtin{Name: TestFileName})
	pkg := c.pkg
	if pkgPath != "" {
		pkg = nil
		if p, err := c.imp.Import(pkgPath); err == nil {
			pkg = p
		}
	}
	// The package binds in the file's scope, where a qualified type looks
	// (lookupType); no source spells the name, so it hides nothing.
	if _, bound := c.cur.scope.names[hiddenPrefix+pkgPath]; pkg != nil && !bound {
		c.cur.scope.insert(hiddenPrefix+pkgPath, &PkgName{Name: hiddenPrefix + pkgPath, Pkg: pkg, used: true})
	}
}

// rewriteHarnessed rewrites the body of test, admitted under h (rule 3).
func (c *checker) rewriteHarnessed(test *Test, h *Harness) {
	d := test.Decl
	f := test.file.syntax
	at := d.Test
	// The harness's package binds under a hidden name: its import path,
	// or none when the harness is the test's own package's.
	pkgPath := ""
	if h.Func.Pkg != c.pkg {
		pkgPath = h.Func.Pkg.Path
	}
	pkgID := func() *syntax.Ident { return &syntax.Ident{NamePos: at, Name: hiddenPrefix + pkgPath} }
	qual := func(pos syntax.Pos, name string) *syntax.SelectorExpr {
		return &syntax.SelectorExpr{X: &syntax.Ident{NamePos: pos, Name: hiddenPrefix + pkgPath}, Sel: &syntax.Ident{NamePos: pos, Name: name}}
	}
	str := func(pos syntax.Pos, s string) *syntax.BasicLit {
		return &syntax.BasicLit{ValuePos: pos, Kind: syntax.STRING, Value: strconv.Quote(s), ValueEnd: pos}
	}
	field := func(pos syntax.Pos, name string, v syntax.Expr) syntax.Expr {
		return &syntax.KeyValueExpr{Key: &syntax.Ident{NamePos: pos, Name: name}, Colon: pos, Value: v}
	}
	// The file's name is the runner's (ergo·file): a source of one file
	// is checked with no name, and each tier names it as its reports do.
	fileCall := func(pos syntax.Pos) syntax.Expr {
		return &syntax.CallExpr{Fun: &syntax.Ident{NamePos: pos, Name: TestFileName}, Lparen: pos, Rparen: pos}
	}
	cat := func(pos syntax.Pos, xs ...syntax.Expr) syntax.Expr {
		x := xs[0]
		for _, y := range xs[1:] {
			x = &syntax.BinaryExpr{X: x, OpPos: pos, Op: syntax.ADD, Y: y}
		}
		return x
	}
	line, _ := f.Position(at)
	body := d.Body
	spills := 0
	body.List = rewriteAsserts(body.List, func(s *syntax.AssertStmt) []syntax.Stmt {
		l, col := f.Position(s.Assert)
		text := f.Src[int(s.X.Pos()):min(int(s.X.End()), len(f.Src))]
		var message syntax.Expr = cat(s.Assert, str(s.Assert, "assert failed at "), fileCall(s.Assert), str(s.Assert, fmt.Sprintf(":%d:%d: %s", l, col, text)))
		var spilled []syntax.Stmt
		if b, _, _, shown := ShownCompare(c.pkg, c.pkg.Info, s.X); shown {
			// The runner's shown assert (P1c part 1): each operand once,
			// left to right, into a hidden local (a constant stays
			// inline), and the failure's message shows them.
			operands := [...]*syntax.Expr{&b.X, &b.Y}
			ops := []syntax.Expr{b.X, b.Y}
			spills++
			n := strconv.Itoa(spills)
			for i, name := range []string{hiddenPrefix + "left" + n, hiddenPrefix + "right" + n} {
				if _, isConst := c.pkg.Info.Values[ops[i]]; isConst {
					continue
				}
				pos := ops[i].Pos()
				spilled = append(spilled, &syntax.DeclStmt{Decl: &syntax.VarDecl{Var: pos, Mut: syntax.NoPos, Specs: []*syntax.VarSpec{{Names: []*syntax.Ident{{NamePos: pos, Name: name}}, Assign: pos, Values: []syntax.Expr{ops[i]}}}}})
				read := &syntax.Ident{NamePos: pos, Name: name}
				// The comparison remains the source expression in Info.
				// Anchor the read to the original operand, not to the
				// length of the generated name. Parentheses are transparent
				// to checking and lowering.
				*operands[i] = &syntax.ParenExpr{Lparen: pos, X: read, Rparen: ops[i].End() - 1}
				ops[i] = read
			}
			shown := &syntax.CallExpr{Fun: &syntax.Ident{NamePos: s.Assert, Name: TestShownName}, Lparen: s.Assert, Args: ops, Rparen: s.X.End()}
			message = &syntax.BinaryExpr{X: message, OpPos: s.Assert, Op: syntax.ADD, Y: shown}
		}
		fail := &syntax.CompositeLit{Type: qual(s.Assert, h.Fail.Obj.Name), Lbrace: s.Assert, DotDot: syntax.NoPos, Elts: []syntax.Expr{field(s.Assert, "Message", message)}, Rbrace: s.X.End()}
		check := &syntax.IfStmt{
			If:      s.Assert,
			Cond:    &syntax.UnaryExpr{OpPos: s.Assert, Op: syntax.NOT, X: &syntax.ParenExpr{Lparen: s.Assert, X: s.X, Rparen: s.X.End()}},
			Body:    &syntax.BlockStmt{Lbrace: s.Assert, List: []syntax.Stmt{&syntax.ReturnStmt{Return: s.Assert, Results: []syntax.Expr{fail}}}, Rbrace: s.X.End()},
			ElsePos: syntax.NoPos,
		}
		// The hidden locals bind in the assert's own list, before it: a
		// bare block is no statement of the decided syntax (S109).
		return append(spilled, check)
	})
	seed := &syntax.CallExpr{Fun: &syntax.Ident{NamePos: at, Name: TestSeedName}, Lparen: at, Rparen: at}
	run := &syntax.CompositeLit{Type: qual(at, h.Run.Obj.Name), Lbrace: at, DotDot: syntax.NoPos, Elts: []syntax.Expr{
		field(at, "Name", str(at, test.Name)),
		field(at, "Seed", seed),
		field(at, "Site", cat(at, fileCall(at), str(at, ":"+strconv.Itoa(line)))),
		field(at, "Source", &syntax.CallExpr{Fun: &syntax.Ident{NamePos: at, Name: TestSourceName}, Lparen: at, Args: []syntax.Expr{fileCall(at)}, Rparen: at}),
	}, Rbrace: at}
	lit := &syntax.FuncLit{
		Type: &syntax.FuncType{Escaping: syntax.NoPos, Func: at, Params: &syntax.FieldList{Opening: at, Closing: at},
			Results: &syntax.FieldList{Opening: syntax.NoPos, List: []*syntax.Field{
				{Type: &syntax.ErrorRow{Error: at, Types: []syntax.Expr{qual(at, h.Fail.Obj.Name)}}, WherePos: syntax.NoPos},
			}, Closing: syntax.NoPos},
			Needs: &syntax.NeedsClause{Needs: at, Names: []syntax.Expr{qual(at, h.Cap.Name)}}},
		Body: body,
	}
	call := &syntax.CallExpr{Fun: &syntax.SelectorExpr{X: pkgID(), Sel: &syntax.Ident{NamePos: at, Name: h.Func.Name}}, Lparen: at, Args: []syntax.Expr{run, lit}, Rparen: body.Rbrace}
	failed := func() *syntax.Ident { return &syntax.Ident{NamePos: at, Name: hiddenPrefix + "failed"} }
	report := &syntax.CallExpr{Fun: &syntax.Ident{NamePos: at, Name: TestReportName}, Lparen: at, Args: []syntax.Expr{
		&syntax.SelectorExpr{X: failed(), Sel: &syntax.Ident{NamePos: at, Name: "Message"}},
	}, Rparen: at}
	arm := &syntax.CatchArm{Case: at, Types: []syntax.Expr{&syntax.AsPattern{X: qual(at, h.Fail.Obj.Name), As: at, Name: failed()}}, Colon: at, Body: []syntax.Stmt{&syntax.ExprStmt{X: report}}}
	caught := &syntax.CatchExpr{X: call, Catch: body.Rbrace, Lbrace: body.Rbrace, Arms: []*syntax.CatchArm{arm}, Rbrace: body.Rbrace}
	d.Body = &syntax.BlockStmt{Lbrace: body.Lbrace, List: []syntax.Stmt{&syntax.ExprStmt{X: caught}}, Rbrace: body.Rbrace}
}

// rewriteAsserts is list with each assert, in it and in the statement
// lists inside it, replaced by to's statements. A function literal keeps
// its own: an assert stands in none (S108).
func rewriteAsserts(list []syntax.Stmt, to func(*syntax.AssertStmt) []syntax.Stmt) []syntax.Stmt {
	out := make([]syntax.Stmt, 0, len(list))
	for _, s := range list {
		if a, ok := s.(*syntax.AssertStmt); ok {
			out = append(out, to(a)...)
			continue
		}
		out = append(out, s)
		syntax.Inspect(s, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.BlockStmt:
				n.List = rewriteAsserts(n.List, to)
				return false
			case *syntax.CaseClause:
				n.Body = rewriteAsserts(n.Body, to)
				return false
			case *syntax.CatchArm:
				n.Body = rewriteAsserts(n.Body, to)
				return false
			}
			return true
		})
	}
	return out
}

// ShownCompare is the one rule (checker, evaluator and lowering) for a
// failed assert of x that shows its operands: x, through parentheses, is a
// comparison (==, !=, <, <=, >, >=), not of two constants, whose operand
// types, by info, are both Printable in pkg, the package under test. It
// returns the comparison and the operand types. A try that starts x is
// the caller's to look through.
func ShownCompare(pkg *Package, info *Info, x syntax.Expr) (*syntax.BinaryExpr, Type, Type, bool) {
	b, ok := unparen(x).(*syntax.BinaryExpr)
	if !ok {
		return nil, nil, nil, false
	}
	switch b.Op.String() {
	case "==", "!=", "<", "<=", ">", ">=":
	default:
		return nil, nil, nil, false
	}
	_, lc := info.Values[b.X]
	_, rc := info.Values[b.Y]
	lt, lok := TypedExpr(info, b.X)
	rt, rok := TypedExpr(info, b.Y)
	if (lc && rc) || !lok || !rok || !Printable(pkg, lt) || !Printable(pkg, rt) {
		return nil, nil, nil, false
	}
	return b, lt, rt, true
}

// TypedExpr is x's checked type in info, through parentheses.
func TypedExpr(info *Info, x syntax.Expr) (Type, bool) {
	for {
		if t, ok := info.Types[x]; ok {
			return t, true
		}
		p, isParen := x.(*syntax.ParenExpr)
		if !isParen {
			return nil, false
		}
		x = p.X
	}
}

// Printable is the one rule for a type a failed assert's report shows,
// which an argument literal writes (lower's litExpr, eval's argument): a
// basic type; a slice, Map or
// tuple of printable types; a union of printable members; or a record,
// union or defined type of pkg, the package under test, over printable
// types.
func Printable(pkg *Package, t Type) bool { return printable(pkg, t, map[*Named]bool{}) }

func printable(pkg *Package, t Type, seen map[*Named]bool) bool {
	switch u := t.(type) {
	case *Basic:
		return true
	case *Slice:
		return printable(pkg, u.Elem, seen)
	case *Map:
		return printable(pkg, u.Key, seen) && printable(pkg, u.Elem, seen)
	case *Tuple:
		for _, e := range u.Types {
			if !printable(pkg, e, seen) {
				return false
			}
		}
		return true
	case *Union:
		for _, v := range u.Variants {
			if !printable(pkg, v, seen) {
				return false
			}
		}
		return len(u.Variants) > 0
	case *Named:
		if seen[u] {
			return true
		}
		seen[u] = true
		if u.Obj.Pkg != pkg {
			return false
		}
		switch under := u.Underlying().(type) {
		case *Struct:
			for _, f := range under.Fields {
				if !printable(pkg, f.Type, seen) {
					return false
				}
			}
			return true
		case *Union:
			for _, v := range under.Variants {
				if !printable(pkg, v, seen) {
					return false
				}
			}
			return true
		case *Basic, *Slice, *Map, *Tuple:
			return printable(pkg, under, seen)
		}
	}
	return false
}

// shownCall checks ergo·shown(left, right), which only the rewrite of a
// shown assert writes: a let-bound operand first, so a constant operand
// takes the other's type as the comparison gave it.
func (c *checker) shownCall(x *syntax.CallExpr) operand {
	if len(x.Args) != 2 {
		c.silentArgs(x)
		return bad(x)
	}
	first := 0
	if id, ok := x.Args[0].(*syntax.Ident); !ok || !strings.HasPrefix(id.Name, hiddenPrefix) {
		first = 1
	}
	a := c.valueExpr(x.Args[first], nil)
	b := c.valueExpr(x.Args[1-first], a.typ)
	if a.invalid() || b.invalid() {
		return bad(x)
	}
	return operand{mode: modeValue, typ: Typ[String]}
}
