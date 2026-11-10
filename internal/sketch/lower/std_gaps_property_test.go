package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStdGapProperty is the generated guard of the std lowering gaps
// (lower-gaps, batch 11). Each seed makes a std fixture package, checked
// as std is (check.StdFixture): a record, an operation type of one to
// three operations of four kinds (an int result, a string result with a
// parameter, a bool result, a fallible operation), a function that
// converts the record into the operation type in one of five forms, and
// an exported named context that binds the operation type in one of three
// forms. A program of one to three files calls the function and uses the
// context in a with, in one of four forms, and prints each operation's
// result. Each program must lower to Go that go/parser parses (buildAll),
// that go vet accepts and that runs with the output the generator
// computes. ERGO_STD_GAP_SEED replays one seed; a failing seed prints the
// Ergo sources and the Go.
//
// With the default seeds, every branch of the generator is reached
// (gapBranches), so a mutant of either fix is caught by some program.
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestStdGapProperty(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{}
	replay := os.Getenv("ERGO_STD_GAP_SEED")
	if replay != "" {
		s, err := strconv.ParseUint(replay, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_STD_GAP_SEED=%q: %v", replay, err)
		}
		seeds = append(seeds, s)
	} else {
		for s := uint64(1); s <= 24; s++ {
			seeds = append(seeds, s)
		}
	}
	reached := map[string]bool{}
	var programs []*lowered
	want := map[string]string{}
	for _, seed := range seeds {
		g := genStdGap(seed)
		for _, b := range g.branches {
			reached[b] = true
		}
		out := g.lower(t)
		if out == nil {
			continue
		}
		programs = append(programs, out)
		want[out.name] = g.want
	}
	if replay == "" {
		for _, b := range gapBranches {
			if !reached[b] {
				t.Errorf("no default seed reaches the generator branch %s; add seeds", b)
			}
		}
	}
	if t.Failed() {
		return
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	for _, p := range programs {
		stdout, stderr, exit := runExe(t, exes[p.name])
		if stdout != want[p.name] || exit != 0 {
			t.Errorf("%s: exit %d, stdout %q, want %q\nstderr %s\nreplay: ERGO_STD_GAP_SEED=%s\nsource:\n%s\nGo:\n%s",
				p.name, exit, stdout, want[p.name], firstLines(stderr, 12), strings.TrimPrefix(p.name, "gap"), p.src, p.mainGo())
		}
	}
}

// gapBranches are the generator's branches, which the default seeds reach.
var gapBranches = []string{
	"op int", "op string", "op bool", "op fallible",
	"make tail", "make return", "make let", "make branch", "make param",
	"ctx direct", "ctx embedded", "ctx provider-call",
	"use std", "use user-embeds", "use inline-embeds", "use with-value",
	"files 1", "files 2", "files 3",
}

// stdGap is one generated case: its std fixture, its program's files (the
// root's, and lib's when it has three) and its expected output.
type stdGap struct {
	seed     uint64
	pkg      string // the std fixture's path
	fixture  string
	root     []pkgSource
	lib      []pkgSource
	want     string
	branches []string
}

// gapOp is one operation of the generated operation type.
type gapOp struct {
	kind string
	i    int
	c    int // the int kind's addend
	x    int // the argument of the other kinds
}

func (o gapOp) decl() string {
	switch o.kind {
	case "int":
		return fmt.Sprintf("Get%d() int", o.i)
	case "string":
		return fmt.Sprintf("Name%d(x int) string", o.i)
	case "bool":
		return fmt.Sprintf("Less%d(x int) bool", o.i)
	default:
		return fmt.Sprintf("Half%d(x int) int fails Odd", o.i)
	}
}

func (o gapOp) fn() string {
	switch o.kind {
	case "int":
		return fmt.Sprintf("func Get%d(b box) int { b.n + %d }\n", o.i, o.c)
	case "string":
		return fmt.Sprintf("func Name%d(b box, x int) string { \"\\(b.tag)-\\(x)\" }\n", o.i)
	case "bool":
		return fmt.Sprintf("func Less%d(b box, x int) bool { x < b.n }\n", o.i)
	default:
		return fmt.Sprintf("func Half%d(b box, x int) int fails Odd {\n\tif x %% 2 == 1 {\n\t\treturn Odd{N: x}\n\t}\n\tx / 2 + b.n\n}\n", o.i)
	}
}

// show is the body of a helper that reads the operation through recv, a
// value (r) or the capability (pkg.Ops), and gives a line of text.
func (o gapOp) show(recv, pkg string) string {
	switch o.kind {
	case "int":
		return fmt.Sprintf("\t\"get%d \\(%s.Get%d())\"\n", o.i, recv, o.i)
	case "string":
		return fmt.Sprintf("\t\"name%d \" + %s.Name%d(%d)\n", o.i, recv, o.i, o.x)
	case "bool":
		return fmt.Sprintf("\tif %s.Less%d(%d) {\n\t\treturn \"less%d yes\"\n\t}\n\t\"less%d no\"\n", recv, o.i, o.x, o.i, o.i)
	default:
		return fmt.Sprintf("\tlet h = %s.Half%d(%d) catch {\n\tcase %s.Odd as o:\n\t\treturn \"odd \\(o.N)\"\n\t}\n\t\"half \\(h)\"\n", recv, o.i, o.x, pkg)
	}
}

// result is the line show gives for a box of n and tag.
func (o gapOp) result(n int, tag string) string {
	switch o.kind {
	case "int":
		return fmt.Sprintf("get%d %d", o.i, n+o.c)
	case "string":
		return fmt.Sprintf("name%d %s-%d", o.i, tag, o.x)
	case "bool":
		if o.x < n {
			return fmt.Sprintf("less%d yes", o.i)
		}
		return fmt.Sprintf("less%d no", o.i)
	default:
		if o.x%2 == 1 {
			return fmt.Sprintf("odd %d", o.x)
		}
		return fmt.Sprintf("half %d", o.x/2+n)
	}
}

func genStdGap(seed uint64) *stdGap {
	r := rand.New(rand.NewPCG(seed, 0x5eed))
	g := &stdGap{seed: seed, pkg: "gapstd" + strconv.FormatUint(seed, 10)}
	branch := func(name string) { g.branches = append(g.branches, name) }
	kinds := []string{"int", "string", "bool", "fallible"}
	var ops []gapOp
	for i := range 1 + r.IntN(3) {
		o := gapOp{kind: kinds[r.IntN(len(kinds))], i: i + 1, c: r.IntN(10), x: r.IntN(40)}
		branch("op " + o.kind)
		ops = append(ops, o)
	}
	var f strings.Builder
	f.WriteString("type Odd { N int }\n\ntype box { n int, tag string }\n\ntype Ops {\n")
	for _, o := range ops {
		f.WriteString("\t" + o.decl() + ",\n")
	}
	f.WriteString("}\n\n")
	for _, o := range ops {
		f.WriteString(o.fn() + "\n")
	}
	// The function that converts the record into Ops, and its call.
	n, tag := 1+r.IntN(30), "t"+strconv.Itoa(r.IntN(100))
	lit := fmt.Sprintf("box{n: %d, tag: %q}", n, tag)
	call := g.pkg + ".Make()"
	switch form := []string{"tail", "return", "let", "branch", "param"}[r.IntN(5)]; form {
	case "tail":
		f.WriteString("func Make() Ops { " + lit + " }\n\n")
		branch("make tail")
	case "return":
		f.WriteString("func Make() Ops {\n\treturn " + lit + "\n}\n\n")
		branch("make return")
	case "let":
		f.WriteString("func Make() Ops {\n\tlet b = " + lit + "\n\tb\n}\n\n")
		branch("make let")
	case "branch":
		f.WriteString(fmt.Sprintf("func Make() Ops {\n\tif %d > 15 {\n\t\treturn %s\n\t}\n\t%s\n}\n\n", n, lit, lit))
		branch("make branch")
	case "param":
		f.WriteString(fmt.Sprintf("func Make(n int) Ops { box{n: n, tag: %q} }\n\n", tag))
		call = fmt.Sprintf("%s.Make(%d)", g.pkg, n)
		branch("make param")
	}
	// The exported named context.
	cn, ctag := 1+r.IntN(30), "c"+strconv.Itoa(r.IntN(100))
	clit := fmt.Sprintf("box{n: %d, tag: %q}", cn, ctag)
	switch form := []string{"direct", "embedded", "provider-call"}[r.IntN(3)]; form {
	case "direct":
		f.WriteString("let Ctx = context { Ops: " + clit + " }\n")
		branch("ctx direct")
	case "embedded":
		f.WriteString("let inner = context { Ops: " + clit + " }\n\nlet Ctx = context { inner }\n")
		branch("ctx embedded")
	case "provider-call":
		// The provider calls Make, whose body converts the record: the
		// context's entry holds the operation type already.
		if strings.HasSuffix(call, ".Make()") {
			f.WriteString("let Ctx = context { Ops: Make() }\n")
		} else {
			f.WriteString(fmt.Sprintf("let Ctx = context { Ops: Make(%d) }\n", n))
		}
		cn, ctag = n, tag
		branch("ctx provider-call")
	}
	g.fixture = f.String()

	// The program. Show<i> reads a value, CtxShow<i> the capability.
	files := 1 + r.IntN(3)
	branch("files " + strconv.Itoa(files))
	q := "" // the helpers' qualifier in main
	if files == 3 {
		q = "lib."
	}
	var shows, ctxShows strings.Builder
	for _, o := range ops {
		shows.WriteString(fmt.Sprintf("func Show%d(r %s.Ops) string {\n%s}\n\n", o.i, g.pkg, o.show("r", g.pkg)))
		ctxShows.WriteString(fmt.Sprintf("func CtxShow%d() string needs %s.Ops {\n%s}\n\n", o.i, g.pkg, o.show(g.pkg+".Ops", g.pkg)))
	}
	var want strings.Builder
	var body strings.Builder
	body.WriteString("\tlet r = " + call + "\n")
	for _, o := range ops {
		body.WriteString(fmt.Sprintf("\tConsole.Println(\"value \" + %sShow%d(r))\n", q, o.i))
		want.WriteString("value " + o.result(n, tag) + "\n")
	}
	var ctxLines []string
	for _, o := range ops {
		ctxLines = append(ctxLines, fmt.Sprintf("CtxShow%d()", o.i))
		want.WriteString("context " + o.result(cn, ctag) + "\n")
	}
	// How the program uses the context.
	extra := "" // root declarations beside main
	viaValue := ""
	use := []string{"std", "user-embeds", "inline-embeds", "with-value"}[r.IntN(4)]
	branch("use " + use)
	operand := g.pkg + ".Ctx"
	switch use {
	case "user-embeds":
		extra = "let Mine = context { " + g.pkg + ".Ctx }\n\n"
		operand = "Mine"
	case "inline-embeds":
		operand = "context { " + g.pkg + ".Ctx }"
	case "with-value":
		// A with that yields a value, in a helper function.
		viaValue = fmt.Sprintf("func ViaCtx() []string {\n\twith %s.Ctx {\n\t\t[]string{%s}\n\t}\n}\n\n", g.pkg, strings.Join(ctxLines, ", "))
	}
	if use == "with-value" {
		body.WriteString(fmt.Sprintf("\tfor line in %sViaCtx() {\n\t\tConsole.Println(\"context \" + line)\n\t}\n", q))
	} else {
		body.WriteString("\twith " + operand + " {\n")
		for _, l := range ctxLines {
			body.WriteString("\t\tConsole.Println(\"context \" + " + q + l + ")\n")
		}
		body.WriteString("\t}\n")
	}
	g.want = want.String()
	imports := "import " + g.pkg + "\n\n"
	main := "func main() needs Console {\n" + body.String() + "}\n"
	helpers := shows.String() + ctxShows.String() + viaValue
	switch files {
	case 1:
		g.root = []pkgSource{{"/ergo-test/gap/main.ergo", imports + extra + helpers + main}}
	case 2:
		g.root = []pkgSource{
			{"/ergo-test/gap/main.ergo", imports + extra + main},
			{"/ergo-test/gap/show.ergo", imports + strings.TrimSuffix(helpers, "\n")},
		}
	case 3:
		g.root = []pkgSource{{"/ergo-test/gap/main.ergo", imports + "import lib\n\n" + extra + main}}
		g.lib = []pkgSource{
			{"/ergo-test/gap/lib/show.ergo", imports + strings.TrimSuffix(shows.String(), "\n")},
			{"/ergo-test/gap/lib/context.ergo", imports + strings.TrimSuffix(ctxShows.String()+viaValue, "\n")},
		}
	}
	return g
}

// source is the case's Ergo text, every file under its path.
func (g *stdGap) source() string {
	var b strings.Builder
	fmt.Fprintf(&b, "// seed %d\n// std/%s/fixture.ergo\n%s", g.seed, g.pkg, g.fixture)
	for _, s := range append(slices.Clone(g.lib), g.root...) {
		b.WriteString("// " + s.path + "\n" + s.text)
	}
	return b.String()
}

// lower checks and lowers the case; a check or lowering failure fails the
// test with the case's source and returns nil.
func (g *stdGap) lower(t *testing.T) *lowered {
	t.Helper()
	fail := func(format string, args ...any) *lowered {
		t.Errorf("seed %d (replay: ERGO_STD_GAP_SEED=%d): %s\nsource:\n%s", g.seed, g.seed, fmt.Sprintf(format, args...), g.source())
		return nil
	}
	f, err := syntax.Parse(g.fixture)
	if err != nil {
		return fail("the fixture does not parse: %v", err)
	}
	f.Name = "fixture.ergo"
	stdPkg, ds := check.StdFixture(g.pkg, []*syntax.File{f})
	if len(ds) != 0 {
		return fail("the fixture has diagnostics: %v", ds)
	}
	imp := pkgImporter{g.pkg: stdPkg}
	parse := func(path string, srcs []pkgSource, imp pkgImporter) (Unit, error) {
		u := Unit{}
		for _, s := range srcs {
			file, err := syntax.Parse(s.text)
			if err != nil {
				return u, fmt.Errorf("%s: %v", s.path, err)
			}
			file.Name = s.path
			u.Files = append(u.Files, file)
			u.Sources = append(u.Sources, Source{Path: s.path, Text: s.text})
		}
		pkg, ds := check.Check(path, u.Files, imp)
		if len(ds) != 0 {
			return u, fmt.Errorf("package %s: diagnostics %v", path, ds)
		}
		u.Package = pkg
		return u, nil
	}
	var imports []Unit
	if len(g.lib) > 0 {
		lib, err := parse("lib", g.lib, imp)
		if err != nil {
			return fail("%v", err)
		}
		imp["lib"] = lib.Package
		imports = append(imports, lib)
	}
	root, err := parse("main", g.root, imp)
	if err != nil {
		return fail("%v", err)
	}
	root.Imports = imports
	out := lowerUnit("gap"+strconv.FormatUint(g.seed, 10), root, false)
	out.src = g.source()
	switch {
	case out.panicked != nil:
		return fail("the lowering panicked: %v", out.panicked)
	case len(out.refused) > 0:
		return fail("the lowering refused: %+v", out.refused)
	case out.err != nil:
		return fail("the lowering failed: %v", out.err)
	}
	return out
}

// TestStdGapRealStd drives the same two shapes over the real std: every
// exported std function whose body converts a record into its operation
// type result, and every exported std named context, gets a program that
// calls it or uses it in a with, which must lower, vet and run. None
// exists today (lowering-parse-gap.md); one that this generator cannot
// drive (a function with parameters or needs, a context with needs) fails
// here, so the generator is extended with it.
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestStdGapRealStd(t *testing.T) {
	var programs []*lowered
	want := map[string]string{}
	add := func(name, src, out string) {
		o := lowerSource(name, src, true)
		switch {
		case len(o.diags) > 0:
			t.Errorf("%s: checker diagnostics %v; extend TestStdGapRealStd for it\n%s", name, o.diags, src)
		case o.panicked != nil || o.err != nil || len(o.refused) > 0:
			t.Errorf("%s: lowering: panic %v, error %v, refused %+v\n%s", name, o.panicked, o.err, o.refused, src)
		default:
			programs = append(programs, o)
			want[name] = out
		}
	}
	for _, path := range std.Packages() {
		p, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("std %s: %v", path, err)
		}
		for _, f := range stdConvertingFuncs(p) {
			if len(f.Sig.Params) > 0 || len(rowElems(f.Sig.Uses)) > 0 {
				t.Errorf("std %s.%s converts a record into its operation type result and takes parameters or needs capabilities; extend TestStdGapRealStd to call it", path, f.Name)
				continue
			}
			name := "realfn_" + strings.ReplaceAll(path, "/", "_") + "_" + f.Name
			add(name, fmt.Sprintf("import %s\n\nfunc keep[T](_ T) {}\n\nfunc main() needs Console {\n\tkeep(%s.%s())\n\tConsole.Println(\"made\")\n}\n", path, path, f.Name), "made\n")
		}
		for _, cname := range slices.Sorted(func(yield func(string) bool) {
			for n := range p.Contexts {
				if !yield(n) {
					return
				}
			}
		}) {
			w := p.Contexts[cname]
			if w.Decl == nil || !exportedName(cname) {
				continue
			}
			name := "realctx_" + strings.ReplaceAll(path, "/", "_") + "_" + cname
			add(name, fmt.Sprintf("import %s\n\nfunc main() needs Console {\n\twith %s.%s {\n\t\tConsole.Println(\"ok\")\n\t}\n}\n", path, path, cname), "ok\n")
		}
	}
	if len(programs) == 0 {
		return
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	for _, p := range programs {
		stdout, stderr, exit := runExe(t, exes[p.name])
		if stdout != want[p.name] || exit != 0 {
			t.Errorf("%s: exit %d, stdout %q, want %q\nstderr %s\nsource:\n%s\nGo:\n%s", p.name, exit, stdout, want[p.name], firstLines(stderr, 12), p.src, p.mainGo())
		}
	}
}

// stdConvertingFuncs are the exported functions of the std package p whose
// one result is an operation type and whose body converts a value into
// it (an operations conversion, Info.Implicit, inside the body).
func stdConvertingFuncs(p *check.Package) []*check.Func {
	var out []*check.Func
	for _, f := range p.Funcs {
		if f.Decl == nil || f.Decl.Body == nil || !exportedName(f.Name) || len(f.Sig.Results) != 1 {
			continue
		}
		var file *syntax.File
		for _, pf := range p.Files() {
			if slices.Contains(pf.Decls, syntax.Decl(f.Decl)) {
				file = pf
			}
		}
		if file == nil {
			continue
		}
		for x, c := range p.Info.Implicit {
			if c.Kind != check.ConvOperations || c.To.String() != f.Sig.Results[0].String() {
				continue
			}
			if x.Pos() >= f.Decl.Body.Pos() && x.End() <= f.Decl.Body.End() && inFileOf(file, x) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// exportedName reports an Ergo name that other packages can name.
func exportedName(name string) bool {
	return name != "" && unicode.IsUpper([]rune(name)[0])
}

// inFileOf reports whether the node x is in file f.
func inFileOf(f *syntax.File, x syntax.Node) bool {
	found := false
	syntax.Inspect(f, func(n syntax.Node) bool {
		if n == x {
			found = true
		}
		return !found
	})
	return found
}
