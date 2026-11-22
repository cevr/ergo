package check

import (
	"fmt"
	"path"
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
//     Seed uint64 and Site string, F a record of D's package with exactly
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
//     test T { try H(R{Name: "T", Seed: ⟨seed⟩, Site: "file:line"}, func() fails F needs D { BODY' }) }
//
//     BODY' is the body with each assert x turned into
//     if !(x) { return F{Message: "assert failed at file:line:col: x"} }.
//     ⟨seed⟩ is the run's --seed, a built-in only the rewrite names
//     (CallTestSeed). Both tiers then run the same Ergo; a rewrite bug is
//     a diagnostic of the second check, never wrong Go.
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

// testHarnessMarks is the test-only marking of harnesses until the member
// `test H` lands in the syntax (q4: the syntax member goes last): the
// qualified name path.D of an operation type to its harness's name.
var testHarnessMarks map[string]string

// SetTestHarnesses marks, for a test, each operation type path.D of the map
// as declaring the harness the map names; restore undoes it. Test-only:
// the syntax member `test H` replaces it.
func SetTestHarnesses(m map[string]string) (restore func()) {
	saved := testHarnessMarks
	testHarnessMarks = m
	return func() { testHarnessMarks = saved }
}

const harnessShapeText = "func %[1]s(run R, body func() fails F needs %[2]s) fails F, with R a record {Name string, Seed uint64, Site string} and F a record {Message string}, both of this package"

// planHarnesses plans the harness each operation type of the package
// declares (rule 1). It runs after the bodies converge, so H's inferred
// rows are final.
func (c *checker) planHarnesses() {
	for _, tn := range c.pkg.Types {
		name, ok := testHarnessMarks[c.pkg.Path+"."+tn.Name]
		if !ok || tn.Decl == nil {
			continue
		}
		c.cur = tn.file
		c.planHarness(tn, tn.Decl, name)
	}
}

func (c *checker) planHarness(tn *TypeName, at syntax.Node, name string) {
	bad := func(why string) {
		c.errorf(at, CodeType, "test harness %s of %s must have the shape "+harnessShapeText+"; %s", name, tn.Name, why)
	}
	d, _ := tn.Type.(*Named)
	if d == nil || len(d.TParams) != 0 {
		bad(tn.Name + " is generic")
		return
	}
	if _, ok := under(d).(*Interface); !ok {
		bad(tn.Name + " is not an operation type")
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
		why = "it has type parameters"
	case len(s.Params) != 2 || len(s.Results) != 0:
		why = "it takes " + strconv.Itoa(len(s.Params)) + " parameters and gives " + strconv.Itoa(len(s.Results)) + " results"
	case !s.Uses.Empty():
		why = "its needs row is " + s.Uses.String()
	}
	if why == "" {
		run = c.harnessRecord(s.Params[0].Type, "Name", Typ[String], "Seed", Typ[Uint64], "Site", Typ[String])
		if run == nil {
			why = "its first parameter is " + s.Params[0].Type.String()
		}
	}
	if why == "" {
		if es := s.Errors.Elems(); len(es) == 1 {
			fail = c.harnessRecord(es[0], "Message", Typ[String])
		}
		if fail == nil {
			why = "its failure row is " + rowText(s.Errors)
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
			why = "its second parameter is " + s.Params[1].Type.String()
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
// made, or "" for any other body.
func rewrittenHarness(body *syntax.BlockStmt) string {
	if body == nil || len(body.List) != 1 {
		return ""
	}
	es, _ := body.List[0].(*syntax.ExprStmt)
	if es == nil {
		return ""
	}
	try, _ := es.X.(*syntax.TryExpr)
	if try == nil {
		return ""
	}
	call, _ := try.X.(*syntax.CallExpr)
	if call == nil {
		return ""
	}
	sel, _ := call.Fun.(*syntax.SelectorExpr)
	if sel == nil {
		return ""
	}
	id, _ := sel.X.(*syntax.Ident)
	if id == nil || !strings.HasPrefix(id.Name, hiddenPrefix) {
		return ""
	}
	return strings.TrimPrefix(id.Name, hiddenPrefix)
}

// bindHarnessNames binds, in the frame of a rewritten test, the hidden
// names its body uses: the harness's package path and the seed built-in.
func (c *checker) bindHarnessNames(frame *Scope, pkgPath string) {
	frame.insert(TestSeedName, &Builtin{Name: TestSeedName})
	var pkg *Package
	if pkgPath == c.pkg.Path {
		pkg = c.pkg
	} else if p, err := c.imp.Import(pkgPath); err == nil {
		pkg = p
	}
	if pkg != nil {
		frame.insert(hiddenPrefix+pkgPath, &PkgName{Name: hiddenPrefix + pkgPath, Pkg: pkg, used: true})
	}
}

// rewriteHarnessed rewrites the body of test, admitted under h (rule 3).
func (c *checker) rewriteHarnessed(test *Test, h *Harness) {
	d := test.Decl
	f := test.file.syntax
	at := d.Test
	pkgPath := h.Func.Pkg.Path
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
	line, _ := f.Position(at)
	site := path.Base(strings.ReplaceAll(f.Name, "\\", "/")) + ":" + strconv.Itoa(line)
	body := d.Body
	rewriteAsserts(body.List, func(s *syntax.AssertStmt) syntax.Stmt {
		l, col := f.Position(s.Assert)
		text := f.Src[int(s.X.Pos()):min(int(s.X.End()), len(f.Src))]
		msg := fmt.Sprintf("assert failed at %s:%d:%d: %s", path.Base(strings.ReplaceAll(f.Name, "\\", "/")), l, col, text)
		fail := &syntax.CompositeLit{Type: qual(s.Assert, h.Fail.Obj.Name), Lbrace: s.Assert, DotDot: syntax.NoPos, Elts: []syntax.Expr{field(s.Assert, "Message", str(s.Assert, msg))}, Rbrace: s.X.End()}
		return &syntax.IfStmt{
			If:      s.Assert,
			Cond:    &syntax.UnaryExpr{OpPos: s.Assert, Op: syntax.NOT, X: &syntax.ParenExpr{Lparen: s.Assert, X: s.X, Rparen: s.X.End()}},
			Body:    &syntax.BlockStmt{Lbrace: s.Assert, List: []syntax.Stmt{&syntax.ReturnStmt{Return: s.Assert, Results: []syntax.Expr{fail}}}, Rbrace: s.X.End()},
			ElsePos: syntax.NoPos,
		}
	})
	seed := &syntax.CallExpr{Fun: &syntax.Ident{NamePos: at, Name: TestSeedName}, Lparen: at, Rparen: at}
	run := &syntax.CompositeLit{Type: qual(at, h.Run.Obj.Name), Lbrace: at, DotDot: syntax.NoPos, Elts: []syntax.Expr{
		field(at, "Name", str(at, test.Name)),
		field(at, "Seed", seed),
		field(at, "Site", str(at, site)),
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
	d.Body = &syntax.BlockStmt{Lbrace: body.Lbrace, List: []syntax.Stmt{&syntax.ExprStmt{X: &syntax.TryExpr{Try: at, X: call}}}, Rbrace: body.Rbrace}
}

// rewriteAsserts replaces, in list and the statement lists inside it, each
// assert by to's statement. A function literal keeps its own: an assert
// stands in none (S108).
func rewriteAsserts(list []syntax.Stmt, to func(*syntax.AssertStmt) syntax.Stmt) {
	for i, s := range list {
		if a, ok := s.(*syntax.AssertStmt); ok {
			list[i] = to(a)
			continue
		}
		syntax.Inspect(s, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.BlockStmt:
				rewriteAsserts(n.List, to)
				return false
			case *syntax.CaseClause:
				rewriteAsserts(n.Body, to)
				return false
			case *syntax.CatchArm:
				rewriteAsserts(n.Body, to)
				return false
			}
			return true
		})
	}
}
