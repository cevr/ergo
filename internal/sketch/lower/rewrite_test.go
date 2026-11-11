package lower

import (
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The rewrite property (types.md, Laws 14; rewrite.go). Each generated
// program declares operation types of the rewrite-law property's model
// (rewrite_law_test.go) under fresh names, with true and false rewrite
// laws and post laws, a lawful provider and, for the first type, a second
// provider for which one law is false. Its sites call the providers in
// windows broken by opaque statements. The program is lowered twice: with
// the evidence of its check run (RewriteChecks) and with the reference
// lowering; both must print the same.

// agreeOps are the model's operations in rwType's order.
var agreeOps = []string{"Add", "Inc", "Halve", "Clear", "Read", "Even"}

var agreeOpRe = regexp.MustCompile(`\b(Add|Inc|Halve|Clear|Read|Even)\(`)

// agreeType is a generated operation type: its model, the fresh names of
// its operations, its post laws (in the model's names) and the second
// provider's name and increment, "" when it has none.
type agreeType struct {
	rwType
	ops   map[string]string
	posts []string
	other string
	od    int64
}

func (a agreeType) rename(s string) string {
	return agreeOpRe.ReplaceAllStringFunc(s, func(m string) string { return a.ops[m[:len(m)-1]] + "(" })
}

func (a agreeType) source() string {
	src := a.rwType.source()
	head, rest, _ := strings.Cut(src, "}\n\n")
	for _, p := range a.posts {
		head += "\t" + p + ",\n"
	}
	src = head + "}\n\n" + rest
	if a.other != "" {
		q := rwType{name: "X", prov: a.other, c: a.c, d: a.od}.source()
		_, provider, _ := strings.Cut(q, "}\n\n")
		src += provider
	}
	return a.rename(src)
}

// genAgreeTypes draws two types. The first has the second provider: its
// increment differs from the lawful one's in the low bit, so the unfold
// law U, true for the lawful provider, is false for it.
func genAgreeTypes(rng *rand.Rand) []agreeType {
	var out []agreeType
	for i := range 2 {
		t := rwType{name: rwName(rng, "T"), prov: rwName(rng, "P"), c: []int64{1, 3}[rng.Intn(2)], d: 1 + int64(rng.Intn(3))}
		a := agreeType{rwType: t, ops: map[string]string{}}
		for _, op := range agreeOps {
			a.ops[op] = rwName(rng, "o")
		}
		var k int64
		for k = 0; k < 4 && (k*t.c)%4 != t.d; k++ {
		}
		late := int64(2)
		add := func(ps ...string) rwOp { return rwOp{name: "Add", arg: rwExpr{params: ps}} }
		a.laws = []rwLaw{
			// False only in the high bit; it comes before the true
			// join, so a build that ignored the evidence would use it.
			{name: rwName(rng, "L"), params: []string{"a", "b"}, lhs: []rwOp{add("a"), add("b")}, rhs: []rwOp{{name: "Add", arg: rwExpr{params: []string{"a", "b"}, add: late}}}},
			{name: rwName(rng, "J"), params: []string{"a", "b"}, lhs: []rwOp{add("a"), add("b")}, rhs: []rwOp{{name: "Add", arg: rwExpr{params: []string{"a", "b"}}}}},
			{name: rwName(rng, "G"), guard: "true", lhs: []rwOp{{name: "Clear"}}},
			{name: rwName(rng, "F"), guard: "true", lhs: []rwOp{{name: "Halve"}}},
			{name: rwName(rng, "U"), lhs: []rwOp{{name: "Inc"}}, rhs: []rwOp{{name: "Add", arg: rwExpr{add: k}}}},
		}
		for len(a.laws) < 7 {
			l := randomLaw(rng, rwName(rng, "R"))
			if v, _ := t.verdict(l); v != "tested" || l.guard == "" && len(l.lhs) == 1 && (l.lhs[0].name == "Add" || l.lhs[0].name == "Halve") {
				continue // false, or an unfold of Add (U unfolds into it) or of Halve (a fixed site needs it)
			}
			a.laws = append(a.laws, l)
		}
		// A true post law, and a false one: Halve does not make every
		// state even.
		a.posts = []string{"law " + rwName(rng, "Q") + "() { Clear(); Even() == true }", "law " + rwName(rng, "Q") + "() { Halve(); Even() == true }"}
		if i == 0 {
			a.other, a.od = rwName(rng, "P"), 2
			if t.d == 2 {
				a.od = 3
			}
		}
		out = append(out, a)
	}
	return out
}

// agreeProgram writes the program: the types, a site function per
// provider, an anchor (`Add(1); Add(2)` on the first lawful provider,
// which J joins) and main, which calls each site with a few arguments.
func agreeProgram(rng *rand.Rand, types []agreeType) (src, anchor string) {
	var b strings.Builder
	b.WriteString("import sync\n\n")
	for _, a := range types {
		b.WriteString(a.source())
	}
	var sites []string
	for _, a := range types {
		provs := []string{a.prov}
		if a.other != "" {
			provs = append(provs, a.other)
		}
		for _, p := range provs {
			bump := rwName(rng, "bump")
			b.WriteString(a.rename("func " + bump + "(q " + p + ") { q.Inc() }\n\n"))
			for range 3 {
				name := rwName(rng, "site")
				sites = append(sites, name)
				var body []string
				for range 6 + rng.Intn(8) {
					switch r := rng.Intn(20); {
					case r < 6:
						arg := []string{"x", "y", strconv.Itoa(rng.Intn(6))}[rng.Intn(3)]
						body = append(body, "c.Add("+arg+")")
					case r < 9:
						body = append(body, "c.Inc()")
					case r < 12:
						body = append(body, "c.Clear()")
					case r < 15:
						body = append(body, "c.Halve()")
					case r < 17:
						body = append(body, "Console.Println(\"read \\(c.Read())\")")
					case r < 18:
						body = append(body, bump+"(c)")
					default:
						body = append(body, "if x > y {\n\t\tc.Halve()\n\t}")
					}
				}
				b.WriteString(a.rename("func " + name + "(x int, y int) needs Console {\n\tlet c = New" + p + "()\n\t" + strings.Join(body, "\n\t") + "\n\tConsole.Println(\"" + name + " \\(c.Read()) \\(c.Even())\")\n}\n\n"))
			}
		}
	}
	// Two fixed sites per type: a Clear after a Halve, which no passing
	// post law makes even (the false post law on Halve says it does), and
	// a Clear after an Add, which no post law speaks of, so the first
	// Clear's fact is gone.
	var fixed []string
	for _, a := range types {
		for _, mid := range []string{"c.Halve()", "c.Add(1)"} {
			name := rwName(rng, "fixed")
			fixed = append(fixed, name)
			b.WriteString(a.rename("func " + name + "(x int, y int) needs Console {\n\tlet c = New" + a.prov + "()\n\tc.Add(x)\n\tc.Clear()\n\t" + mid + "\n\tc.Clear()\n\tConsole.Println(\"" + name + " \\(c.Read()) \\(c.Even())\")\n}\n\n"))
		}
	}
	anchor = rwName(rng, "anchor")
	b.WriteString(types[0].rename("func " + anchor + "() needs Console {\n\tlet c = New" + types[0].prov + "()\n\tc.Add(1)\n\tc.Add(2)\n\tConsole.Println(\"anchor \\(c.Read())\")\n}\n\n"))
	b.WriteString("func main() needs Console {\n\t" + anchor + "()\n")
	for _, s := range sites {
		for range 3 {
			b.WriteString("\t" + s + "(" + strconv.Itoa(rng.Intn(5)) + ", " + strconv.Itoa(rng.Intn(5)) + ")\n")
		}
	}
	// Each fixed site at every state Add(x) reaches (C is odd).
	for _, s := range fixed {
		for x := range 4 {
			b.WriteString("\t" + s + "(" + strconv.Itoa(x) + ", 0)\n")
		}
	}
	b.WriteString("}\n")
	return b.String(), anchor
}

// TestRewriteAgrees: the build's evidence admits proved laws only, so on
// these tested runs the anchor keeps its two Adds (owner q115). Then, with
// the tested runs admitted explicitly (a test-only admission: the engine
// under test, not the build's rule), a program lowered with its check
// run's evidence prints what the reference lowering prints, and at the
// anchor, a site of constants on a known provider whose join law the check
// run passed, the Go calls the right side's one Add and no left-side Add
// pair.
// ERGO_REWRITE_AGREE_SEED replays a seed.
//
// It builds Go because the rewritten and the reference programs must run:
// their providers keep state in a sync.Ref, a Go body that check-time
// evaluation does not run, and the evidence is the built check run.
func TestRewriteAgrees(t *testing.T) {
	needsBuild(t)
	seed := int64(1)
	if s := os.Getenv("ERGO_REWRITE_AGREE_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	rng := rand.New(rand.NewSource(seed))
	types := genAgreeTypes(rng)
	src, anchor := agreeProgram(rng, types)
	replay := "replay: ERGO_REWRITE_AGREE_SEED=" + strconv.FormatInt(seed, 10) + " go test -run TestRewriteAgrees ./internal/sketch/lower"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("seed %d: %v\n%s", seed, err, src)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("seed %d: %v\n%s", seed, diags, src)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	var checkOut string
	ev, err := RewriteChecks(u, func(files []File, laws []string) (string, error) {
		exes := buildAll(t, []*lowered{{name: "check", src: src, file: file, pkg: pkg, files: files}}, true)
		var out strings.Builder
		for _, law := range laws {
			stdout, _, _ := runExeArgs(t, exes["check"], "--seed", RewriteCheckSeed, "--law", law)
			out.WriteString(stdout)
		}
		checkOut = out.String()
		return checkOut, nil
	})
	if err != nil {
		t.Fatalf("seed %d: %v\n%s", seed, err, src)
	}
	// The build's evidence admits proved laws only (owner q115), and these
	// runs are tested: the build keeps the reference form at the anchor.
	add := types[0].ops["Add"]
	rewritten := u
	rewritten.Rewrite = ev
	built, err := Lower(rewritten)
	if err != nil {
		t.Fatalf("seed %d: %v\n%s", seed, err, src)
	}
	if got := strings.Count(goFunc(t, (&lowered{files: built}).mainGo(), anchor), add); got != 2 {
		t.Fatalf("seed %d: the build's anchor calls %s %d times on tested evidence, want 2\ncheck run:\n%s\n%s", seed, add, got, checkOut, replay)
	}
	// The property tests the engine: it admits the tested runs explicitly,
	// which no build does.
	ev.(*checkedEvidence).admitTested = true
	fast, err := Lower(rewritten)
	if err != nil {
		t.Fatalf("seed %d: %v\n%s", seed, err, src)
	}
	rewriteLowering = "reference"
	ref, err := Lower(rewritten)
	rewriteLowering = ""
	if err != nil {
		t.Fatalf("seed %d: %v\n%s", seed, err, src)
	}
	progs := []*lowered{{name: "fast", src: src, file: file, pkg: pkg, files: fast}, {name: "ref", src: src, file: file, pkg: pkg, files: ref}}
	// The promised form at the anchor: one Add, of the constant 3.
	for _, p := range progs {
		body := goFunc(t, p.mainGo(), anchor)
		want := 2
		if p.name == "fast" {
			want = 1
		}
		if got := strings.Count(body, add); got != want || want == 1 && !strings.Contains(body, "int(3)") {
			t.Fatalf("seed %d: the %s anchor calls %s %d times, want %d:\n%s\ncheck run:\n%s\n%s", seed, p.name, add, got, want, body, checkOut, replay)
		}
	}
	exes := buildAll(t, progs, true)
	if exes == nil {
		return
	}
	fo, fe, fcode := runExe(t, exes["fast"])
	ro, re, rcode := runExe(t, exes["ref"])
	if fo != ro || fcode != rcode {
		t.Fatalf("seed %d: the rewritten program prints\n%s(exit %d, %s)\nthe reference\n%s(exit %d, %s)\nsource:\n%s\ncheck run:\n%s\n%s", seed, fo, fcode, firstLines(fe, 8), ro, rcode, firstLines(re, 8), src, checkOut, replay)
	}
}

// admitAll admits every law: the engine's own behaviour, apart from the
// evidence.
type admitAll struct{}

func (admitAll) Admit(*check.Law, *check.Named, RewriteInstance) (bool, string) { return true, "" }

// rwdeepSource is the v1 design audit's probe rwdeep (v1-audit-2 D1) with
// its site on the provider's own value, so the lowering knows the
// provider: the law TwoIncsAreTwice is false only at state 9, nine
// operations from a fresh Box.
const rwdeepSource = `import sync

type Counter {
	Inc(),
	Twice(),
	Read() int,

	law TwoIncsAreTwice() { Inc(); Inc() } == { Twice() },
}

type Box { s sync.Ref[int] }

func NewBox() Box { Box{s: sync.NewRef[int](0)} }

func Inc(p Box) { p.s.Set(p.s.Get() + 1) }

func Twice(p Box) {
	let v = p.s.Get()
	if v == 9 {
		p.s.Set(0)
	} else {
		p.s.Set(v + 2)
	}
}

func Read(p Box) int { p.s.Get() }

func use() int {
	let b = NewBox()
	for _ in 0..9 {
		b.Inc()
	}
	b.Inc()
	b.Inc()
	b.Read()
}

func main() needs Console {
	Console.Println("after 11 increments: \(use())")
}
`

// TestRewriteDeepFalseLaw: a rewrite law false only at a state nine
// operations deep is refuted by the build's check run, and the site keeps
// its two Incs; with every law admitted, the same site calls Twice, so
// the window is one the engine would rewrite.
//
// It builds Go because the evidence is the built check run: the
// provider keeps its state in a sync.Ref, a Go body that check-time
// evaluation does not run.
func TestRewriteDeepFalseLaw(t *testing.T) {
	needsBuild(t)
	file, err := syntax.Parse(rwdeepSource)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v", diags)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: rwdeepSource}}}
	var checkOut string
	ev, err := RewriteChecks(u, func(files []File, laws []string) (string, error) {
		exes := buildAll(t, []*lowered{{name: "check", src: rwdeepSource, file: file, pkg: pkg, files: files}}, true)
		var out strings.Builder
		for _, law := range laws {
			stdout, _, _ := runExeArgs(t, exes["check"], "--seed", RewriteCheckSeed, "--law", law)
			out.WriteString(stdout)
		}
		checkOut = out.String()
		return checkOut, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Even an engine test's admission of tested runs admits no refuted law.
	ev.(*checkedEvidence).admitTested = true
	for _, c := range []struct {
		ev    RewriteEvidence
		twice bool
	}{{ev, false}, {admitAll{}, true}} {
		u.Rewrite = c.ev
		files, err := Lower(u)
		if err != nil {
			t.Fatal(err)
		}
		body := goFunc(t, (&lowered{files: files}).mainGo(), "use")
		if got := strings.Contains(body, "Twice("); got != c.twice {
			t.Fatalf("calls Twice: %v, want %v:\n%s\ncheck run:\n%s", got, c.twice, body, checkOut)
		}
	}
	if !strings.Contains(checkOut, "FAIL law Counter.TwoIncsAreTwice for Box") {
		t.Fatalf("the check run did not refute the law:\n%s", checkOut)
	}
}

// TestRewriteEmptyWindow is the smallest repro of a fixed bug: a window
// whose rewrite leaves no call left its receiver's and arguments'
// temporaries unused, and Go refused the program ("declared and not
// used").
func TestRewriteEmptyWindow(t *testing.T) {
	src := "import sync\n\ntype T {\n\tTouch(n int),\n\tRead() int,\n\n\tlaw Idle(n int) { Touch(n) } == { },\n}\n\n" +
		"type P { s sync.Ref[int] }\n\nfunc NewP() P { P{s: sync.NewRef[int](0)} }\n\n" +
		"func Touch(p P, n int) { }\n\nfunc Read(p P) int { p.s.Get() }\n\n" +
		"func site(x int) needs Console {\n\tlet c = NewP()\n\tc.Touch(x)\n\tConsole.Println(\"\\(c.Read())\")\n}\n\nfunc main() needs Console { site(1) }\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v\n%s", diags, src)
	}
	files, err := Lower(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}, Rewrite: admitAll{}})
	if err != nil {
		t.Fatal(err)
	}
	p := &lowered{name: "empty", src: src, file: file, pkg: pkg, files: files}
	if body := goFunc(t, p.mainGo(), "site"); strings.Contains(body, "Touch(") {
		t.Fatalf("the window was not rewritten:\n%s", body)
	}
	typecheckGo(t, []*lowered{p})
}

// TestRewriteWorkBound: the engine's work on a window that never settles
// (two laws that swap commuting calls back and forth) stops one unit past
// rewriteWorkPerCall per call, and the window keeps the reference
// lowering.
func TestRewriteWorkBound(t *testing.T) {
	for n := 2; n <= 4; n++ {
		calls := []string{"c.Inc()"}
		for range n - 1 {
			calls = append(calls, "c.Add(x)")
		}
		src := "import sync\n\ntype T {\n\tAdd(n int),\n\tInc(),\n\tRead() int,\n\n" +
			"\tlaw Swap(a int) { Inc(); Add(a) } == { Add(a); Inc() },\n" +
			"\tlaw Back(a int) { Add(a); Inc() } == { Inc(); Add(a) },\n}\n\n" +
			"type P { s sync.Ref[int] }\n\nfunc NewP() P { P{s: sync.NewRef[int](0)} }\n\n" +
			"func Add(p P, n int) { p.s.Set(p.s.Get() + n) }\n\nfunc Inc(p P) { p.s.Set(p.s.Get() + 1) }\n\nfunc Read(p P) int { p.s.Get() }\n\n" +
			"func site(x int) needs Console {\n\tlet c = NewP()\n\t" + strings.Join(calls, "\n\t") + "\n\tConsole.Println(\"\\(c.Read())\")\n}\n\nfunc main() needs Console { site(1) }\n"
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		pkg, diags := check.Check("main", []*syntax.File{file}, nil)
		if len(diags) > 0 {
			t.Fatalf("%v\n%s", diags, src)
		}
		u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}, Rewrite: admitAll{}}
		l := newBuildLowerer(u, nil)
		if _, err := l.program(); err != nil {
			t.Fatal(err)
		}
		if want := rewriteWorkPerCall*n + 1; l.rewrite.work != want || l.rewrite.refused != 1 || l.rewrite.applied != 0 {
			t.Fatalf("n = %d: work %d, refused %d, applied %d; want work %d, one window refused", n, l.rewrite.work, l.rewrite.refused, l.rewrite.applied, want)
		}
	}
}
