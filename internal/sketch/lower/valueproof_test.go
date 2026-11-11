package lower

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestPrivateRuntimeOperationCalls is the smallest repro of LR3a's missing
// caller: an operation private to std json must call the exported Go method.
// It typechecks the real lowered caller, not a naming-helper result.
func TestPrivateRuntimeOperationCalls(t *testing.T) {
	files, ok := std.Package("json")
	if !ok {
		t.Fatal("json is not embedded")
	}
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	u := Unit{}
	for _, name := range names {
		f, err := syntax.Parse(files[name])
		if err != nil {
			t.Fatal(err)
		}
		u.Files = append(u.Files, f)
		u.Sources = append(u.Sources, Source{Path: "/ergo-test/json/" + name, Text: files[name]})
	}
	const src = `func Probe() string {
		let t = NewText()
		t.raw("private caller")
		t.Written()
	}`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	u.Files = append(u.Files, f)
	u.Sources = append(u.Sources, Source{Path: "/ergo-test/json/probe.ergo", Text: src})
	pkg, ds := check.StdFixture("json", u.Files)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	u.Package = pkg
	main := checkPackage(t, "main", []pkgSource{{testSourcePath, `import fixturejson
func main() needs Console { Console.Println(json.Probe()) }`}}, pkgImporter{"fixturejson": pkg})
	p := mustLowerUnit(t, "private", main, false)
	typecheckGo(t, []*lowered{p})
}

const wheelSource = `
type Digit int where 0 <= it && it <= 9
type Counter {
	Add(n Digit),
	Total() Digit,
	law AddJoins(a Digit, b Digit) { Add(a); Add(b) } == { Add(plus(a, b)) },
}
type Wheel { at Digit }
func NewWheel() Wheel { Wheel{at: 0} }
func plus(a Digit, b Digit) Digit {
	match (int(a) + int(b)) % 10 {
	case 0: 0
	case 1: 1
	case 2: 2
	case 3: 3
	case 4: 4
	case 5: 5
	case 6: 6
	case 7: 7
	case 8: 8
	case _: 9
	}
}
func Add(w *Wheel, n Digit) { w.at = plus(w.at, n) }
func Total(w *Wheel) Digit { w.at }
func site() Digit {
	let mut w = NewWheel()
	w.Add(1)
	w.Add(2)
	w.Add(4)
	w.Total()
}
func main() needs Console { Console.Println("\(site())") }
`

// TestProvedRunsModel builds Go because the proof must execute the same
// lowered provider bodies that the real build rewrites, not a second model
// implementation. The modulo-ten reference has ten reachable states and
// AddJoins holds on every pair of Digit arguments.
func TestProvedRunsModel(t *testing.T) {
	needsBuild(t)
	savedInline := inlineLowering
	inlineLowering = "call"
	defer func() { inlineLowering = savedInline }()
	provedModel(t)
	src := wheelSource
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v\n%s", diags, src)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	var receipt string
	ev, err := RewriteChecks(u, func(files []File, laws []string) (string, error) {
		exes := buildAll(t, []*lowered{{name: "proof", src: src, file: file, pkg: pkg, files: files}}, true)
		out, stderr, code := runExeArgs(t, exes["proof"], "--law", "Counter.AddJoins")
		if code != 0 {
			t.Fatalf("proof exit %d:\n%s\n%s", code, out, stderr)
		}
		receipt = out
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(receipt, "ok law Counter.AddJoins for Wheel (proved: all 1000 cases)") {
		t.Fatalf("missing exhaustive receipt:\n%s", receipt)
	}
	u.Rewrite = ev
	fast, err := Lower(u)
	if err != nil {
		t.Fatal(err)
	}
	rewriteLowering = "reference"
	ref, err := Lower(u)
	rewriteLowering = ""
	if err != nil {
		t.Fatal(err)
	}
	progs := []*lowered{{name: "fast", src: src, file: file, pkg: pkg, files: fast}, {name: "ref", src: src, file: file, pkg: pkg, files: ref}}
	for _, p := range progs {
		body := goFunc(t, p.mainGo(), "site")
		want := 3
		if p.name == "fast" {
			want = 1
		}
		if got := strings.Count(body, ".Add("); got != want {
			t.Fatalf("%s has %d Add calls, want %d:\n%s", p.name, got, want, body)
		}
	}
	exes := buildAll(t, progs, true)
	for _, name := range []string{"fast", "ref"} {
		out, stderr, code := runExe(t, exes[name])
		if out != "7\n" || code != 0 {
			t.Fatalf("%s: %q, exit %d, stderr %s", name, out, code, stderr)
		}
	}
}

// provedModel draws the state bounds and constructor state, then uses an
// independent transition table to close reachable states and decide equality.
// Read hides all state, so an observation-only implementation cannot pass.
func provedModel(t *testing.T) {
	t.Helper()
	seed := int64(1)
	if s := os.Getenv("ERGO_PROVED_RUNS_SEED"); s != "" {
		var err error
		seed, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewSource(seed))
	type model struct {
		n, initial                                  int
		bounded, ctor, handle, unlisted, bad, guard bool
		want                                        string
	}
	ms := []model{
		{bounded: true, ctor: true},
		{bounded: true, ctor: true, bad: true},
		{ctor: true, bad: true},
		{bounded: true},
		{bounded: true, bad: true},
		{bounded: true, ctor: true, handle: true},
		{bounded: true, ctor: true, unlisted: true, bad: true},
		{bounded: true, ctor: true, unlisted: true},
		{},
		{bounded: true, ctor: true, bad: true, guard: true},
	}
	var program strings.Builder
	program.WriteString("import sync\n")
	for i := range ms {
		var src strings.Builder
		m := &ms[i]
		m.n = 3 + rng.Intn(7)
		m.initial = rng.Intn(m.n)
		// Reference closure, independent of the generated runner's queue/key.
		reachable := map[int]bool{}
		var visit func(int)
		visit = func(s int) {
			if reachable[s] {
				return
			}
			reachable[s] = true
			visit((s + 1) % m.n)
			q := (s + 2) % m.n
			if m.bad && s == m.n-1 {
				q = 0
			}
			visit(q)
		}
		if m.ctor && !m.unlisted {
			visit(m.initial)
		} else {
			for s := range m.n {
				reachable[s] = true
			}
		}
		same := true
		for s := range reachable {
			if m.guard && s == m.n-1 {
				continue
			}
			q := (s + 2) % m.n
			if m.bad && s == m.n-1 {
				q = 0
			}
			same = same && (s+2)%m.n == q
		}
		switch {
		case m.handle, !m.bounded && !m.ctor:
			m.want = "unproved"
		case same:
			m.want = "proved"
		case m.ctor && !m.unlisted:
			m.want = "FAIL"
		default:
			m.want = "unproved"
		}
		suffix := strconv.Itoa(i)
		p, typ, small := "P"+suffix, "Ops"+suffix, "Small"+suffix
		fmt.Fprintf(&src, "type %s int where 0 <= it && it < %d\n", small, m.n)
		fieldT := "int"
		if m.bounded {
			fieldT = small
		}
		fmt.Fprintf(&src, "type %s { s %s", p, fieldT)
		if m.handle {
			src.WriteString(", h sync.Ref[bool]")
		}
		src.WriteString(" }\n")
		fmt.Fprintf(&src, "func state%s(n int) %s { match n %% %d {\n", suffix, fieldT, m.n)
		for s := 0; s < m.n-1; s++ {
			fmt.Fprintf(&src, "case %d: %d\n", s, s)
		}
		fmt.Fprintf(&src, "case _: %d\n} }\n", m.n-1)
		if m.ctor {
			fmt.Fprintf(&src, "func New%s() %s { %s{s: %d", p, p, p, m.initial)
			if m.handle {
				src.WriteString(", h: sync.NewRef(false)")
			}
			src.WriteString("} }\n")
		}
		fmt.Fprintf(&src, "func Inc(p *%s) { p.s = state%s(int(p.s)+1) }\n", p, suffix)
		fmt.Fprintf(&src, "func Twice(p *%s) { ", p)
		if m.bad {
			fmt.Fprintf(&src, "if int(p.s) == %d { p.s = 0 } else { ", m.n-1)
		}
		fmt.Fprintf(&src, "p.s = state%s(int(p.s)+2)", suffix)
		if m.bad {
			src.WriteString(" }")
		}
		src.WriteString(" }\n")
		fmt.Fprintf(&src, "func Read(_ %s) int { 0 }\n", p)
		fmt.Fprintf(&src, "func Ready(p %s) bool { int(p.s) != %d }\n", p, m.n-1)
		fmt.Fprintf(&src, "type %s { Inc(), Twice(), Read() int, Ready() bool,\n", typ)
		if m.unlisted {
			src.WriteString("Touch(n int),\n")
		}
		src.WriteString("law Same()")
		if m.guard {
			src.WriteString(" when Ready() == true")
		}
		src.WriteString(" { Inc(); Inc() } == { Twice() }, }\n")
		if m.unlisted {
			fmt.Fprintf(&src, "func Touch(_ *%s, _ int) { }\n", p)
		}
		block := src.String()
		for _, name := range []string{"Inc", "Twice", "Read", "Ready", "Touch"} {
			block = strings.ReplaceAll(block, name+"(", name+suffix+"(")
		}
		program.WriteString(block)
	}
	text := program.String()
	u := checkPackage(t, "main", []pkgSource{{testSourcePath, text}}, nil)
	p := mustLowerUnit(t, "model", u, true)
	exes := buildAll(t, []*lowered{p}, true)
	for i, m := range ms {
		title := fmt.Sprintf("Ops%d.Same", i)
		out, stderr, code := runExeArgs(t, exes["model"], "--law", title)
		proved := strings.Contains(out, "(proved:")
		refuted := strings.Contains(out, "FAIL law "+title+" for ")
		if proved != (m.want == "proved") || refuted != (m.want == "FAIL") || (code != 0) != (m.want == "FAIL") {
			t.Fatalf("seed %d: %s want %s, code %d:\n%s\n%s\n%s\nreplay: ERGO_PROVED_RUNS_SEED=%d go test -run TestProvedRunsModel ./internal/sketch/lower", seed, title, m.want, code, out, stderr, text, seed)
		}
	}
	out, stderr, code := runExeArgs(t, exes["model"], "--law", "Ops0.Same", "--states", "1")
	if code != 0 || strings.Contains(out, "(proved:") || !strings.Contains(out, "not proved: explored") {
		t.Fatalf("seed %d: a truncated search proved:\n%s\n%s", seed, out, stderr)
	}
}
