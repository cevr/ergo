package lower

import (
	"fmt"
	"math/big"
	"path"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
)

// Test mode (196l; check-v1 spec D-E4, ledger row C20). A test is lowered
// only by LowerTests, a separate build of the same checked unit:
//
//  1. Lower, the run build, emits no Go for a test: decl skips every test
//     declaration and nameTests reserves nothing, so no Go line of the run
//     build comes from a test body (TestRunBuildOmitsTests proves it on the
//     emitted Go, by its //line directives). An assert outside a test body
//     is refused by the checker (S108), so the run build never meets one.
//  2. LowerTests lowers the whole package as the run build does, plus one Go
//     function per test, `func ergoTestName(env *Env, params...) error`,
//     and replaces the entry: Go's main reads --seed N (1 by default) and
//     runs every test in source order inside the program's root task
//     (v1.Main), after the package initializer.
//  3. An example test runs once. A property whose parameters can all be
//     listed runs once for each combination of values and is reported
//     proved, by the rule of laws (listedTest, lawcases.go); any other
//     property runs 100 cases, each with arguments a generator derived
//     from its parameter types draws (testGen). A case fails when an assert in it fails, wherever the
//     assert is (a with body, a function literal, a task it starts), or
//     when a typed failure escapes the body. A failure prints
//     `FAIL Name (case K of 100, seed S: x = …): …` and the run goes on; a
//     defect prints its FAIL line and stops the run with the runtime's own
//     report (exit 2). The run ends with `PASS (N tests)`, or with
//     `FAIL (K of N tests)` and exit status 1.
//
// An assert lowers to `if !(cond) { ergoAssertFail(pos, text) }`, which
// panics with the failed assert; a comparison whose operands have literals
// binds each operand once and adds them to the text (assertCompare). The frames the panic crosses (a with
// scope, an exit action, a task boundary) record it as a defect of the
// exit they carry on, beside whatever else ended them. The case reads that
// exit through the runtime (v1.Capture), never a record of what happened
// on the way: it fails when every reason is a failed assert or a typed
// failure, and otherwise raises the exit again whole (v1.Raise), so a
// defect, one an exit action raised after an assert included, keeps its
// stack for the runtime's report and stops the run (E3a M3).
//
// Prior art: Go's test binary is a separate build with a generated main
// that calls each TestX and stops at a panic; Elixir's ExUnit and Gleam's
// gleeunit run test functions the build of the program never contains.
// QuickCheck and Hypothesis derive a sized generator from the type and
// print the seed and the failing arguments; Elixir's StreamData and Gleam's
// qcheck shrink as well, which this first runner does not.

// testCases is the number of cases a property runs.
const testCases = testplan.Cases

// testDepth is the size a property's arguments start with: each record,
// tuple, union, slice or Map level lowers it by one, and at zero a slice
// and a Map are empty and a union takes its smallest variant.
const testDepth = testplan.Depth

// testLowering is the test build's state; the run build has none.
type testLowering struct {
	gens map[pkgKey]bool // generator functions emitted
	lits map[pkgKey]bool // argument literal functions emitted (literal.go)
	keys map[pkgKey]bool // state key functions emitted (machinelaws.go)
	// law is the law whose body is being lowered, nil outside one: a bare
	// operation call in it (CallLawOp) calls its operation type's
	// capability, which the run binds to the provider under test.
	law *check.Law
	// lawOwn is the Go parameter that holds the provider under test in a
	// law of a generic operation type, whose bare operation calls call it;
	// "" in any other law.
	lawOwn string
	// lawPkgs are the imported packages whose laws the build lowers
	// (owners.go, viewOf), set on first use.
	lawPkgs map[*check.Package]bool
}

// LowerTests lowers the checked unit to the Go module of its test binary:
// the package as Lower lowers it, each test as a Go function, and a main
// that runs the tests (see Test mode). It takes the unit Lower takes.
func LowerTests(u Unit) ([]File, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	l := newBuildLowerer(u, &testLowering{gens: map[pkgKey]bool{}, lits: map[pkgKey]bool{}})
	code, err := l.program()
	if err != nil {
		return nil, err
	}
	return moduleFiles(u.Go, code), nil
}

// testHelpers are the harness's package-scope entities: key and preferred
// name, in the order the naming pass reserves them.
var testHelpers = []struct{ key, base string }{
	{"fmt", "fmt"}, {"os", "os"}, {"sort", "sort"}, {"strconv", "strconv"}, {"strings", "strings"},
	{"Assertion", "ergoAssertion"}, {"AssertFail", "ergoAssertFail"}, {"AssertDiff", "ergoAssertDiff"}, {"Refuted", "ergoRefuted"},
	{"Case", "ergoCase"}, {"Asserted", "ergoAsserted"}, {"Where", "ergoWhere"},
	{"Rand", "ergoRand"}, {"NewRand", "ergoNewRand"},
	{"GenSlice", "ergoGenSlice"}, {"GenMap", "ergoGenMap"},
	{"GenIn", "ergoGenIn"}, {"GenStrLen", "ergoGenStrLen"}, {"GenSliceLen", "ergoGenSliceLen"},
	{"LitFloat", "ergoLitFloat"}, {"LitComplex", "ergoLitComplex"},
	{"LitDuration", "ergoLitDuration"}, {"LitEntries", "ergoLitEntries"},
	{"Seed", "ergoSeed"}, {"Tests", "ergoTests"},
	{"Graph", "ergoGraph"}, {"Node", "ergoNode"}, {"Explore", "ergoExplore"}, {"Signals", "ergoSignals"}, {"Block", "ergoBlock"},
	{"ExploreAll", "ergoExploreAll"}, {"ExploreRuns", "ergoExploreRuns"}, {"Trace", "ergoTrace"}, {"Refute", "ergoRefute"},
	{"States", "ergoStates"}, {"explore", "explore"}, {"testgen", "testgen"},
}

// helper is the Go name of the harness entity key.
func (l *lowerer) helper(key string) string {
	for _, h := range testHelpers {
		if h.key == key {
			return l.pkgName(pkgKey{"test helper", key}, h.base)
		}
	}
	panic("lower: unknown test helper " + key)
}

// nameTests reserves the test build's package names: the harness, each
// test's function, and the generator and the argument literal function of
// each type a property's parameters reach. The run build reserves none
// (rule 1).
func (l *lowerer) nameTests() {
	if l.tests == nil {
		return
	}
	for _, h := range testHelpers {
		l.helper(h.key)
	}
	reserveLit := func(t check.Type) {
		l.litTypes(t, map[string]bool{}, func(t check.Type) {
			key, base, _ := l.litKey(t)
			l.pkgName(key, base)
		})
	}
	reserveGen := func(t check.Type) {
		l.genNamed(t, map[check.Type]bool{}, func(n *check.Named) { l.genName(n) })
		reserveLit(t)
	}
	for _, r := range l.runLaws() {
		l.lawFunc(r.Law)
	}
	// The key functions of the states of the machines whose reachability
	// laws run (machinelaws.go, State keys).
	for _, r := range l.runLaws() {
		if law := r.Law; lawLowered(r) && law.Machine != nil && law.Decl.Reach != nil {
			state := law.Machine.Type.(*check.Named).Underlying().(*check.MachineType).State
			if l.keyWhy(state, map[string]bool{}) == "" {
				l.keyTypes(state, map[string]bool{}, func(t check.Type) {
					key, base, _ := l.keyName(t)
					l.pkgName(key, base)
				})
			}
		}
	}
	// Each base (ergoLaw, ergoGen, ergoLit) is its own, so the order of
	// the law functions and the generators decides no name.
	l.genRoots(reserveGen)
	for _, t := range l.runTests() {
		l.testFunc(t)
		// The literal functions of the operands a failed assert shows.
		syntax.Inspect(t.Decl, func(n syntax.Node) bool {
			if a, ok := n.(*syntax.AssertStmt); ok {
				if _, lt, rt, shown := l.assertShown(a.X); shown {
					reserveLit(lt)
					reserveLit(rt)
				}
			}
			return true
		})
	}
}

// genRoots calls f on each type the test build generates a value of: the
// parameters of each law run that are not slots, each provider (or its
// constructor's parameters) and each property's parameters.
func (l *lowerer) genRoots(f func(check.Type)) {
	for _, r := range l.runLaws() {
		law := r.Law
		if !lawLowered(r) {
			continue
		}
		// A law of a generic operation type draws its parameters at each
		// provider's instance (lawRuns), not at its own type parameters.
		generic := lawGenericOwner(law) != nil
		for i, v := range law.Params {
			if !lawSlotParam(law, i) && !generic {
				f(v.Type)
			}
		}
		if law.Decl.Reach != nil {
			// A machine's reachability law (MU6) generates its signals.
			for _, t := range l.reachTypes(law) {
				f(t)
			}
		}
		for _, b := range r.Bindings {
			if bindingUnrun(b) != "" {
				continue
			}
			if generic {
				for i, t := range b[0].Params {
					if !lawSlotParam(law, i) {
						f(t)
					}
				}
			}
			for _, p := range b {
				if p.Ctor != nil {
					for _, v := range p.Ctor.Sig.Params {
						f(v.Type)
					}
				} else {
					f(p.Type)
				}
			}
		}
	}
	for _, t := range l.runTests() {
		for _, v := range t.Params {
			f(v.Type)
		}
	}
}

// testImports are the import lines the test build adds; the harness uses
// each of them.
func (l *lowerer) testImports() []string {
	if l.tests == nil {
		return nil
	}
	var out []string
	for _, p := range []string{"fmt", "os", "sort", "strconv", "strings"} {
		out = append(out, l.helper(p)+" "+strconv.Quote(p))
	}
	out = append(out, l.helper("explore")+" "+strconv.Quote(runtimeModule+"/runtime/explore"))
	out = append(out, l.helper("testgen")+" "+strconv.Quote(runtimeModule+"/runtime/testgen"))
	return out
}

// runTests are the tests the test build runs: the root package's only. An
// imported package's tests belong to that package's own test build, as Go's
// go test runs the tests of the package it names, not of its imports.
// They are in canonical order (canon.go), so the runner, the names and the
// report do not depend on the order the checker met the files.
func (l *lowerer) runTests() []*check.Test {
	return l.canon.tests(l.pkg)
}

func (l *lowerer) testFunc(t *check.Test) string {
	return l.pkgName(pkgKey{"test", t}, "ergoTest"+t.Name)
}

// runLaws are the laws the test build reports: the root package's law
// runs (check.Package.LawRuns), its own laws and the laws of its imports
// bound to providers it is the least package to see, in canonical order,
// as runTests are its tests (Laws). A law that does not run (Law.Unrun) is
// reported as skipped and has no body in the build.
func (l *lowerer) runLaws() []check.LawRun {
	return l.canon.lawRuns(l.pkg)
}

// lawLowered reports whether the build lowers the run's law: it runs and
// has a binding whose providers all run.
func lawLowered(r check.LawRun) bool {
	if r.Law.Unrun != "" {
		return false
	}
	for _, b := range r.Bindings {
		if bindingUnrun(b) == "" {
			return true
		}
	}
	for _, lc := range r.Contexts {
		if lc.Unrun == "" {
			return true
		}
	}
	return false
}

// lawContexts are the contexts the test build runs laws under
// (LawRun.Contexts), in the runs' order; none in a run build.
func (l *lowerer) lawContexts() []check.LawContext {
	if l.tests == nil {
		return nil
	}
	var out []check.LawContext
	for _, r := range l.runLaws() {
		if r.Law.Unrun != "" {
			continue
		}
		for _, lc := range r.Contexts {
			if lc.Unrun == "" {
				out = append(out, lc)
			}
		}
	}
	return out
}

// bindingUnrun is why a binding does not run: its first provider's, "" when
// every provider runs.
func bindingUnrun(b []check.LawProvider) string {
	for _, p := range b {
		if p.Unrun != "" {
			return p.Unrun
		}
	}
	return ""
}

// lawTitle is the name a law's report uses: Name at top level, Type.Name
// for a member law, each with its package's name before it when the law
// is of another package than the one under test.
func (l *lowerer) lawTitle(law *check.Law) string {
	title := law.Name
	switch {
	case law.Owner != nil:
		title = law.Owner.Name + "." + law.Name
	case law.Machine != nil:
		title = law.Machine.Name + "." + law.Name
	}
	if law.Pkg != nil && law.Pkg != l.pkg {
		title = law.Pkg.Name + "." + title
	}
	return title
}

// providerTitle is the name a report gives a provider: its type's name,
// with its package's name before it when it is of another package than
// the one under test.
func (l *lowerer) providerTitle(p check.LawProvider) string {
	if pkg := p.Type.Obj.Pkg; pkg != nil && pkg != l.pkg {
		return pkg.Name + "." + p.Type.Obj.Name
	}
	return p.Type.Obj.Name
}

// bindingLabel is the report's name of a run of law with the binding b:
// `law T.L for P` for a member law's owner, then `with x = P` for each
// parameter of an operation type.
func (l *lowerer) bindingLabel(law *check.Law, b []check.LawProvider) string {
	label := "law " + l.lawTitle(law)
	var with []string
	for k, s := range law.Slots {
		if s.Param < 0 {
			label += " for " + l.providerTitle(b[k])
			continue
		}
		with = append(with, law.Params[s.Param].Name+" = "+l.providerTitle(b[k]))
	}
	if len(with) > 0 {
		label += " with " + strings.Join(with, ", ")
	}
	return label
}

func (l *lowerer) lawFunc(law *check.Law) string {
	base := "ergoLaw" + law.Name
	switch {
	case law.Owner != nil:
		base = "ergoLaw" + law.Owner.Name + law.Name
	case law.Machine != nil:
		base = "ergoLaw" + law.Machine.Name + law.Name
	}
	return l.pkgName(pkgKey{"law", law}, base)
}

// lawDecl lowers a law that runs to a Go function of its body in the test
// build (Laws): `func ergoLawName(env *Env, params...) (bool, error)`. Its
// result is the law's tail, or a returned value; what escapes it is its
// error. A member law's bare operation calls read the capability of its
// operation type from env, which the run binds to the provider under test.
func (l *lowerer) lawDecl(law *check.Law) {
	d := law.Decl
	l.tests.law = law
	defer func() { l.tests.law = nil }()
	// A law of an imported package is lowered within its file, reading its
	// package's Info, as that package's functions are (sources.go); a law
	// of std, as a std body is: with no line directive (decls.go).
	defer l.leave(l.enter(d.Name, law.Pkg))
	sourced := law.Pkg == nil || l.isSourced(law.Pkg)
	if !sourced {
		l.info = law.Pkg.Info
	}
	l.bodies++
	scope := newNamer(l.names)
	fc := newFuncCtx(l, scope, sourced)
	fc.site = d
	fc.owner = "law " + l.lawTitle(law)
	if sourced {
		l.recordDecl(fc.owner, d)
	}
	fc.env = scope.alloc("env")
	parts := []string{fc.env + " *" + l.env()}
	tparams := ""
	if own := lawGenericOwner(law); own != nil {
		// A law of a generic operation type is a Go generic function in
		// the type's parameters (types.md, Generics 5), and takes the
		// provider under test after the environment: its instance is no
		// environment field, since the law's own type names T (Laws 10).
		var names map[*check.TypeParam]string
		tparams, names = l.typeParams(own.TParams, scope, d)
		fc.params = names
		args := make([]string, len(own.TParams))
		for i, tp := range own.TParams {
			args[i] = names[tp]
		}
		l.tests.lawOwn = scope.alloc("own")
		parts = append(parts, l.tests.lawOwn+" "+fc.goType(own)+"["+strings.Join(args, ", ")+"]")
		defer func() { l.tests.lawOwn = "" }()
	}
	result := check.Type(check.Typ[check.Bool])
	fc.frame = newFrame(result, true, framePolls(l.info, d))
	fc.frame.law = true
	for _, v := range law.Params {
		if v.Name == "_" {
			parts = append(parts, scope.alloc("_p")+" "+fc.goType(v.Type))
			continue
		}
		n := scope.alloc(v.Name)
		fc.locals[v] = n
		parts = append(parts, n+" "+fc.goType(v.Type))
	}
	head := "func " + l.lawFunc(law) + tparams + "(" + strings.Join(parts, ", ") + ")"
	frameErr := ""
	needsFrame := l.bodyNeedsFrame(d.Body.List)
	if needsFrame {
		frameErr = scope.alloc("err")
		head += fc.namedResults(result, frameErr)
	} else {
		head += fc.resultList(result, true)
	}
	fc.depth = 1
	if needsFrame {
		fc.site = d
		fc.openFrame(frameErr)
	}
	fc.entryPoll(l.info, d)
	fc.body(d.Body.List)
	if sourced {
		l.user.WriteString(l.directive(d))
	}
	l.user.WriteString(head + " {\n")
	l.user.WriteString(fc.out.String())
	l.user.WriteString("}\n\n")
}

// testOf is the checked test of the declaration d.
func (l *lowerer) testOf(d *syntax.TestDecl) *check.Test {
	for _, t := range l.runTests() {
		if t.Decl == d {
			return t
		}
	}
	ice(d, "the test %s has no Tests entry", d.Name.Name)
	return nil
}

// testDecl lowers a test declaration: nothing in the run build (rule 1), a
// Go function of the test body in the test build. The body is a failure
// target, so the function returns what escapes it.
func (l *lowerer) testDecl(d *syntax.TestDecl) {
	if l.tests == nil {
		return
	}
	t := l.testOf(d)
	l.bodies++
	scope := newNamer(l.names)
	fc := newFuncCtx(l, scope, true)
	fc.site = d
	fc.owner = "test " + t.Name
	l.recordDecl(fc.owner, d)
	fc.env = scope.alloc("env")
	parts := []string{fc.env + " *" + l.env()}
	fc.frame = newFrame(nil, true, framePolls(l.info, d))
	for _, v := range t.Params {
		if v.Name == "_" {
			parts = append(parts, scope.alloc("_p")+" "+fc.goType(v.Type))
			continue
		}
		n := scope.alloc(v.Name)
		fc.locals[v] = n
		parts = append(parts, n+" "+fc.goType(v.Type))
	}
	head := "func " + l.testFunc(t) + "(" + strings.Join(parts, ", ") + ")"
	frameErr := ""
	needsFrame := l.bodyNeedsFrame(d.Body.List)
	if needsFrame {
		frameErr = scope.alloc("err")
		head += fc.namedResults(nil, frameErr)
	} else {
		head += fc.resultList(nil, true)
	}
	l.user.WriteString(l.directive(d))
	l.user.WriteString(head + " {\n")
	fc.depth = 1
	if needsFrame {
		fc.site = d
		fc.openFrame(frameErr)
	}
	fc.entryPoll(l.info, d)
	l.analyzeValues(d, l.info)
	fc.body(d.Body.List)
	l.user.WriteString(fc.out.String())
	l.user.WriteString("}\n\n")
}

// assert lowers `assert cond` in the test build. The run build never meets
// one: the checker refuses an assert outside a test or law body (S108), and
// the run build lowers no test body.
func (fc *funcCtx) assert(s *syntax.AssertStmt) {
	if fc.l.tests == nil {
		ice(s, "an assert reached the run build, which lowers no test or law body")
	}
	i, _, _, _ := fc.l.at(s.X, s.X.Pos())
	text := strconv.Quote(fc.l.srcs[i].text[s.X.Pos():s.X.End()])
	var cond string
	if x, lt, rt, ok := fc.l.assertShown(s.X); ok {
		cond, text = fc.assertCompare(x, lt, rt, text)
	} else {
		cond = fc.value(s.X)
	}
	fc.line("if !(" + cond + ") {")
	fc.line("\t" + fc.l.helper("AssertFail") + "(" + strconv.Quote(fc.l.siteText(s)) + ", " + text + ")")
	fc.line("}")
}

// assertCompare lowers the comparison x of an assert whose operands the
// report shows (assertShown): each operand is evaluated once, left to right,
// into a temporary (a constant stays inline), the comparison is binaryOf of
// those, and the failure text is the source text, then `: left L, right R`,
// each operand an Ergo literal of its type (literal.go, the property
// printer). Two strings, either of more than one line, add a line diff
// ($AssertDiff). The text is built only when the assert fails.
func (fc *funcCtx) assertCompare(x *syntax.BinaryExpr, lt, rt check.Type, text string) (cond, failure string) {
	operands := []syntax.Expr{x.X, x.Y}
	parts := fc.operands(operands, false)
	for k, e := range operands {
		if !fc.isConst(e) {
			parts[k] = fc.spill(parts[k])
		}
	}
	l := fc.l
	failure = text + " + " + strconv.Quote(": left ") + " + " + l.litExpr(lt, parts[0], x) + " + " + strconv.Quote(", right ") + " + " + l.litExpr(rt, parts[1], x)
	if isStringLike(lt) && isStringLike(rt) {
		failure += " + " + l.helper("AssertDiff") + "(string(" + parts[0] + "), string(" + parts[1] + "))"
	}
	return fc.binaryOf(x, parts), failure
}

// isStringLike reports string or a defined type over it.
func isStringLike(t check.Type) bool {
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	return isBasic(t, check.String)
}

// assertShown reports whether a failed assert of x shows its operands: x is
// a comparison (==, !=, <, <=, >, >=), not of two constants, whose operand
// types both have an argument literal (printable). It returns the
// comparison and the operand types. The naming pass (nameTests) and the
// lowering (assert) ask it the same question.
func (l *lowerer) assertShown(x syntax.Expr) (*syntax.BinaryExpr, check.Type, check.Type, bool) {
	b, ok := unparen(x).(*syntax.BinaryExpr)
	if !ok {
		return nil, nil, nil, false
	}
	switch b.Op.String() {
	case "==", "!=", "<", "<=", ">", ">=":
	default:
		return nil, nil, nil, false
	}
	_, lc := l.info.Values[b.X]
	_, rc := l.info.Values[b.Y]
	lt, lok := l.typedExpr(b.X)
	rt, rok := l.typedExpr(b.Y)
	if (lc && rc) || !lok || !rok || !l.printable(lt, map[*check.Named]bool{}) || !l.printable(rt, map[*check.Named]bool{}) {
		return nil, nil, nil, false
	}
	return b, lt, rt, true
}

// typedExpr is x's checked type, through parentheses.
func (l *lowerer) typedExpr(x syntax.Expr) (check.Type, bool) {
	for {
		if t, ok := l.info.Types[x]; ok {
			return t, true
		}
		p, isParen := x.(*syntax.ParenExpr)
		if !isParen {
			return nil, false
		}
		x = p.X
	}
}

// printable reports a type that has an argument literal (litExpr): a basic
// type; a slice, Map or tuple of printable types; or a record, union or
// defined type of the package under test over printable types. A type of
// another package, of std or of the prelude, an operation type, a function,
// a borrow and a machine have none here, so their asserts show only the
// source text, as before.
func (l *lowerer) printable(t check.Type, seen map[*check.Named]bool) bool {
	switch u := t.(type) {
	case *check.Basic:
		return true
	case *check.Slice:
		return l.printable(u.Elem, seen)
	case *check.Map:
		return l.printable(u.Key, seen) && l.printable(u.Elem, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if !l.printable(e, seen) {
				return false
			}
		}
		return true
	case *check.Union:
		// A row value prints as its member (q47).
		for _, v := range u.Variants {
			if !l.printable(v, seen) {
				return false
			}
		}
		return len(u.Variants) > 0
	case *check.Named:
		if seen[u] {
			return true
		}
		seen[u] = true
		if u.Obj.Pkg != l.pkg {
			return false
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				if !l.printable(f.Type, seen) {
					return false
				}
			}
			return true
		case *check.Union:
			for _, v := range under.Variants {
				if !l.printable(v, seen) {
					return false
				}
			}
			return true
		case *check.Basic, *check.Slice, *check.Map, *check.Tuple:
			return l.printable(under, seen)
		}
	}
	return false
}

// siteText is the position of the node n as a report names it:
// file:line:column, the file, the one that holds n (locate). A program's
// file is named by its base name; a std body's node, which no source of
// the program holds, by its std path (std/maps/maps.ergo), as noCase names
// it: the prelude's Unreachable called from std code (fs5, batch 12), and a
// law of std (law-xpkg).
func (l *lowerer) siteText(n syntax.Node) string {
	file, line, column := l.locate(n, n.Pos())
	if _, ok := l.sourceOf(n); ok {
		file = path.Base(strings.ReplaceAll(file, "\\", "/"))
	}
	return file + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(column)
}

// testEntry writes the test build's Go main and harness (Test mode, rule 2).
func (l *lowerer) testEntry(b *strings.Builder) {
	rt := l.rt()
	locals := newNamer(l.names)
	seed := locals.alloc("seed")
	b.WriteString("func main() {\n")
	b.WriteString("\t" + seed + " := " + l.helper("Seed") + "()\n")
	b.WriteString("\t" + rt + ".Main(" + l.rootTask(locals, "error", nil, func(env string) string {
		var s string
		if l.hasInits() {
			l.entryInits = true
			s = "\t\t" + l.pkgName(pkgKey{"init", nil}, "ergoInit") + "(" + env + ")\n"
		}
		return s + "\t\treturn " + l.helper("Tests") + "(" + env + ", " + seed + ")\n"
	}) + ")\n}\n\n")
	for _, r := range l.runLaws() {
		if lawLowered(r) && r.Law.Decl.Reach == nil {
			l.lawDecl(r.Law)
		}
	}
	b.WriteString(l.testRunner())
	b.WriteString(l.harness())
}

// testRunnerHead is the runner's declared header. It is apart from
// testRunner so that the runner's closures, which take no Env and capture
// the runner's, are written by a function that never names the Env type:
// only envClosure writes a closure that takes one.
func (l *lowerer) testRunnerHead(env, seed string) string {
	return "func " + l.helper("Tests") + "(" + env + " *" + l.env() + ", " + seed + " uint64) error {\n"
}

// testRunner is the Go function that runs every test in source order.
func (l *lowerer) testRunner() string {
	scope := newNamer(l.names)
	env, seed, failed, total := scope.alloc("env"), scope.alloc("seed"), scope.alloc("failed"), scope.alloc("total")
	fmtPkg, run := l.helper("fmt"), l.helper("Case")
	var b strings.Builder
	b.WriteString("// " + l.helper("Tests") + " runs every test in source order (196l).\n")
	b.WriteString(l.testRunnerHead(env, seed))
	tests := l.runTests()
	runs, skips := 0, 0
	for _, r := range l.runLaws() {
		if r.Law.Unrun != "" {
			skips++
			continue
		}
		for _, b := range r.Bindings {
			if bindingUnrun(b) == "" {
				runs++
			} else {
				skips++
			}
		}
		for _, lc := range r.Contexts {
			if lc.Unrun == "" {
				runs++
			} else {
				skips++
			}
		}
	}
	// A skip is never silent (fs4): each prints its skip line, and the
	// summary counts them.
	skipped := ""
	if skips > 0 {
		skipped = ", " + strconv.Itoa(skips) + " skipped"
	}
	b.WriteString("\t" + failed + ", " + total + " := 0, " + strconv.Itoa(len(tests)+runs) + "\n")
	for _, t := range tests {
		// The case runner takes the test's name, and its declaration's
		// site, which a typed failure that escapes the body names.
		name := strconv.Quote(t.Name) + ", " + strconv.Quote(l.siteText(t.Decl))
		if len(t.Params) == 0 {
			b.WriteString("\tif " + run + "(" + taskOf(env) + ", " + name + ", nil, func() error { return " + l.testFunc(t) + "(" + env + ") }) {\n")
			b.WriteString("\t\t" + fmtPkg + ".Println(" + strconv.Quote("ok "+t.Name) + ")\n")
			b.WriteString("\t} else {\n\t\t" + failed + "++\n\t}\n")
			continue
		}
		// A property follows the rule of laws (Listed laws): when its
		// parameters can all be listed, it takes each value once and is
		// proved; else it draws testCases generated cases.
		if l.listedTest(&b, t, scope, env, failed) {
			continue
		}
		inner := newNamer(scope)
		r, ok, i := inner.alloc("r"), inner.alloc("ok"), inner.alloc("i")
		b.WriteString("\t{\n")
		b.WriteString("\t\t" + r + " := " + l.helper("NewRand") + "(" + seed + ", " + strconv.Quote(t.Name) + ")\n")
		b.WriteString("\t\t" + r + ".env = " + env + "\n")
		b.WriteString("\t\t" + ok + " := true\n")
		b.WriteString("\t\tfor " + i + " := 1; " + ok + " && " + i + " <= " + strconv.Itoa(testCases) + "; " + i + "++ {\n")
		// The case's size grows with its number (QuickCheck's sized):
		// case i draws collections of up to i/4 elements at the top.
		b.WriteString("\t\t\t" + r + ".start(" + i + ")\n")
		args := []string{env}
		conv := l.helper("strconv")
		// The case's report: its number, the seed, the size and each
		// argument as an Ergo literal of its type (literal.go), written
		// only when it fails.
		shown := []string{strconv.Quote("case "), conv + ".Itoa(" + i + ")", strconv.Quote(" of " + strconv.Itoa(testCases) + ", seed "), conv + ".FormatUint(" + seed + ", 10)", strconv.Quote(", size "), conv + ".Itoa(" + i + " / 4)"}
		for k, v := range testplan.Test(t).Inputs {
			base := v.Name
			if base == "_" {
				base = "arg"
			}
			a := inner.alloc(base)
			b.WriteString("\t\t\t" + a + " := " + l.genExpr(v.Type, r, strconv.Itoa(testDepth), inner, t.Decl) + "\n")
			args = append(args, a)
			sep := ", "
			if k == 0 {
				sep = ": "
			}
			shown = append(shown, strconv.Quote(sep+v.Name+" = "), l.litExpr(v.Type, a, t.Decl))
		}
		b.WriteString("\t\t\t" + ok + " = " + run + "(" + taskOf(env) + ", " + name + ", func() string { return " + strings.Join(shown, " + ") + " }, func() error { return " + l.testFunc(t) + "(" + strings.Join(args, ", ") + ") })\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\tif " + ok + " {\n\t\t\t" + fmtPkg + ".Println(" + strconv.Quote(fmt.Sprintf("ok %s (%d cases)", t.Name, testCases)) + ")\n")
		b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n\t\t}\n\t}\n")
	}
	for _, law := range l.runLaws() {
		l.lawRuns(&b, law, scope, env, seed, failed)
	}
	b.WriteString("\tif " + failed + " > 0 {\n")
	b.WriteString("\t\t" + fmtPkg + ".Printf(\"FAIL (%d of %d tests" + skipped + ")\\n\", " + failed + ", " + total + ")\n")
	b.WriteString("\t\treturn " + fmtPkg + ".Errorf(\"%d of %d tests failed\", " + failed + ", " + total + ")\n\t}\n")
	b.WriteString("\t" + fmtPkg + ".Printf(\"PASS (%d tests" + skipped + ")\\n\", " + total + ")\n")
	b.WriteString("\treturn nil\n}\n\n")
	return b.String()
}

// lawSlotParam reports whether the law's parameter i is a slot, an
// operation type a run binds to a provider (check.Law.Slots).
// lawOwnOf is the provider parameter a law operation call c calls in a
// law of a generic operation type, "" for any other call or outside the
// test build.
func (t *testLowering) lawOwnOf(c *check.Call) string {
	if t == nil || c.Kind != check.CallLawOp {
		return ""
	}
	return t.lawOwn
}

// lawGenericOwner is the operation type of a law of a generic operation
// type that runs (its first slot), nil for any other law.
func lawGenericOwner(law *check.Law) *check.Named {
	return testplan.GenericOwner(law)
}

func lawSlotParam(law *check.Law, i int) bool {
	for _, s := range law.Slots {
		if s.Param == i {
			return true
		}
	}
	return false
}

// lawRuns writes the runs of one law into the runner b (Laws): a skip line
// when it does not run; a note when it is of the package under test and
// none of its bindings runs here (its providers' packages run it); a
// reachability run for a machine's reachability law (MU6); else one run per
// binding of its slots, a law with no slots having one. A run whose values
// can all be listed takes every value (listedRuns); another run takes
// testCases generated cases. Each run draws from its own stream, named by
// the law and the binding, so adding a law or a provider changes no other run's cases. A
// case builds each bound provider, binds the owner's, converted to the
// law's operation type, in a copy of the runner's environment, passes each
// parameter's, draws the other parameters, and calls the law's function: a
// false result refutes it ($Refuted), and an escaping failure or a failed
// assert fails it as a test's case does.
func (l *lowerer) lawRuns(b *strings.Builder, lr check.LawRun, scope *namer, env, seed, failed string) {
	law := lr.Law
	fmtPkg, run, conv := l.helper("fmt"), l.helper("Case"), l.helper("strconv")
	title := l.lawTitle(law)
	if law.Unrun != "" {
		b.WriteString("\t" + fmtPkg + ".Println(" + strconv.Quote("skip law "+title+": "+law.Unrun) + ")\n")
		return
	}
	// A machine's reachability law has no operation-type slot, so no
	// context binds it: it runs before the context runs are deferred.
	if l.machineLawRuns(b, law, scope, env, seed, failed) {
		return
	}
	// The runs under a context take the case's task from here, the one
	// classified reader of a law case's task (TestRuntime199TaskReaders).
	defer l.lawContextRuns(b, lr, scope, env, taskOf(env), seed, failed)
	if len(lr.Bindings) == 0 && len(lr.Contexts) > 0 {
		return
	}
	if len(lr.Bindings) == 0 {
		var types []string
		for _, s := range law.Slots {
			types = append(types, s.Type.Obj.Name)
		}
		b.WriteString("\t" + fmtPkg + ".Println(" + strconv.Quote("law "+title+" runs where "+strings.Join(types, " and ")+" has a provider: none is in this package or its imports") + ")\n")
		return
	}
	type target struct {
		label string // the report's name of the run
		b     []check.LawProvider
	}
	var targets []target
	for _, bd := range lr.Bindings {
		label := l.bindingLabel(law, bd)
		if why := bindingUnrun(bd); why != "" {
			b.WriteString("\t" + fmtPkg + ".Println(" + strconv.Quote("skip "+label+": "+why) + ")\n")
			continue
		}
		targets = append(targets, target{label: label, b: bd})
	}
	at := strconv.Quote(l.siteText(law.Decl))
	for _, tg := range targets {
		plan := testplan.Law(law, tg.b)
		// One rule for every law (Listed laws): a run whose values can
		// all be listed takes each once and is proved; else it draws.
		if l.listedRuns(b, law, tg.label, tg.b, scope, env, failed) {
			continue
		}
		inner := newNamer(scope)
		r, ok, i := inner.alloc("r"), inner.alloc("ok"), inner.alloc("i")
		name := strconv.Quote(tg.label) + ", " + at
		b.WriteString("\t{\n")
		b.WriteString("\t\t" + r + " := " + l.helper("NewRand") + "(" + seed + ", " + strconv.Quote(tg.label) + ")\n")
		b.WriteString("\t\t" + r + ".env = " + env + "\n")
		b.WriteString("\t\t" + ok + " := true\n")
		b.WriteString("\t\tfor " + i + " := 1; " + ok + " && " + i + " <= " + strconv.Itoa(testCases) + "; " + i + "++ {\n")
		b.WriteString("\t\t\t" + r + ".start(" + i + ")\n")
		shown := []string{strconv.Quote("case "), conv + ".Itoa(" + i + ")", strconv.Quote(" of " + strconv.Itoa(testCases) + ", seed "), conv + ".FormatUint(" + seed + ", 10)", strconv.Quote(", size "), conv + ".Itoa(" + i + " / 4)"}
		sep := ": "
		var bind []string
		e, held, err := inner.alloc("e"), inner.alloc("held"), inner.alloc("err")
		fc := newFuncCtx(l, inner, false)
		fc.site = law.Decl
		// provide writes the case's build of the provider p, named by base
		// and shown as `name = …`, and returns its value converted to the
		// slot's operation type.
		provide := func(p check.LawProvider, base, name string) string {
			pv := inner.alloc(base)
			if ctor := p.Ctor; ctor != nil {
				// The provider has no generator: its constructor builds
				// it for the case, from generated arguments (fs4).
				cargs := []string{env}
				shownArgs := []string{}
				for _, v := range ctor.Sig.Params {
					a := inner.alloc(v.Name)
					b.WriteString("\t\t\t" + a + " := " + l.genExpr(v.Type, r, strconv.Itoa(testDepth), inner, law.Decl) + "\n")
					cargs = append(cargs, a)
					shownArgs = append(shownArgs, l.litExpr(v.Type, a, law.Decl))
				}
				b.WriteString("\t\t\t" + pv + ", _ := " + fc.funcValue(ctor, ctor.Sig, law.Decl, nil) + "(" + strings.Join(cargs, ", ") + ")\n")
				call := strconv.Quote(sep + name + " = " + ctor.Name + "(")
				if len(shownArgs) > 0 {
					call += " + " + strings.Join(shownArgs, ` + ", " + `)
				}
				shown = append(shown, call, strconv.Quote(")"))
			} else {
				b.WriteString("\t\t\t" + pv + " := " + l.genExpr(p.Type, r, strconv.Itoa(testDepth), inner, law.Decl) + "\n")
				shown = append(shown, strconv.Quote(sep+name+" = "), l.litExpr(p.Type, pv, law.Decl))
			}
			sep = ", "
			return fc.convert(p.Conv, pv, law.Decl)
		}
		args := []string{"&" + e}
		callee := l.lawFunc(law)
		generic := lawGenericOwner(law) != nil
		for k, s := range law.Slots {
			if s.Param < 0 && generic {
				// A law of a generic operation type takes its provider
				// after the environment, at the instance the provider
				// fixes (lawDecl).
				args = append(args, provide(tg.b[k], "provider", "provider"))
				tc := &typeCtx{l: l, site: law.Decl}
				targs := make([]string, len(tg.b[k].Slot.TArgs))
				for i, a := range tg.b[k].Slot.TArgs {
					targs[i] = tc.goType(a)
				}
				callee += "[" + strings.Join(targs, ", ") + "]"
				continue
			}
			if s.Param < 0 {
				bind = append(bind, e+"."+l.envField(s.Type)+" = "+provide(tg.b[k], "provider", "provider"))
				continue
			}
		}
		paramPlans := plan.Inputs
		if len(paramPlans) > 0 && paramPlans[0].Param < 0 {
			paramPlans = paramPlans[1:]
		}
		for k, v := range law.Params {
			input := paramPlans[k]
			vt := input.Type
			base := v.Name
			if base == "_" {
				base = "arg"
			}
			if p := input.Provider; p != nil {
				args = append(args, provide(*p, base, v.Name))
				continue
			}
			a := inner.alloc(base)
			gen := l.genExpr(vt, r, strconv.Itoa(testDepth), inner, law.Decl)
			if input.Where != nil {
				gen = l.genRefined(vt, input.Where, r, strconv.Itoa(testDepth), inner, law.Decl)
			}
			b.WriteString("\t\t\t" + a + " := " + gen + "\n")
			args = append(args, a)
			shown = append(shown, strconv.Quote(sep+v.Name+" = "), l.litExpr(vt, a, law.Decl))
			sep = ", "
		}
		body := e + " := *" + env + "; " + strings.Join(append(bind, ""), "; ") +
			held + ", " + err + " := " + callee + "(" + strings.Join(args, ", ") + "); if " + err + " != nil { return " + err + " }; if !" + held + " { " + l.helper("Refuted") + "(" + at + ") }; return nil"
		b.WriteString("\t\t\t" + ok + " = " + run + "(" + taskOf(env) + ", " + name + ", func() string { return " + strings.Join(shown, " + ") + " }, func() error { " + body + " })\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\tif " + ok + " {\n\t\t\t" + fmtPkg + ".Println(" + strconv.Quote(fmt.Sprintf("ok %s (%d cases)", tg.label, testCases)) + ")\n")
		b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n\t\t}\n\t}\n")
	}
}

// harness is the test build's fixed Go: the assert, the case runner, the
// generator's source of randomness and the seed flag. Every package name
// it uses is a reserved helper or one of the two runtime imports ($Runtime,
// $Task); its locals name nothing else.
func (l *lowerer) harness() string {
	names := map[string]string{"Runtime": l.rt(), "Task": l.taskPkg(), "Depth": strconv.Itoa(testDepth)}
	for _, h := range testHelpers {
		names[h.key] = l.helper(h.key)
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	// Replace longer keys first, so no key eats a longer one it begins.
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	pairs := []string{}
	for _, k := range keys {
		pairs = append(pairs, "$"+k, names[k])
	}
	return strings.NewReplacer(pairs...).Replace(harnessGo + harnessReachGo)
}

const harnessGo = `// $Assertion is a failed assert, the panic that ends its case (196l).
// A refuted law is one too: its what is "law refuted", not "assert failed".
type $Assertion struct{ at, text, what string }

func (a $Assertion) String() string {
	what := a.what
	if what == "" {
		what = "assert failed"
	}
	return what + " at " + a.at + ": " + a.text
}

func $AssertFail(at, text string) { panic($Assertion{at: at, text: text}) }

// $Refuted ends a law's case whose law, declared at at, is false.
func $Refuted(at string) { panic($Assertion{at: at, text: "the law is false", what: "law refuted"}) }

// $AssertDiff is the line diff of a failed comparison of two strings, when
// they differ and either holds more than one line: a line feed, then each line of the
// longest common subsequence of their lines as "  " + line, a line only the
// left holds as "- " + line and one only the right holds as "+ " + line.
// One-line strings need none: the two literals show them.
func $AssertDiff(a, b string) string {
	if a == b || (!$strings.Contains(a, "\n") && !$strings.Contains(b, "\n")) {
		return ""
	}
	x, y := $strings.Split(a, "\n"), $strings.Split(b, "\n")
	// lcs[i][j] is the length of the longest common subsequence of x[i:]
	// and y[j:].
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out $strings.Builder
	out.WriteString("\n--- left\n+++ right")
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			out.WriteString("\n  " + x[i])
			i, j = i+1, j+1
		case j == len(y) || (i < len(x) && lcs[i+1][j] >= lcs[i][j+1]):
			out.WriteString("\n- " + x[i])
			i++
		default:
			out.WriteString("\n+ " + y[j])
			j++
		}
	}
	return out.String()
}

// $Case runs one case of the test name, declared at at, on the task t. A
// typed failure that escapes the body fails it: it prints a FAIL line that
// names the failure and the test's site, and returns false. A
// panic is read as the exit that survived the body's unwinding
// ($Runtime.Capture): when its reasons are failed asserts, with typed
// failures beside them, the case fails with the first assert. Any other
// exit, a defect an exit action raised after an assert began the unwind
// included, prints a FAIL line and is raised again whole ($Runtime.Raise),
// so the runtime reports every reason with its Ergo frames and the run
// stops.
func $Case(t *$Task.Task, name, at string, args func() string, body func() error) (ok bool) {
	done := false
	defer func() {
		if done {
			return
		}
		exit := $Runtime.Capture(t, recover())
		if a, failed := $Asserted(exit); failed {
			$fmt.Println("FAIL " + name + $Where(args) + ": " + a.String())
			ok = false
			return
		}
		$fmt.Println("FAIL " + name + $Where(args) + ": a defect, which stops the run")
		$Runtime.Raise(exit)
	}()
	err := body()
	done = true
	if err != nil {
		$fmt.Println("FAIL " + name + $Where(args) + ": a failure escaped the test at " + at + ": " + err.Error())
		return false
	}
	return true
}

// $Asserted is the first failed assert of exit, when every reason of exit is
// a failed assert or a typed failure and one is an assert. A failed assert
// is a defect whose value is an $Assertion: the panic itself, or the record
// a boundary made of it.
func $Asserted(exit error) (first $Assertion, failed bool) {
	c, isCause := exit.(*$Task.Cause)
	if !isCause || c == nil {
		return first, false
	}
	for _, r := range c.Reasons {
		switch r.Kind {
		case $Task.KindFailure:
		case $Task.KindDefect:
			v := r.Value
			if p, isPanic := v.(*$Runtime.Panic); isPanic {
				v = p.Value
			}
			a, isAssert := v.($Assertion)
			if !isAssert {
				return first, false
			}
			if !failed {
				first, failed = a, true
			}
		default:
			return first, false
		}
	}
	return first, failed
}

func $Where(args func() string) string {
	if args == nil {
		return ""
	}
	return " (" + args() + ")"
}

// $Rand draws a property's arguments: splitmix64, so a seed replays every
// case. size is the case's size, set by the runner before each case: case
// i of 100 has size i/4, so collections grow with the run (QuickCheck's
// sized). env is the test runner's environment, which a constructor that a
// generator calls runs in (check/tests.go rule 3): it has no rows, so it
// reads only the task. world is the case's values of the builders with no
// parameters (check/builders.go rule 4), by builder.
type $Rand struct {
	stream *$testgen.Stream
	size int // compatibility with the context runner's case boundary
	env   any
}

// start begins case i of a run: its size is i/4, and its world is empty,
// so no case sees another's file system or output.
func (r *$Rand) start(i int) {
	r.stream.Start(i)
	r.size = i / 4
}

// built is the case's one value of the builder key, which build makes on
// the case's first use of it.
func (r *$Rand) built(key string, build func() any) any {
	return r.stream.Built(key, build)
}

// $NewRand is the generator of the test name under seed: each property
// draws its own stream, so adding a test changes no other test's cases.
func $NewRand(seed uint64, name string) *$Rand {
	return &$Rand{stream: $testgen.New(seed, name)}
}

func (r *$Rand) next() uint64 {
	return r.stream.Next()
}

func (r *$Rand) intn(n int) int { return r.stream.Intn(n) }

func (r *$Rand) bool() bool { return r.stream.Bool() }

// The stream owns the seed mixer and every primitive distribution.
func (r *$Rand) i64() int64 {
	return r.stream.I64()
}

// f64 is always finite: a small half-integer half the time.
func (r *$Rand) f64() float64 {
	return r.stream.F64()
}

// str is a string biased to short ones: half the time 0..5 units, else
// 0..size. Each unit is drawn from a class a codec treats apart, so every
// class appears in a run: half the time a rune of $Runes, else a quote, a
// backslash, a control byte (0x00..0x1F), DEL, a byte that is not UTF-8
// (a lone continuation byte 0x80..0xBF, or 0xFE or 0xFF), an astral rune
// (U+10000..U+10FFFF), another BMP rune (no surrogate) or printable ASCII.
func (r *$Rand) str() string {
	r.stream.SetSize(r.size)
	return r.stream.Str()
}

// $GenIn draws an integer of one of the closed spans of a refinement
// (types.md, Refinements 9): a span, each as likely, then its low end, its
// high end, or any value in it, half the time an end. An unsigned type's
// spans hold its values' bits.
func $GenIn(r *$Rand, spans [][2]int64) int64 {
	return r.stream.In(spans)
}

// $GenStrLen is a string of n bytes, of str's units; a unit that would
// pass n is cut, as a codec meets a cut rune.
func $GenStrLen(r *$Rand, n int) string {
	return r.stream.StrLen(n)
}

// $GenSliceLen is a slice of n elements.
func $GenSliceLen[T any](r *$Rand, d int, n int, elem func(*$Rand, int) T) []T {
	xs := make([]T, n)
	for i := range xs {
		xs[i] = elem(r, d-1)
	}
	return xs
}

// limit is the largest collection length at depth d: the case's size at
// the top ($Depth), halved at each level below it, so nested collections
// stay small.
func (r *$Rand) limit(d int) int {
	r.stream.SetSize(r.size)
	return r.stream.Limit(d)
}

func $GenSlice[T any](r *$Rand, d int, elem func(*$Rand, int) T) []T {
	if d <= 0 {
		return []T{}
	}
	xs := make([]T, r.intn(r.limit(d)+1))
	for i := range xs {
		xs[i] = elem(r, d-1)
	}
	return xs
}

func $GenMap[K comparable, V any](r *$Rand, d int, key func(*$Rand, int) K, val func(*$Rand, int) V) map[K]V {
	m := map[K]V{}
	if d <= 0 {
		return m
	}
	for n := r.intn(r.limit(d) + 1); n > 0; n-- {
		m[key(r, d-1)] = val(r, d-1)
	}
	return m
}

// $LitFloat is the shortest text that reads back as v, with a decimal
// point or an exponent, so it is a float literal in any context.
func $LitFloat(v float64, bits int) string {
	s := $strconv.FormatFloat(v, 'g', -1, bits)
	if !$strings.ContainsAny(s, ".eEnN") {
		s += ".0"
	}
	return s
}

// $LitComplex is Go's form of a complex value, each part of bits: Ergo has
// no complex literal.
func $LitComplex(v complex128, bits int) string {
	return "complex(" + $LitFloat(real(v), bits) + ", " + $LitFloat(imag(v), bits) + ")"
}

// $LitDuration writes a duration in its largest exact unit: 1500ms, 2h.
func $LitDuration(n int64) string {
	if n == -1<<63 {
		return "-9223372036854775807ns - 1ns"
	}
	units := []struct {
		suffix string
		size   int64
	}{{"h", 3600e9}, {"m", 60e9}, {"s", 1e9}, {"ms", 1e6}, {"us", 1e3}}
	for _, u := range units {
		if n != 0 && n%u.size == 0 {
			return $strconv.FormatInt(n/u.size, 10) + u.suffix
		}
	}
	return $strconv.FormatInt(n, 10) + "ns"
}

// $LitEntries writes a Map literal of the type typ, its keys sorted: by
// less when the key type is ordered, else by the keys' text.
func $LitEntries[K comparable, V any](typ string, m map[K]V, key func(K) string, val func(V) string, less func(a, b K) bool) string {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	if less == nil {
		less = func(a, b K) bool { return key(a) < key(b) }
	}
	$sort.Slice(keys, func(i, j int) bool { return less(keys[i], keys[j]) })
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = key(k) + ": " + val(m[k])
	}
	return typ + "{" + $strings.Join(parts, ", ") + "}"
}

// $Seed reads --seed N or --seed=N, N a decimal uint64, the seed of every
// property; 1 by default. It skips --states N, which $States reads; any
// other argument is a usage error (exit 2).
func $Seed() uint64 {
	seed := uint64(1)
	args := $os.Args[1:]
	for i := 0; i < len(args); i++ {
		v, a := "", args[i]
		switch {
		case a == "--seed" && i+1 < len(args):
			i++
			v = args[i]
		case len(a) > len("--seed=") && a[:len("--seed=")] == "--seed=":
			v = a[len("--seed="):]
		case a == "--states" && i+1 < len(args):
			i++
			continue
		case len(a) > len("--states=") && a[:len("--states=")] == "--states=":
			continue
		}
		n, err := $strconv.ParseUint(v, 10, 64)
		if err != nil {
			$fmt.Fprintln($os.Stderr, "usage: [--seed N], not "+a)
			$os.Exit(2)
		}
		seed = n
	}
	return seed
}

`

// Generators (rule 3; check/tests.go rule 3 decides which types have one).
// A basic type, a slice, a Map and a tuple are drawn inline; each named type
// has one generated Go function, `func ergoGenT(r *ergoRand, d int) T`,
// keyed by its Go spelling (typeKey), so recursive types call themselves.
// d is the size left: a union at d <= 0 takes a variant of least rank
// (check.ValueRanks), which ends every recursion.

// genNamed calls visit on each named type t reaches through a generator.
func (l *lowerer) genNamed(t check.Type, seen map[check.Type]bool, visit func(*check.Named)) {
	switch u := t.(type) {
	case *check.Slice:
		l.genNamed(u.Elem, seen, visit)
	case *check.Map:
		l.genNamed(u.Key, seen, visit)
		l.genNamed(u.Elem, seen, visit)
	case *check.Tuple:
		for _, e := range u.Types {
			l.genNamed(e, seen, visit)
		}
	case *check.Union:
		for _, v := range u.Variants {
			l.genNamed(v, seen, visit)
		}
	case *check.Named:
		if seen[u] {
			return
		}
		seen[u] = true
		visit(u)
		if _, isOps := u.Underlying().(*check.Interface); isOps {
			if b := l.pkg.Builder(u); b != nil {
				_, params := b.Instance(u)
				for _, p := range params {
					l.genNamed(p, seen, visit)
				}
			}
			return
		}
		if ctor := genConstructor(u); ctor != nil {
			for _, v := range ctor.Sig.Params {
				l.genNamed(v.Type, seen, visit)
			}
			return
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				l.genNamed(f.Type, seen, visit)
			}
		case *check.Union:
			for _, v := range under.Variants {
				l.genNamed(v, seen, visit)
			}
		default:
			l.genNamed(under, seen, visit)
		}
	}
}

// genConstructor is the constructor a generator builds n's values with, or
// nil when it builds them field by field (check/tests.go rule 3).
func genConstructor(n *check.Named) *check.Func {
	return testplan.Constructor(n)
}

func (l *lowerer) genName(n *check.Named) string {
	return l.pkgName(pkgKey{"gen", l.typeKey(n)}, "ergoGen"+n.Obj.Name)
}

// genExpr is the Go expression that draws a value of t from the generator
// r with size d; scope holds the enclosing locals.
func (l *lowerer) genExpr(t check.Type, r, d string, scope *namer, site syntax.Node) string {
	tc := &typeCtx{l: l, site: site}
	elem := func(e check.Type) string {
		inner := newNamer(scope)
		ir, id := inner.alloc("r"), inner.alloc("d")
		return "func(" + ir + " *" + l.helper("Rand") + ", " + id + " int) " + tc.goType(e) + " { return " + l.genExpr(e, ir, id, inner, site) + " }"
	}
	switch u := t.(type) {
	case *check.Basic:
		switch {
		case u.Kind == check.Bool:
			return r + ".bool()"
		case u.Kind == check.String:
			return r + ".str()"
		case u.Kind == check.Duration:
			return tc.goType(u) + "(" + r + ".i64())"
		case u.Kind == check.Float32 || u.Kind == check.Float64:
			return u.Name + "(" + r + ".f64())"
		case u.Kind == check.Complex64:
			return "complex(float32(" + r + ".f64()), float32(" + r + ".f64()))"
		case u.Kind == check.Complex128:
			return "complex(" + r + ".f64(), " + r + ".f64())"
		case u.Kind >= check.Int && u.Kind <= check.Uintptr:
			return u.Name + "(" + r + ".i64())"
		}
	case *check.Slice:
		return l.helper("GenSlice") + "(" + r + ", " + d + ", " + elem(u.Elem) + ")"
	case *check.Map:
		return l.helper("GenMap") + "(" + r + ", " + d + ", " + elem(u.Key) + ", " + elem(u.Elem) + ")"
	case *check.Tuple:
		parts := make([]string, len(u.Types))
		for i, e := range u.Types {
			parts[i] = tupleField(i) + ": " + l.genExpr(e, r, d+"-1", scope, site)
		}
		return tc.goType(u) + "{" + strings.Join(parts, ", ") + "}"
	case *check.Named:
		return l.genFunc(u, site) + "(" + r + ", " + d + ")"
	case *check.Union:
		// A row value (q47): a member, drawn as a union's variant is, in a
		// function literal called at once.
		inner := newNamer(scope)
		ir, id := inner.alloc("r"), inner.alloc("d")
		var body strings.Builder
		l.genPick(&body, u, u.Variants, ir, id, inner, site)
		return "func(" + ir + " *" + l.helper("Rand") + ", " + id + " int) error {\n" + body.String() + "}(" + r + ", " + d + ")"
	}
	ice(site, "a property parameter of type %s, which has no generator; the checker admits only generatable types", t)
	return ""
}

// lenCap bounds the lengths a refinement's generator draws above a span's
// low end, so an unbounded `len(it) >= 1` stays small.
const lenCap = 32

// genRefined is the Go expression that draws a value of t inside the
// refinement ref (types.md, Refinements 9): t itself is refined, or t is a
// law parameter's type and ref its where.
func (l *lowerer) genRefined(t check.Type, ref *check.Refinement, r, d string, scope *namer, site syntax.Node) string {
	tc := &typeCtx{l: l, site: site}
	unsigned := false
	under := t
	if n, ok := t.(*check.Named); ok {
		under = n.Underlying()
	}
	if b, ok := under.(*check.Basic); ok {
		unsigned = b.Kind >= check.Uint && b.Kind <= check.Uintptr
	}
	var spans []string
	for _, s := range ref.Spans {
		lo, hi := new(big.Int).Set(s.Lo), new(big.Int).Set(s.Hi)
		if ref.Len {
			if top := new(big.Int).Add(lo, big.NewInt(lenCap)); hi.Cmp(top) > 0 {
				hi = top
			}
		}
		spans = append(spans, "{"+int64Bits(lo)+", "+int64Bits(hi)+"}")
	}
	in := l.helper("GenIn") + "(" + r + ", [][2]int64{" + strings.Join(spans, ", ") + "})"
	if !ref.Len {
		if unsigned {
			return tc.goType(t) + "(uint64(" + in + "))"
		}
		return tc.goType(t) + "(" + in + ")"
	}
	if s, ok := under.(*check.Slice); ok {
		inner := newNamer(scope)
		ir, id := inner.alloc("r"), inner.alloc("d")
		elem := "func(" + ir + " *" + l.helper("Rand") + ", " + id + " int) " + tc.goType(s.Elem) + " { return " + l.genExpr(s.Elem, ir, id, inner, site) + " }"
		return tc.goType(t) + "(" + l.helper("GenSliceLen") + "(" + r + ", " + d + ", int(" + in + "), " + elem + "))"
	}
	return tc.goType(t) + "(" + l.helper("GenStrLen") + "(" + r + ", int(" + in + ")))"
}

// int64Bits is v as a Go int64 literal of its two's-complement bits, so a
// uint64 above MaxInt64 keeps its bits.
func int64Bits(v *big.Int) string {
	if v.IsInt64() {
		return v.String()
	}
	return strconv.FormatInt(int64(v.Uint64()), 10)
}

// genPick writes the body that draws one of variants, the variants of the
// union t or the members of the row value t: at d <= 0 one of least rank,
// else any, each equally likely.
func (l *lowerer) genPick(body *strings.Builder, t check.Type, variants []check.Type, r, d string, scope *namer, site syntax.Node) {
	small := testplan.SmallVariants(variants)
	if len(small) == 0 {
		ice(site, "the union %s has no finite value; the checker admits only generatable types", t)
	}
	pick := func(vs []check.Type, depth string) {
		if len(vs) == 1 {
			body.WriteString("\t\treturn " + l.genExpr(vs[0], r, depth, scope, site) + "\n")
			return
		}
		body.WriteString("\t\tswitch " + r + ".intn(" + strconv.Itoa(len(vs)) + ") {\n")
		for i, v := range vs[:len(vs)-1] {
			body.WriteString("\t\tcase " + strconv.Itoa(i) + ":\n\t\t\treturn " + l.genExpr(v, r, depth, scope, site) + "\n")
		}
		body.WriteString("\t\t}\n\t\treturn " + l.genExpr(vs[len(vs)-1], r, depth, scope, site) + "\n")
	}
	body.WriteString("\tif " + d + " <= 0 {\n")
	pick(small, d+"-1")
	body.WriteString("\t}\n\t{\n")
	pick(variants, d+"-1")
	body.WriteString("\t}\n")
}

// genFunc is the generator function of the named type n, emitted once.
func (l *lowerer) genFunc(n *check.Named, site syntax.Node) string {
	key := pkgKey{"gen", l.typeKey(n)}
	name := l.genName(n)
	if l.tests.gens[key] {
		return name
	}
	l.tests.gens[key] = true
	tc := &typeCtx{l: l, site: site}
	goType := tc.goType(n)
	scope := newNamer(l.names)
	r, d := scope.alloc("r"), scope.alloc("d")
	var body strings.Builder
	if b := l.opBuilder(n, site); b != nil {
		// A capability is built by its builder (builders.go), in the
		// runner's environment unless it is a runtime/v1 body.
		ev := ""
		if !l.stdBuilt(b, site) {
			ev = scope.alloc("env")
			body.WriteString("\t" + ev + ", _ := " + r + ".env.(*" + l.env() + ")\n")
		}
		l.genBuilt(n, b, r, d, ev, scope, site, &body)
		l.gen.WriteString("func " + name + "(" + r + " *" + l.helper("Rand") + ", " + d + " int) " + goType + " {\n" + body.String() + "}\n\n")
		return name
	}
	if ctor := genConstructor(n); ctor != nil {
		// Built as its package builds it: by its constructor, from
		// generated arguments (check/tests.go rule 3). The constructor has
		// no rows, so it reads no environment and fails nothing.
		fc := newFuncCtx(l, scope, false)
		fc.site = site
		ev := scope.alloc("env")
		body.WriteString("\t" + ev + ", _ := " + r + ".env.(*" + l.env() + ")\n")
		args := []string{ev}
		for _, v := range ctor.Sig.Params {
			a := scope.alloc(v.Name)
			body.WriteString("\t" + a + " := " + l.genExpr(v.Type, r, d+"-1", scope, site) + "\n")
			args = append(args, a)
		}
		v := scope.alloc("v")
		body.WriteString("\t" + v + ", _ := " + fc.funcValue(ctor, ctor.Sig, site, nil) + "(" + strings.Join(args, ", ") + ")\n")
		body.WriteString("\treturn " + v + "\n")
		l.gen.WriteString("func " + name + "(" + r + " *" + l.helper("Rand") + ", " + d + " int) " + goType + " {\n" + body.String() + "}\n\n")
		return name
	}
	if ref := check.RefinementOf(n); ref != nil {
		body.WriteString("\treturn " + l.genRefined(n, ref, r, d, scope, site) + "\n")
		l.gen.WriteString("func " + name + "(" + r + " *" + l.helper("Rand") + ", " + d + " int) " + goType + " {\n" + body.String() + "}\n\n")
		return name
	}
	switch under := n.Underlying().(type) {
	case *check.Struct:
		td := l.typeDecl(n.Obj, site)
		if td.goError {
			body.WriteString("\treturn " + l.rt() + ".GoErrorText(" + l.genExpr(under.Fields[0].Type, r, d+"-1", scope, site) + ")\n")
			break
		}
		parts := make([]string, len(under.Fields))
		for i, f := range under.Fields {
			field, ok := td.fieldName[f.Name]
			if !ok {
				ice(site, "%s has no field %s", td.name, f.Name)
			}
			parts[i] = field + ": " + l.genExpr(f.Type, r, d+"-1", scope, site)
		}
		body.WriteString("\treturn " + goType + "{" + strings.Join(parts, ", ") + "}\n")
	case *check.Union:
		if fu := l.flatUnionOf(n); fu != nil {
			flatBail(site, fu) // a property's argument keeps its interface (flatunion.go)
		}
		l.genPick(&body, n, under.Variants, r, d, scope, site)
	default:
		body.WriteString("\treturn " + goType + "(" + l.genExpr(under, r, d, scope, site) + ")\n")
	}
	l.gen.WriteString("func " + name + "(" + r + " *" + l.helper("Rand") + ", " + d + " int) " + goType + " {\n" + body.String() + "}\n\n")
	return name
}
