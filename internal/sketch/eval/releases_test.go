package eval

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestEvalReleasesAgreeWithModel is the release property of ORACLE O2
// (AGENTS.md, Tests: a seeded property against an independent reference
// model; resources.md, Context ownership as built). Each seed generates
// functions whose bodies nest withs: inline contexts of one or two
// resource providers (Res binds Store, Aud binds Audit; each Close writes
// a line), the named context Both, whose Store is built through the Audit
// it binds before it, a Lease whose Close notes through Audit, so it runs in the
// environment outside its with, a local context named by two withs, a
// with of two operands, a log annotation (a context value) and a with
// that installs a Context value. The innermost block ends normally, by
// return, by a typed failure (returned or by try), or the with fails while it
// builds its providers (R7). Every effect writes a line to one journal.
// The model is a Go interpreter of the same nest: a stack of levels, each
// with the providers it built, released last first on every exit, in
// the bindings outside the with. The test asserts the journal equals the
// model's, and the law ReleasedEachCase, run under Both, holds only when
// each case released what it built (types.md, Laws 10). A failing seed
// prints the Ergo source, the result and the replay command.
//
// Mutants only it kills (testdata/mutants.json): oracle-o2-release-forward,
// oracle-o2-release-skipped-on-exit, oracle-o2-release-in-inner-env,
// oracle-o2-acquire-fail-leaks, oracle-o2-law-case-unreleased,
// oracle-o2-context-value-leaks.
func TestEvalReleasesAgreeWithModel(t *testing.T) {
	n := 150
	if testing.Short() {
		n = 50
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_RELEASE_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	reached := map[string]bool{}
	for seed := first; seed < first+uint64(n); seed++ {
		g := &releaseGen{r: rand.New(rand.NewPCG(seed, 0x2e)), reached: reached}
		src := g.program()
		if dir := os.Getenv("ERGO_EVAL_RELEASE_DUMP"); dir != "" {
			// The program, to run under ergo test as ground truth.
			if err := os.WriteFile(fmt.Sprintf("%s/seed%d.ergo", dir, seed), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		pkg, diags := check.Check("main", []*syntax.File{f}, nil)
		if len(diags) > 0 {
			t.Fatalf("seed %d: check: %v\n%s", seed, diags, src)
		}
		admitReleasingContexts(pkg)
		rep, err := Run(pkg, DefaultBudget())
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		names := map[string]bool{}
		for _, res := range rep.Results {
			names[res.Name] = true
			// The test and the run under Both must pass; the law's other
			// bindings (Res, Plain; Lease, whose Close notes through Audit) only
			// must not fail.
			mustPass := res.Name == "Releases" || res.Name == "Owned" || strings.HasSuffix(res.Name, " under Both")
			if (mustPass && res.Status != Passed) || res.Status == Refuted || res.Status == Defect {
				t.Fatalf("seed %d: %s is %s (%s at %s:%d)\n%s\nreplay: ERGO_EVAL_RELEASE_SEED=%d go test -run TestEvalReleasesAgreeWithModel ./internal/sketch/eval",
					seed, res.Name, res.Status, res.Reason, res.Site.File, res.Site.Line, src, seed)
			}
		}
		for _, want := range []string{"Releases", "Owned", "law Store.ReleasedEachCase under Both"} {
			if !names[want] {
				t.Fatalf("seed %d: no obligation %s in %+v", seed, want, rep.Results)
			}
		}
	}
	if n > 1 {
		for _, k := range releaseForms {
			if !reached[k] {
				t.Fatalf("the seeds never reached the form %s (reached %v)", k, reached)
			}
		}
	}
}

// admitReleasingContexts clears the one LawContext.Unrun reason that
// batch 19's checker drops (wf10 ex-entry: a law case releases the
// providers its build acquired), so the evaluator's per-case release runs
// at a base whose checker still skips such a context. Once that checker
// is merged the reason never occurs and this does nothing.
func admitReleasingContexts(pkg *check.Package) {
	for i := range pkg.LawRuns {
		for j := range pkg.LawRuns[i].Contexts {
			lc := &pkg.LawRuns[i].Contexts[j]
			if strings.HasSuffix(lc.Unrun, "which a law case does not release") {
				lc.Unrun = ""
			}
		}
	}
}

// releaseForms lists every with and exit a run of seeds must reach.
var releaseForms = []string{
	"with-res", "with-aud", "with-pair", "with-both", "with-lease", "with-local", "with-two", "with-note", "with-install",
	"exit-normal", "exit-return", "exit-fail", "exit-try", "exit-acquire",
}

const releasePrelude = `import log
import strconv
import sync

type Oops { Code int }

type Store {
	Put(k string),

	// ReleasedEachCase: run under Both, a case starts after every earlier
	// case released its Store.
	law ReleasedEachCase(k string) { ReleasedSoFar() },
}

type Audit {
	Note(s string),
}

let journal = sync.NewRef[[]string]([]string{})

func say(s string) { journal.Update(func(xs []string) []string { xs.Append(s) }) }

// open counts the builds of Both's Store less its closes.
let open = sync.NewRef[int](0)

// ReleasedSoFar: every build of Both's Store but the one in use was
// closed.
func ReleasedSoFar() bool { open.Get() == 0 || open.Get() == 1 }

type Res { name string }

func Put(r Res, k string) { say(r.name + ".put " + k) }

func Close(r Res) {
	say("close " + r.name)
	if r.name == "S" {
		open.Update(func(n int) int { n - 1 })
	}
}

type Aud { name string }

func Note(a Aud, s string) { say(a.name + ".note " + s) }

func Close(a Aud) { say("close " + a.name) }

type Lease { name string }

func Put(l Lease, k string) { say(l.name + ".put " + k) }

func Close(l Lease) needs Audit { Audit.Note("release " + l.name) }

type Plain { name string }

func Put(p Plain, k string) { say(p.name + ".put " + k) }

func NewRes(name string) Res needs Audit {
	Audit.Note("build " + name)
	open.Update(func(n int) int { n + 1 })
	Res{name: name}
}

func openRes(name string, fail bool) Res fails Oops {
	if fail {
		return Oops{Code: 9}
	}
	Res{name: name}
}

func refuse(c int) int fails Oops { return Oops{Code: c} }

// owning owns a local resource: it is released when the function ends
// (resources.md R5, L8 B1), so the test Owned passes only with the
// release in the journal.
func owning() {
	let r = Res{name: "o"}
	r.Put("k")
}

test Owned {
	journal.Set([]string{})
	owning()
	assert journal.Get() == []string{"o.put k", "close o"}
}

let Both = context { Audit: Aud{name: "A"}, Store: NewRes("S") }

func who() string {
	match log.Annotation("who") {
	case Some[string] as s:
		s.Value
	case None:
		"-"
	}
}
`

// releaseGen generates the functions and keeps the model.
type releaseGen struct {
	r       *rand.Rand
	b       *strings.Builder
	out     []string
	reached map[string]bool
	fresh   int
	// usesLocal and usesValue: the function being written names its
	// local context lc, its Context value cv.
	usesLocal, usesValue bool
}

// relLevel is one with of the model: what it built, in order, and the
// bindings and annotation in force inside it.
type relLevel struct {
	built []relProv // in acquisition order
	store *relProv
	audit *relProv
	who   string
}

// relProv is a provider of the model: its kind (res, aud, lease, plain)
// and name.
type relProv struct{ kind, name string }

func (g *releaseGen) say(s string) { g.out = append(g.out, s) }

func (g *releaseGen) name(prefix string) string {
	g.fresh++
	return prefix + strconv.Itoa(g.fresh)
}

func (g *releaseGen) line(depth int, format string, args ...any) {
	g.b.WriteString(strings.Repeat("\t", depth))
	fmt.Fprintf(g.b, format, args...)
	g.b.WriteByte('\n')
}

func (g *releaseGen) program() string {
	g.b = &strings.Builder{}
	g.b.WriteString(releasePrelude)
	nf := 2 + g.r.IntN(4)
	g.out = nil
	for k := 0; k < nf; k++ {
		g.function(k)
	}
	g.b.WriteString("\ntest Releases {\n\tjournal.Set([]string{})\n")
	for k := 0; k < nf; k++ {
		fmt.Fprintf(g.b, "\tlet r%d = f%d() catch {\n\tcase Oops as o:\n\t\t100 + o.Code\n\t}\n", k, k)
		fmt.Fprintf(g.b, "\tsay(\"f%d=\" + strconv.Itoa(r%d))\n", k, k)
	}
	quoted := make([]string, len(g.out))
	for i, s := range g.out {
		quoted[i] = strconv.Quote(s)
	}
	fmt.Fprintf(g.b, "\tassert journal.Get() == []string{%s}\n}\n", strings.Join(quoted, ", "))
	return g.b.String()
}

// function writes func fK and runs its model; the model's lines are
// appended in call order, and the test's own line after them.
func (g *releaseGen) function(k int) {
	fmt.Fprintf(g.b, "\nfunc f%d() int fails Oops {\n", k)
	exits := []string{"exit-normal", "exit-return", "exit-fail", "exit-try", "exit-acquire"}
	exit := exits[g.r.IntN(len(exits))]
	g.reached[exit] = true
	// The body goes to its own builder: the local contexts it names are
	// declared before it.
	head := g.b
	g.b = &strings.Builder{}
	g.usesLocal, g.usesValue = false, false
	defer func() {
		body := g.b.String()
		g.b = head
		if g.usesLocal {
			g.line(1, "let lc = context { Store: Res{name: \"loc\"} }")
		}
		if g.usesValue {
			g.line(1, "let cv Context[Store] = context { Store: Plain{name: \"cv\"} }")
		}
		g.b.WriteString(body)
		g.line(1, "0")
		g.b.WriteString("}\n")
	}()
	top := &relLevel{who: "-"}
	result := 0
	failed := false
	g.nest(1, []*relLevel{top}, 1+g.r.IntN(3), exit, &result, &failed)
	switch {
	case failed:
		g.say(fmt.Sprintf("f%d=%d", k, 100+result))
	default:
		g.say(fmt.Sprintf("f%d=%d", k, result))
	}
}

// nest writes levels withs, each with steps, and the exit in the
// innermost; it runs the model as it writes.
func (g *releaseGen) nest(depth int, stack []*relLevel, levels int, exit string, result *int, failed *bool) bool {
	cur := stack[len(stack)-1]
	g.steps(depth, cur)
	if levels == 0 {
		switch exit {
		case "exit-return":
			v := 1 + g.r.IntN(50)
			g.line(depth, "return %d", v)
			*result = v
			return true
		case "exit-fail":
			v := 1 + g.r.IntN(50)
			g.line(depth, "return Oops{Code: %d}", v)
			*result, *failed = v, true
			return true
		case "exit-try":
			v := 1 + g.r.IntN(50)
			g.line(depth, "let t = try refuse(%d)", v)
			g.line(depth, "say(strconv.Itoa(t))")
			*result, *failed = v, true
			return true
		case "exit-acquire":
			a := g.name("q")
			g.line(depth, "with context { Audit: Aud{name: %q}, Store: try openRes(%q, true) } {", a, g.name("x"))
			g.line(depth+1, "Store.Put(\"never\")")
			g.line(depth+1, "Audit.Note(\"never\")")
			g.line(depth, "}")
			// Aud was built first; the failure releases it (R7).
			g.say("close " + a)
			*result, *failed = 9, true
			return true
		}
		return false
	}
	kinds := []string{"with-res", "with-aud", "with-pair", "with-both", "with-local", "with-two", "with-note", "with-install"}
	if cur.audit != nil {
		kinds = append(kinds, "with-lease")
	}
	kind := kinds[g.r.IntN(len(kinds))]
	g.reached[kind] = true
	next := &relLevel{store: cur.store, audit: cur.audit, who: cur.who}
	res := func() *relProv { return &relProv{"res", g.name("s")} }
	aud := func() *relProv { return &relProv{"aud", g.name("a")} }
	var header string
	switch kind {
	case "with-res":
		p := res()
		header = fmt.Sprintf("with context { Store: Res{name: %q} } {", p.name)
		next.built, next.store = []relProv{*p}, p
	case "with-aud":
		p := aud()
		header = fmt.Sprintf("with context { Audit: Aud{name: %q} } {", p.name)
		next.built, next.audit = []relProv{*p}, p
	case "with-pair":
		s, a := res(), aud()
		if g.r.IntN(2) == 0 {
			header = fmt.Sprintf("with context { Store: Res{name: %q}, Audit: Aud{name: %q} } {", s.name, a.name)
			next.built = []relProv{*s, *a}
		} else {
			header = fmt.Sprintf("with context { Audit: Aud{name: %q}, Store: Res{name: %q} } {", a.name, s.name)
			next.built = []relProv{*a, *s}
		}
		next.store, next.audit = s, a
	case "with-both":
		header = "with Both {"
		// Aud A first; S is built through it.
		g.say("A.note build S")
		a, s := &relProv{"aud", "A"}, &relProv{"res", "S"}
		next.built, next.store, next.audit = []relProv{*a, *s}, s, a
	case "with-lease":
		p := &relProv{"lease", g.name("l")}
		header = fmt.Sprintf("with context { Store: Lease{name: %q} } {", p.name)
		next.built, next.store = []relProv{*p}, p
		if g.r.IntN(2) == 0 {
			// The with binds an Audit of its own too: the Lease's
			// release still notes through the outer one.
			a := aud()
			header = fmt.Sprintf("with context { Store: Lease{name: %q}, Audit: Aud{name: %q} } {", p.name, a.name)
			next.built, next.audit = []relProv{*p, *a}, a
		}
	case "with-local":
		header = "with lc {"
		g.usesLocal = true
		// The block may need only Store: lc's Audit is then not built.
		p := &relProv{"res", "loc"}
		next.built, next.store = []relProv{*p}, p
	case "with-two":
		s, a := res(), aud()
		header = fmt.Sprintf("with context { Audit: Aud{name: %q} }, context { Store: Res{name: %q} } {", a.name, s.name)
		// Two levels in one: the second operand's providers release
		// before the first's.
		next.built, next.store, next.audit = []relProv{*a, *s}, s, a
	case "with-note":
		w := g.name("w")
		header = fmt.Sprintf("with log.Annotate(\"who\", %q) {", w)
		next.who = w
	case "with-install":
		header = "with cv {"
		g.usesValue = true
		next.store = &relProv{"plain", "cv"}
	}
	g.line(depth, "%s", header)
	// Every block uses each capability its with binds, so the with
	// acquires all it lists (resources.md, What a with acquires 1).
	if next.store != nil {
		k := g.name("k")
		g.line(depth+1, "Store.Put(%q)", k)
		g.say(next.store.name + ".put " + k)
	}
	if next.audit != nil {
		k := g.name("n")
		g.line(depth+1, "Audit.Note(%q)", k)
		g.say(next.audit.name + ".note " + k)
	}
	left := g.nest(depth+1, append(stack, next), levels-1, exit, result, failed)
	g.line(depth, "}")
	// The with's block ends: release, last built first, outside it.
	for i := len(next.built) - 1; i >= 0; i-- {
		p := next.built[i]
		switch p.kind {
		case "lease":
			g.say(cur.audit.name + ".note release " + p.name)
		default:
			g.say("close " + p.name)
		}
	}
	if left {
		return true
	}
	g.steps(depth, cur)
	return false
}

// steps writes a few effects at the level cur and runs them.
func (g *releaseGen) steps(depth int, cur *relLevel) {
	for i := g.r.IntN(3); i > 0; i-- {
		switch {
		case cur.store != nil && g.r.IntN(3) == 0:
			k := g.name("k")
			g.line(depth, "Store.Put(%q)", k)
			g.say(cur.store.name + ".put " + k)
		case cur.audit != nil && g.r.IntN(2) == 0:
			k := g.name("n")
			g.line(depth, "Audit.Note(%q)", k)
			g.say(cur.audit.name + ".note " + k)
		default:
			g.line(depth, "say(\"who=\" + who())")
			g.say("who=" + cur.who)
		}
	}
}
