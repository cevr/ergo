package lower

import (
	"fmt"
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

func (a agreeType) source(values bool) string {
	if values {
		return a.valueSource()
	}
	src := a.rwType.source()
	head, rest, _ := strings.Cut(src, "}\n\n")
	for _, p := range a.posts {
		head += "\t" + p + ",\n"
	}
	src = head + "}\n\n" + rest
	if a.other != "" {
		q := rwType{name: "X", prov: a.other, c: a.c, d: a.od}.source()
		_, provider, _ := strings.Cut(q, "}\n\n")
		src += strings.Replace(provider, "} implements X\n", "} implements "+a.name+"\n", 1)
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
func agreeProgram(rng *rand.Rand, types []agreeType, values bool) (src, anchor string) {
	var b strings.Builder
	if values {
		b.WriteString(`type Digit int where 0 <= it && it < 4
func digit(n int) Digit {
	match n & 3 { case 0: 0; case 1: 1; case 2: 2; case _: 3 }
}
`)
	} else {
		b.WriteString("import sync\n\n")
	}
	for _, a := range types {
		b.WriteString(a.source(values))
	}
	var sites []string
	for _, a := range types {
		provs := []string{a.prov}
		if a.other != "" {
			provs = append(provs, a.other)
		}
		for _, p := range provs {
			bump := rwName(rng, "bump")
			borrow := ""
			if values {
				borrow = "*"
			}
			b.WriteString(a.rename("func " + bump + "(q " + borrow + p + ") { q.Inc() }\n\n"))
			for range 3 {
				name := rwName(rng, "site")
				sites = append(sites, name)
				var body []string
				for range 6 + rng.Intn(8) {
					switch r := rng.Intn(20); {
					case r < 6:
						arg := []string{"x", "y", strconv.Itoa(rng.Intn(4))}[rng.Intn(3)]
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
						arg := "c"
						if values {
							arg = "&c"
						}
						body = append(body, bump+"("+arg+")")
					default:
						body = append(body, "if x > y {\n\t\tc.Halve()\n\t}")
					}
				}
				build := "let c = New" + p + "()"
				if values && p == types[len(types)-1].prov {
					// v1 audit 3, G1: the last type's provider is built
					// outside its constructor too, so its construction is
					// open and a law proved only from New's states must
					// not rewrite these sites.
					switch rng.Intn(3) {
					case 0:
						build = "let mut c = " + p + "{s: x, k: y}"
					case 1:
						build = "let mut c = " + p + "{..New" + p + "(), k: y}"
					default:
						build = "let mut c = New" + p + "()\n\tc.k = y"
					}
				}
				b.WriteString(a.rename("func " + name + "(x int, y int) needs Console {\n\t" + build + "\n\t" + strings.Join(body, "\n\t") + "\n\tConsole.Println(\"" + name + " \\(c.Read()) \\(c.Even())\")\n}\n\n"))
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
			b.WriteString("\t" + s + "(" + strconv.Itoa(rng.Intn(4)) + ", " + strconv.Itoa(rng.Intn(4)) + ")\n")
		}
	}
	// Each fixed site at every state Add(x) reaches (C is odd).
	for _, s := range fixed {
		for x := range 4 {
			b.WriteString("\t" + s + "(" + strconv.Itoa(x) + ", 0)\n")
		}
	}
	b.WriteString("}\n")
	text := b.String()
	if values {
		text = strings.ReplaceAll(text, "(x int, y int)", "(x Digit, y Digit)")
		text = strings.ReplaceAll(text, "let c = New", "let mut c = New")
	}
	return text, anchor
}

// valueSource gives the same two-bit reference machine its own value
// state and listable parameters. Computed Add arguments reduce modulo four,
// exactly as Add itself does in the handle-backed reference provider.
func (a agreeType) valueSource() string {
	var b strings.Builder
	fmt.Fprintf(&b, "type %s { Add(n Digit), Inc(), Halve(), Clear(), Read() int, Even() bool,\n", a.name)
	for _, law := range a.laws {
		text := law.text()
		head := text[:strings.LastIndex(text, " == ")]
		head = strings.ReplaceAll(head, " int", " Digit")
		var right []string
		for _, op := range law.rhs {
			if op.name != "Add" {
				right = append(right, op.text())
				continue
			}
			arg := op.arg.text()
			bare := len(op.arg.params) == 1 && op.arg.add == 0 && op.arg.spot == ""
			if !bare {
				for _, p := range op.arg.params {
					arg = strings.ReplaceAll(arg, p, "int("+p+")")
				}
				arg = "digit(" + arg + ")"
			}
			right = append(right, "Add("+arg+")")
		}
		fmt.Fprintf(&b, "%s == { %s },\n", head, strings.Join(right, "; "))
	}
	for _, post := range a.posts {
		b.WriteString(post + ",\n")
	}
	b.WriteString("}\n")
	for _, provider := range []struct {
		name  string
		delta int64
	}{{a.prov, a.d}, {a.other, a.od}} {
		if provider.name == "" {
			continue
		}
		p := provider.name
		// k is 0 in every state New and the operations reach; a value
		// built outside them (agreeProgram's open sites) may hold another.
		fmt.Fprintf(&b, "type %s { s Digit, k Digit } implements %s\nfunc New%s() %s { %s{s: 0, k: 0} }\n", p, a.name, p, p, p)
		fmt.Fprintf(&b, "func Add(p *%s, n Digit) { p.s = digit(int(p.s) + int(n)*%d + int(p.k)) }\n", p, a.c)
		fmt.Fprintf(&b, "func Inc(p *%s) { p.s = digit(int(p.s) + %d) }\n", p, provider.delta)
		fmt.Fprintf(&b, "func Halve(p *%s) { p.s = digit(int(p.s)/2) }\n", p)
		fmt.Fprintf(&b, "func Clear(p *%s) { p.s = digit(int(p.s)&^1) }\n", p)
		fmt.Fprintf(&b, "func Read(p %s) int { int(p.s)&1 }\nfunc Even(p %s) bool { int(p.s)&1 == 0 }\n", p, p)
	}
	return a.rename(b.String())
}

// TestRewriteAgrees: the build's evidence admits proved laws only, so on
// these tested runs the anchor keeps its two Adds (owner q115). Then, with
// the tested runs admitted explicitly (a test-only admission: the engine
// under test, not the build's rule), a program lowered with its check
// run's evidence prints what the reference lowering prints, and at the
// anchor, a site of constants on a known provider whose join law the check
// run passed, the Go calls the right side's one Add and no left-side Add
// pair. In the value run, the last type's sites build its provider by a
// literal, a record update or a field write as well (v1 audit 3, G1): a
// law the constructor's states satisfy and other values do not must keep
// those sites as the reference writes them.
// ERGO_REWRITE_AGREE_SEED replays a seed.
//
// It builds Go because the rewritten and the reference programs must run:
// their providers keep state in a sync.Ref, a Go body that check-time
// evaluation does not run, and the evidence is the built check run.
func TestRewriteAgrees(t *testing.T) {
	needsBuild(t)
	defaultLoweringOnly(t) // its anchor checks the rewritten form
	saved := inlineLowering
	inlineLowering = "call"
	defer func() { inlineLowering = saved }()
	for _, values := range []bool{false, true} {
		t.Run(fmt.Sprintf("value=%v", values), func(t *testing.T) { rewriteAgree(t, values) })
	}
}

// rewriteAgree builds Go because agreement includes native pointer updates,
// handle-backed providers and the compiled evidence runner.
func rewriteAgree(t *testing.T, values bool) {
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
	src, anchor := agreeProgram(rng, types, values)
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
		// This is an engine property with deliberately false obligations:
		// real builds refuse a reachable counterexample. Feed the engine
		// the remaining verdicts so it can test provider-specific refusal
		// and alternate matching rules on the same generated windows.
		var verdicts strings.Builder
		for _, line := range strings.Split(checkOut, "\n") {
			if !strings.HasPrefix(line, "FAIL law ") {
				verdicts.WriteString(line + "\n")
			}
		}
		return verdicts.String(), nil
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
	wantDefault := 2
	if values {
		wantDefault = 1
	}
	if got := strings.Count(goFunc(t, (&lowered{files: built}).mainGo(), anchor), add); got != wantDefault {
		t.Fatalf("seed %d: the build's anchor calls %s %d times, want %d\ncheck run:\n%s\n%s", seed, add, got, wantDefault, checkOut, replay)
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
		constant := "int(3)"
		if values {
			constant = "Digit(3)"
		}
		if got := strings.Count(body, add); got != want || want == 1 && !strings.Contains(body, constant) {
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

type Box { s sync.Ref[int] } implements Counter

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
	// A missed false state is still only tested evidence. Simulate that
	// reporting miss without changing admission: the real build must keep
	// both Inc calls even when the sampled reporter says ok.
	evidence := ev.(*checkedEvidence)
	for key := range evidence.ok {
		evidence.ok[key] = "tested"
	}
	for _, r := range pkg.LawRuns {
		if r.Law.Name == "TwoIncsAreTwice" {
			for _, bd := range r.Bindings {
				evidence.ok[evidenceKey(r.Law.Title(pkg), provTitle(pkg, bd[0].Type))] = "tested"
			}
		}
	}
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
		"type P { s sync.Ref[int] } implements T\n\nfunc NewP() P { P{s: sync.NewRef[int](0)} }\n\n" +
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
	defaultLoweringOnly(t) // it counts the engine's work
	for n := 2; n <= 4; n++ {
		calls := []string{"c.Inc()"}
		for range n - 1 {
			calls = append(calls, "c.Add(x)")
		}
		src := "import sync\n\ntype T {\n\tAdd(n int),\n\tInc(),\n\tRead() int,\n\n" +
			"\tlaw Swap(a int) { Inc(); Add(a) } == { Add(a); Inc() },\n" +
			"\tlaw Back(a int) { Add(a); Inc() } == { Inc(); Add(a) },\n}\n\n" +
			"type P { s sync.Ref[int] } implements T\n\nfunc NewP() P { P{s: sync.NewRef[int](0)} }\n\n" +
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
