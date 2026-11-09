package lower

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
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
//  3. An example test runs once; a property runs 100 cases, each with
//     arguments a generator derived from its parameter types draws
//     (testGen). A case fails when an assert in it fails, wherever the
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
const testCases = 100

// testDepth is the size a property's arguments start with: each record,
// tuple, union, slice or Map level lowers it by one, and at zero a slice
// and a Map are empty and a union takes its smallest variant.
const testDepth = 4

// testLowering is the test build's state; the run build has none.
type testLowering struct {
	gens map[pkgKey]bool // generator functions emitted
	lits map[pkgKey]bool // argument literal functions emitted (literal.go)
	// law is the law whose body is being lowered, nil outside one: a bare
	// operation call in it (CallLawOp) calls its operation type's
	// capability, which the run binds to the provider under test.
	law *check.Law
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
	return []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: code}}, nil
}

// testHelpers are the harness's package-scope entities: key and preferred
// name, in the order the naming pass reserves them.
var testHelpers = []struct{ key, base string }{
	{"fmt", "fmt"}, {"os", "os"}, {"sort", "sort"}, {"strconv", "strconv"}, {"strings", "strings"},
	{"Assertion", "ergoAssertion"}, {"AssertFail", "ergoAssertFail"}, {"AssertDiff", "ergoAssertDiff"}, {"Refuted", "ergoRefuted"},
	{"Case", "ergoCase"}, {"Asserted", "ergoAsserted"}, {"Where", "ergoWhere"},
	{"Rand", "ergoRand"}, {"NewRand", "ergoNewRand"}, {"Runes", "ergoRunes"}, {"Edges", "ergoEdges"},
	{"GenSlice", "ergoGenSlice"}, {"GenMap", "ergoGenMap"},
	{"LitFloat", "ergoLitFloat"}, {"LitComplex", "ergoLitComplex"},
	{"LitDuration", "ergoLitDuration"}, {"LitEntries", "ergoLitEntries"},
	{"Seed", "ergoSeed"}, {"Tests", "ergoTests"},
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
		law := r.Law
		l.lawFunc(law)
		if !lawLowered(r) {
			continue
		}
		for i, v := range law.Params {
			if !lawSlotParam(law, i) {
				reserveGen(v.Type)
			}
		}
		for _, b := range r.Bindings {
			if bindingUnrun(b) != "" {
				continue
			}
			for _, p := range b {
				if p.Ctor != nil {
					for _, v := range p.Ctor.Sig.Params {
						reserveGen(v.Type)
					}
				} else {
					reserveGen(p.Type)
				}
			}
		}
	}
	for _, t := range l.runTests() {
		l.testFunc(t)
		for _, v := range t.Params {
			reserveGen(v.Type)
		}
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
	return false
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
	if law.Owner != nil {
		title = law.Owner.Name + "." + law.Name
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
	if law.Owner != nil {
		base = "ergoLaw" + law.Owner.Name + law.Name
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
	head := "func " + l.lawFunc(law) + "(" + strings.Join(parts, ", ") + ")"
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
	seed, task, env := locals.alloc("seed"), locals.alloc("t"), locals.alloc("env")
	b.WriteString("func main() {\n")
	b.WriteString("\t" + seed + " := " + l.helper("Seed") + "()\n")
	b.WriteString("\t" + rt + ".Main(func(" + task + " *" + l.taskPkg() + ".Task) error {\n")
	b.WriteString("\t\t" + env + " := &" + l.env() + "{" + envTask + ": " + task + "}\n")
	if l.hasInits() {
		b.WriteString("\t\t" + l.pkgName(pkgKey{"init", nil}, "ergoInit") + "(" + env + ")\n")
	}
	b.WriteString("\t\treturn " + l.helper("Tests") + "(" + env + ", " + seed + ")\n")
	b.WriteString("\t})\n}\n\n")
	for _, r := range l.runLaws() {
		if lawLowered(r) {
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
		inner := newNamer(scope)
		r, ok, i := inner.alloc("r"), inner.alloc("ok"), inner.alloc("i")
		b.WriteString("\t{\n")
		b.WriteString("\t\t" + r + " := " + l.helper("NewRand") + "(" + seed + ", " + strconv.Quote(t.Name) + ")\n")
		b.WriteString("\t\t" + r + ".env = " + env + "\n")
		b.WriteString("\t\t" + ok + " := true\n")
		b.WriteString("\t\tfor " + i + " := 1; " + ok + " && " + i + " <= " + strconv.Itoa(testCases) + "; " + i + "++ {\n")
		// The case's size grows with its number (QuickCheck's sized):
		// case i draws collections of up to i/4 elements at the top.
		b.WriteString("\t\t\t" + r + ".size = " + i + " / 4\n")
		args := []string{env}
		conv := l.helper("strconv")
		// The case's report: its number, the seed, the size and each
		// argument as an Ergo literal of its type (literal.go), written
		// only when it fails.
		shown := []string{strconv.Quote("case "), conv + ".Itoa(" + i + ")", strconv.Quote(" of " + strconv.Itoa(testCases) + ", seed "), conv + ".FormatUint(" + seed + ", 10)", strconv.Quote(", size "), conv + ".Itoa(" + i + " / 4)"}
		for k, v := range t.Params {
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
// none of its bindings runs here (its providers' packages run it); else one
// run of testCases cases per binding of its slots, a law with no slots
// having one. Each run draws from its own stream, named by the law and the
// binding, so adding a law or a provider changes no other run's cases. A
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
	if len(law.Slots) == 0 && len(law.Params) == 0 {
		// A top-level law with no parameters draws nothing: it runs once,
		// as an example test does.
		label := "law " + title
		e, held, err := scope.alloc("e"), scope.alloc("held"), scope.alloc("err")
		body := e + " := *" + env + "; " + held + ", " + err + " := " + l.lawFunc(law) + "(&" + e + "); if " + err + " != nil { return " + err + " }; if !" + held + " { " + l.helper("Refuted") + "(" + at + ") }; return nil"
		b.WriteString("\tif " + run + "(" + taskOf(env) + ", " + strconv.Quote(label) + ", " + at + ", nil, func() error { " + body + " }) {\n")
		b.WriteString("\t\t" + fmtPkg + ".Println(" + strconv.Quote("ok "+label) + ")\n")
		b.WriteString("\t} else {\n\t\t" + failed + "++\n\t}\n")
		return
	}
	for _, tg := range targets {
		inner := newNamer(scope)
		r, ok, i := inner.alloc("r"), inner.alloc("ok"), inner.alloc("i")
		name := strconv.Quote(tg.label) + ", " + at
		b.WriteString("\t{\n")
		b.WriteString("\t\t" + r + " := " + l.helper("NewRand") + "(" + seed + ", " + strconv.Quote(tg.label) + ")\n")
		b.WriteString("\t\t" + r + ".env = " + env + "\n")
		b.WriteString("\t\t" + ok + " := true\n")
		b.WriteString("\t\tfor " + i + " := 1; " + ok + " && " + i + " <= " + strconv.Itoa(testCases) + "; " + i + "++ {\n")
		b.WriteString("\t\t\t" + r + ".size = " + i + " / 4\n")
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
		bound := map[int]check.LawProvider{}
		for k, s := range law.Slots {
			if s.Param < 0 {
				bind = append(bind, e+"."+l.envField(s.Type)+" = "+provide(tg.b[k], "provider", "provider"))
				continue
			}
			bound[s.Param] = tg.b[k]
		}
		args := []string{"&" + e}
		for k, v := range law.Params {
			base := v.Name
			if base == "_" {
				base = "arg"
			}
			if p, ok := bound[k]; ok {
				args = append(args, provide(p, base, v.Name))
				continue
			}
			a := inner.alloc(base)
			b.WriteString("\t\t\t" + a + " := " + l.genExpr(v.Type, r, strconv.Itoa(testDepth), inner, law.Decl) + "\n")
			args = append(args, a)
			shown = append(shown, strconv.Quote(sep+v.Name+" = "), l.litExpr(v.Type, a, law.Decl))
			sep = ", "
		}
		body := e + " := *" + env + "; " + strings.Join(append(bind, ""), "; ") +
			held + ", " + err + " := " + l.lawFunc(law) + "(" + strings.Join(args, ", ") + "); if " + err + " != nil { return " + err + " }; if !" + held + " { " + l.helper("Refuted") + "(" + at + ") }; return nil"
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
	return strings.NewReplacer(pairs...).Replace(harnessGo)
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
// reads only the task.
type $Rand struct {
	s    uint64
	size int
	env  any
}

// $NewRand is the generator of the test name under seed: each property
// draws its own stream, so adding a test changes no other test's cases.
func $NewRand(seed uint64, name string) *$Rand {
	r := &$Rand{s: seed}
	for i := 0; i < len(name); i++ {
		r.s = r.s*31 + uint64(name[i])
	}
	return r
}

func (r *$Rand) next() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *$Rand) intn(n int) int { return int(r.next() % uint64(n)) }

func (r *$Rand) bool() bool { return r.next()&1 == 0 }

// $Edges are the integers where code breaks: zero, one and minus one, and
// the bounds of each Go integer width. A narrower type wraps an int64, so
// each width meets its own bounds (127 and -128 for int8, 255 and 256 for
// uint8, …).
var $Edges = []int64{0, 1, -1, 127, -128, 128, -129, 255, 256, 32767, -32768, 65535, 65536, 2147483647, -2147483648, 4294967295, 4294967296, 9223372036854775807, -9223372036854775808}

// i64 is biased to small values and edges: 40% in -10..10, 15% an edge
// ($Edges), 15% in -999999..999999 and 30% any 64 bits; a narrower type
// wraps it.
func (r *$Rand) i64() int64 {
	switch k := r.intn(20); {
	case k < 8:
		return int64(r.intn(21)) - 10
	case k < 11:
		return $Edges[r.intn(len($Edges))]
	case k < 14:
		return int64(r.intn(1999999)) - 999999
	}
	return int64(r.next())
}

// f64 is always finite: a small half-integer half the time.
func (r *$Rand) f64() float64 {
	if r.bool() {
		return float64(r.intn(21)-10) / 2
	}
	return float64(int64(r.next())) / (1 << 32)
}

var $Runes = []rune("abz AZ09_-é世\n")

// str is a string biased to short ones: half the time 0..5 units, else
// 0..size. Each unit is drawn from a class a codec treats apart, so every
// class appears in a run: half the time a rune of $Runes, else a quote, a
// backslash, a control byte (0x00..0x1F), DEL, a byte that is not UTF-8
// (a lone continuation byte 0x80..0xBF, or 0xFE or 0xFF), an astral rune
// (U+10000..U+10FFFF), another BMP rune (no surrogate) or printable ASCII.
func (r *$Rand) str() string {
	n := r.intn(6)
	if r.bool() {
		n = r.intn(r.size + 1)
	}
	b := make([]byte, 0, n*4)
	for i := 0; i < n; i++ {
		switch r.intn(16) {
		case 8:
			b = append(b, '"')
		case 9:
			b = append(b, '\\')
		case 10:
			b = append(b, byte(r.intn(0x20)))
		case 11:
			b = append(b, 0x7f)
		case 12:
			if k := r.intn(0x42); k < 0x40 {
				b = append(b, byte(0x80+k))
			} else {
				b = append(b, byte(0xFE+k-0x40))
			}
		case 13:
			b = append(b, string(rune(0x10000+r.intn(0x100000)))...)
		case 14:
			b = append(b, string(rune(0x80+r.intn(0xD800-0x80)))...)
		case 15:
			b = append(b, byte(0x20+r.intn(0x5F)))
		default:
			b = append(b, string($Runes[r.intn(len($Runes))])...)
		}
	}
	return string(b)
}

// limit is the largest collection length at depth d: the case's size at
// the top ($Depth), halved at each level below it, so nested collections
// stay small.
func (r *$Rand) limit(d int) int {
	n := r.size
	for k := d; k < $Depth && n > 0; k++ {
		n /= 2
	}
	return n
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
// property; 1 by default. Any other argument is a usage error (exit 2).
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
	case *check.Named:
		if seen[u] {
			return
		}
		seen[u] = true
		visit(u)
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
	if n.Obj == nil || n.Orig != nil {
		return nil
	}
	return n.Obj.Constructor()
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
	}
	ice(site, "a property parameter of type %s, which has no generator; the checker admits only generatable types", t)
	return ""
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
	switch under := n.Underlying().(type) {
	case *check.Struct:
		td := l.typeDecl(n.Obj, site)
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
		ranks := check.NewValueRanks()
		least := -1
		var small []check.Type
		for _, v := range under.Variants {
			switch k := ranks.Rank(v); {
			case k < 0:
			case least < 0 || k < least:
				least, small = k, []check.Type{v}
			case k == least:
				small = append(small, v)
			}
		}
		if len(small) == 0 {
			ice(site, "the union %s has no finite value; the checker admits only generatable types", n)
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
		pick(under.Variants, d+"-1")
		body.WriteString("\t}\n")
	default:
		body.WriteString("\treturn " + goType + "(" + l.genExpr(under, r, d, scope, site) + ")\n")
	}
	l.gen.WriteString("func " + name + "(" + r + " *" + l.helper("Rand") + ", " + d + " int) " + goType + " {\n" + body.String() + "}\n\n")
	return name
}
