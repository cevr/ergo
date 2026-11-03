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
// records the case's first failed assert and panics. The case reads the
// record and not the panic value, so the frames the panic crossed (a with
// scope, a task boundary) cannot hide the assert; with no record, it does
// not recover, so a defect keeps its stack for the runtime's report.
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
}

// LowerTests lowers the checked unit to the Go module of its test binary:
// the package as Lower lowers it, each test as a Go function, and a main
// that runs the tests (see Test mode). It takes the unit Lower takes.
func LowerTests(u Unit) ([]File, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	if len(u.Files) != 1 {
		return nil, fmt.Errorf("lower: %d files in one package; packages of several files are 196b's", len(u.Files))
	}
	l := newBuildLowerer(u, &testLowering{gens: map[pkgKey]bool{}})
	code, err := l.program()
	if err != nil {
		return nil, err
	}
	return []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: code}}, nil
}

// testHelpers are the harness's package-scope entities: key and preferred
// name, in the order the naming pass reserves them.
var testHelpers = []struct{ key, base string }{
	{"fmt", "fmt"}, {"os", "os"}, {"sync", "sync"},
	{"Assertion", "ergoAssertion"}, {"AssertFail", "ergoAssertFail"},
	{"FailedMu", "ergoFailedMu"}, {"Failed", "ergoFailed"},
	{"Case", "ergoCase"}, {"Where", "ergoWhere"},
	{"Rand", "ergoRand"}, {"NewRand", "ergoNewRand"}, {"Runes", "ergoRunes"},
	{"GenSlice", "ergoGenSlice"}, {"GenMap", "ergoGenMap"},
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
// test's function and the generator of each named type a property's
// parameters reach. The run build reserves none (rule 1).
func (l *lowerer) nameTests() {
	if l.tests == nil {
		return
	}
	for _, h := range testHelpers {
		l.helper(h.key)
	}
	for _, t := range l.pkg.Tests {
		l.testFunc(t)
		for _, v := range t.Params {
			l.genNamed(v.Type, map[check.Type]bool{}, func(n *check.Named) { l.genName(n) })
		}
	}
}

// testImports are the import lines the test build adds.
func (l *lowerer) testImports() []string {
	if l.tests == nil {
		return nil
	}
	return []string{l.helper("fmt") + ` "fmt"`, l.helper("os") + ` "os"`, l.helper("sync") + ` "sync"`}
}

func (l *lowerer) testFunc(t *check.Test) string {
	return l.pkgName(pkgKey{"test", t}, "ergoTest"+t.Name)
}

// testOf is the checked test of the declaration d.
func (l *lowerer) testOf(d *syntax.TestDecl) *check.Test {
	for _, t := range l.pkg.Tests {
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
	l.user.WriteString(l.directive(d.Pos()))
	l.user.WriteString(head + " {\n")
	fc.depth = 1
	if needsFrame {
		fc.site = d
		fc.openFrame(frameErr)
	}
	fc.entryPoll(l.info, d)
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
	cond := fc.value(s.X)
	line, column := fc.l.file.Position(s.Pos())
	at := path.Base(strings.ReplaceAll(fc.l.src, "\\", "/")) + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(column)
	text := fc.l.unit.Sources[0].Text[s.X.Pos():s.X.End()]
	fc.line("if !(" + cond + ") {")
	fc.line("\t" + fc.l.helper("AssertFail") + "(" + strconv.Quote(at) + ", " + strconv.Quote(text) + ")")
	fc.line("}")
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
	b.WriteString("\t" + failed + ", " + total + " := 0, " + strconv.Itoa(len(l.pkg.Tests)) + "\n")
	for _, t := range l.pkg.Tests {
		name := strconv.Quote(t.Name)
		if len(t.Params) == 0 {
			b.WriteString("\tif " + run + "(" + name + ", nil, func() error { return " + l.testFunc(t) + "(" + env + ") }) {\n")
			b.WriteString("\t\t" + fmtPkg + ".Println(" + strconv.Quote("ok "+t.Name) + ")\n")
			b.WriteString("\t} else {\n\t\t" + failed + "++\n\t}\n")
			continue
		}
		inner := newNamer(scope)
		r, ok, i := inner.alloc("r"), inner.alloc("ok"), inner.alloc("i")
		b.WriteString("\t{\n")
		b.WriteString("\t\t" + r + " := " + l.helper("NewRand") + "(" + seed + ", " + name + ")\n")
		b.WriteString("\t\t" + ok + " := true\n")
		b.WriteString("\t\tfor " + i + " := 1; " + ok + " && " + i + " <= " + strconv.Itoa(testCases) + "; " + i + "++ {\n")
		args := []string{env}
		format := "case %d of " + strconv.Itoa(testCases) + ", seed %d"
		shown := []string{i, seed}
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
			format += sep + v.Name + " = %+v"
			shown = append(shown, a)
		}
		b.WriteString("\t\t\t" + ok + " = " + run + "(" + name + ", func() string { return " + fmtPkg + ".Sprintf(" + strconv.Quote(format) + ", " + strings.Join(shown, ", ") + ") }, func() error { return " + l.testFunc(t) + "(" + strings.Join(args, ", ") + ") })\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\tif " + ok + " {\n\t\t\t" + fmtPkg + ".Println(" + strconv.Quote(fmt.Sprintf("ok %s (%d cases)", t.Name, testCases)) + ")\n")
		b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n\t\t}\n\t}\n")
	}
	b.WriteString("\tif " + failed + " > 0 {\n")
	b.WriteString("\t\t" + fmtPkg + ".Printf(\"FAIL (%d of %d tests)\\n\", " + failed + ", " + total + ")\n")
	b.WriteString("\t\treturn " + fmtPkg + ".Errorf(\"%d of %d tests failed\", " + failed + ", " + total + ")\n\t}\n")
	b.WriteString("\t" + fmtPkg + ".Printf(\"PASS (%d tests)\\n\", " + total + ")\n")
	b.WriteString("\treturn nil\n}\n\n")
	return b.String()
}

// harness is the test build's fixed Go: the assert record, the case runner,
// the generator's source of randomness and the seed flag. Every package
// name it uses is a reserved helper; its locals name nothing else.
func (l *lowerer) harness() string {
	pairs := []string{}
	keys := make([]string, 0, len(testHelpers))
	for _, h := range testHelpers {
		keys = append(keys, h.key)
	}
	// Replace longer keys first, so $Failed never eats $FailedMu.
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		pairs = append(pairs, "$"+k, l.helper(k))
	}
	return strings.NewReplacer(pairs...).Replace(harnessGo)
}

const harnessGo = `// $Assertion is a failed assert, the panic that ends its case (196l).
type $Assertion struct{ at, text string }

func (a $Assertion) String() string { return "assert failed at " + a.at + ": " + a.text }

// $Failed is the running case's first failed assert. A case reads it, not
// the panic value, whichever frames and tasks the panic crossed.
var (
	$FailedMu $sync.Mutex
	$Failed   *$Assertion
)

func $AssertFail(at, text string) {
	a := $Assertion{at, text}
	$FailedMu.Lock()
	if $Failed == nil {
		$Failed = &a
	}
	$FailedMu.Unlock()
	panic(a)
}

// $Case runs one case of the test name. A failed assert, or a typed failure
// that escapes the body, fails it: it prints a FAIL line and returns false.
// Any other panic is a defect: it prints a FAIL line and is not recovered,
// so the runtime reports it with its Ergo frames and the run stops.
func $Case(name string, args func() string, body func() error) (ok bool) {
	$FailedMu.Lock()
	$Failed = nil
	$FailedMu.Unlock()
	done := false
	defer func() {
		if done {
			return
		}
		$FailedMu.Lock()
		a := $Failed
		$FailedMu.Unlock()
		if a == nil {
			$fmt.Println("FAIL " + name + $Where(args) + ": a defect, which stops the run")
			return
		}
		recover()
		$fmt.Println("FAIL " + name + $Where(args) + ": " + a.String())
		ok = false
	}()
	err := body()
	done = true
	if err != nil {
		$fmt.Println("FAIL " + name + $Where(args) + ": a failure escaped the test: " + err.Error())
		return false
	}
	return true
}

func $Where(args func() string) string {
	if args == nil {
		return ""
	}
	return " (" + args() + ")"
}

// $Rand draws a property's arguments: splitmix64, so a seed replays every
// case.
type $Rand struct{ s uint64 }

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

// i64 is a small value half the time, so a property meets zero, one and
// both signs, and any 64 bits otherwise; a narrower type wraps it.
func (r *$Rand) i64() int64 {
	if r.bool() {
		return int64(r.intn(21)) - 10
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

func (r *$Rand) str() string {
	b := make([]rune, r.intn(6))
	for i := range b {
		b[i] = $Runes[r.intn(len($Runes))]
	}
	return string(b)
}

func $GenSlice[T any](r *$Rand, d int, elem func(*$Rand, int) T) []T {
	if d <= 0 {
		return []T{}
	}
	xs := make([]T, r.intn(4))
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
	for n := r.intn(4); n > 0; n-- {
		m[key(r, d-1)] = val(r, d-1)
	}
	return m
}

// $Seed reads --seed N or --seed=N, the seed of every property; 1 by
// default.
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
		if _, err := $fmt.Sscan(v, &seed); v == "" || err != nil {
			$fmt.Fprintln($os.Stderr, "usage: [--seed N], not "+a)
			$os.Exit(2)
		}
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
