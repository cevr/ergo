package lower

import (
	"fmt"
	"go/ast"
	"go/token"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Wire ownership (runtime-199 stage 3b; resources.md, Wire ownership;
// runtime.md, With cleanup). TestRuntime199WireProperty generates wire
// DAGs, runs every with of each under each exit class in real programs, and
// compares what they print with the checker's ownership fact
// (With.Acquired): each acquired resource closes once, at the end of its
// with, last built first; an overridden or unreached provider never runs;
// a nested with restores the outer environment; a child task drains before
// the providers it borrows close, on the with's normal end too; a release
// defect at any position, under every exit class, lets the later releases
// run and keeps every cause; a stop that arrives while a release runs lets
// every release finish and is observed after the with. A named wire is
// closed, so its providers cannot print when they are built: the build
// order shows as the release order (last built first) and as a dependency
// read, which fails when its entry is not built yet.

// wireEntry is one entry of a generated wire, in model order: it depends
// only on capabilities bound by earlier entries.
type wireEntry struct {
	caps []int // the capabilities it binds; two make a shared entry
	deps []int // the capabilities its provider uses
	res  bool  // its type has a checked Close
}

type wireDAG struct {
	k       int
	caps    int
	entries []wireEntry
	order   []int // the entries' order in the wire's source
	base    int   // a capability an embedded wire also binds and W replaces, -1 none
	uses    []int // the capabilities the with's block uses
	inner   int   // the capability a nested with rebinds, -1 none
	child   int   // the capability a child task uses, -1 none
}

func (d *wireDAG) typ(e int) string { return fmt.Sprintf("T%d_%d", d.k, e) }
func (d *wireDAG) baseTyp() string  { return fmt.Sprintf("T%d_b", d.k) }
func (d *wireDAG) innerTyp() string { return fmt.Sprintf("T%d_v", d.k) }
func (d *wireDAG) cap(c int) string { return fmt.Sprintf("C%d_%d", d.k, c) }

// binder is the entry that binds capability c.
func (d *wireDAG) binder(c int) int {
	for e, en := range d.entries {
		if slices.Contains(en.caps, c) {
			return e
		}
	}
	panic("unbound capability")
}

// reached is the model's acquisition set: the entries that bind a used
// capability and, transitively, the entries their providers use.
func (d *wireDAG) reached() map[int]bool {
	out := map[int]bool{}
	var visit func(c int)
	visit = func(c int) {
		e := d.binder(c)
		if out[e] {
			return
		}
		out[e] = true
		for _, dep := range d.entries[e].deps {
			visit(dep)
		}
	}
	for _, c := range d.uses {
		visit(c)
	}
	return out
}

func genWireDAG(r *rand.Rand, k int) *wireDAG {
	d := &wireDAG{k: k, caps: 2 + r.IntN(5), base: -1, inner: -1, child: -1}
	perm := r.Perm(d.caps)
	for i := 0; i < len(perm); {
		n := 1
		if i+1 < len(perm) && r.IntN(3) == 0 {
			n = 2
		}
		en := wireEntry{caps: append([]int(nil), perm[i:i+n]...), res: r.IntN(4) != 0}
		var earlier []int
		for _, prev := range d.entries {
			earlier = append(earlier, prev.caps...)
		}
		for _, j := range r.Perm(len(earlier)) {
			if len(en.deps) < 2 && r.IntN(2) == 0 {
				en.deps = append(en.deps, earlier[j])
			}
		}
		slices.Sort(en.deps)
		d.entries = append(d.entries, en)
		i += n
	}
	d.order = r.Perm(len(d.entries))
	for _, c := range r.Perm(d.caps) {
		if r.IntN(2) == 0 || len(d.uses) == 0 {
			d.uses = append(d.uses, c)
		}
		if len(d.uses) == 4 {
			break
		}
	}
	slices.Sort(d.uses)
	if r.IntN(2) == 0 {
		for _, c := range r.Perm(d.caps) {
			if len(d.entries[d.binder(c)].caps) == 1 {
				d.base = c
				break
			}
		}
	}
	if r.IntN(2) == 0 {
		d.inner = d.uses[r.IntN(len(d.uses))]
	}
	if r.IntN(2) == 0 {
		d.child = d.uses[r.IntN(len(d.uses))]
	}
	return d
}

// decls writes the DAG's types, providers and wires. A type in fail
// panics in its Close after it prints. A type in slow sleeps in its Close
// between two lines, so a stop can arrive while the release runs.
func (d *wireDAG) decls(fail, slow map[string]bool) string {
	var b strings.Builder
	for c := 0; c < d.caps; c++ {
		fmt.Fprintf(&b, "type %s { Name() string }\n", d.cap(c))
	}
	typ := func(name string, res bool) {
		fmt.Fprintf(&b, "type %s {}\nfunc Name(_ %s) string { %q }\n", name, name, name)
		if res {
			needs := "Console"
			if slow[name] {
				needs += ", Scheduler"
			}
			fmt.Fprintf(&b, "func Close(_ %s) needs %s {\n\tConsole.Println(\"close %s\")\n", name, needs, name)
			if slow[name] {
				fmt.Fprintf(&b, "\tScheduler.Sleep(20ms)\n\tConsole.Println(\"closed %s\")\n", name)
			}
			if fail[name] {
				b.WriteString("\tlet xs = []int{}\n\tlet _ = xs[0]\n")
			}
			b.WriteString("}\n")
		}
	}
	for e, en := range d.entries {
		typ(d.typ(e), en.res)
		if len(en.deps) > 0 {
			var needs, body []string
			for _, c := range en.deps {
				needs = append(needs, d.cap(c))
				body = append(body, "\tlet _ = "+d.cap(c)+".Name()\n")
			}
			fmt.Fprintf(&b, "func mk%d_%d() %s needs %s {\n%s\t%s\n}\n", d.k, e, d.typ(e), strings.Join(needs, ", "), strings.Join(body, ""), d.typ(e))
		}
	}
	var entries []string
	for _, e := range d.order {
		en := d.entries[e]
		var keys []string
		for _, c := range en.caps {
			keys = append(keys, d.cap(c))
		}
		provider := d.typ(e)
		if len(en.deps) > 0 {
			provider = fmt.Sprintf("mk%d_%d()", d.k, e)
		}
		head := ""
		if d.base >= 0 && en.caps[0] == d.base {
			head = "replace "
		}
		entries = append(entries, head+strings.Join(keys, ", ")+": "+provider)
	}
	if d.base >= 0 {
		typ(d.baseTyp(), true)
		fmt.Fprintf(&b, "let Base%d = wire { %s: %s }\n", d.k, d.cap(d.base), d.baseTyp())
		entries = append(entries, fmt.Sprintf("Base%d", d.k))
	}
	fmt.Fprintf(&b, "let W%d = wire { %s }\n", d.k, strings.Join(entries, ", "))
	if d.inner >= 0 {
		typ(d.innerTyp(), true)
		fmt.Fprintf(&b, "let V%d = wire { %s: %s }\n", d.k, d.cap(d.inner), d.innerTyp())
	}
	return b.String()
}

// The exit classes of a with's block.
const (
	exitSuccess = "success" // the block ends normally
	exitTail    = "tail"    // its last statement fails, so the with fails
	exitMid     = "mid"     // a try leaves the function from mid-block
	exitDefect  = "defect"  // a defect leaves it
	exitStop    = "stop"    // its task is stopped while it waits
	// exitStopRel ends the block normally, and the stop arrives as the
	// with's releases run (a slow Close sleeps in one): every release still
	// runs whole, and the stop is observed after the with.
	exitStopRel = "stoprel"
)

func (d *wireDAG) hasInner(exit string) bool {
	return d.inner >= 0 && (exit == exitSuccess || exit == exitTail || exit == exitMid)
}

// hasChild reports a child task in the block that borrows a capability. On
// a success the block leaves it running, so the with's normal end
// (EndWith) drains it; on a mid exit or a defect the exit leaves it.
func (d *wireDAG) hasChild(exit string) bool {
	return d.child >= 0 && (exit == exitSuccess || exit == exitMid || exit == exitDefect)
}

// waits reports an exit whose function takes main's ready queue.
func waits(exit string) bool { return exit == exitStop || exit == exitStopRel }

func (d *wireDAG) fn(exit string) string { return fmt.Sprintf("run%d_%s", d.k, exit) }

// function writes the function that runs W's with under exit.
func (d *wireDAG) function(exit string) string {
	var b strings.Builder
	needs := []string{"Console"}
	var fails []string
	if exit == exitTail || exit == exitMid {
		fails = append(fails, "Fault")
	}
	if d.hasChild(exit) || waits(exit) {
		needs = append(needs, "Scheduler")
		fails = append(fails, "queue.Closed")
	}
	params := ""
	if waits(exit) {
		params = "ready queue.Queue[int]"
	}
	row := ""
	if len(fails) > 0 {
		row = " fails " + strings.Join(fails, " | ")
	}
	fmt.Fprintf(&b, "func %s(%s)%s needs %s {\n\twith W%d {\n", d.fn(exit), params, row, strings.Join(needs, ", "), d.k)
	for _, c := range d.uses {
		fmt.Fprintf(&b, "\t\tConsole.Println(\"use \" + %s.Name())\n", d.cap(c))
	}
	if d.hasInner(exit) {
		c := d.cap(d.inner)
		fmt.Fprintf(&b, "\t\twith V%d {\n\t\t\tConsole.Println(\"inner \" + %s.Name())\n\t\t}\n\t\tConsole.Println(\"after \" + %s.Name())\n", d.k, c, c)
	}
	if d.hasChild(exit) {
		fmt.Fprintf(&b, "\t\tlet ready = queue.New[int](0)\n\t\tlet t = task.Go() {\n\t\t\tscope.OnExit() { Console.Println(\"child \" + %s.Name()) }\n\t\t\ttry ready.Push(1)\n\t\t\tScheduler.Sleep(1h)\n\t\t}\n\t\ttry ready.Pop()\n", d.cap(d.child))
	}
	switch exit {
	case exitTail:
		b.WriteString("\t\ttry boom()\n")
	case exitMid:
		b.WriteString("\t\ttry boom()\n\t\tConsole.Println(\"unreached\")\n")
	case exitDefect:
		b.WriteString("\t\tlet xs = []int{}\n\t\tlet _ = xs[0]\n")
	case exitStop:
		b.WriteString("\t\ttry ready.Push(1)\n\t\tScheduler.Sleep(1h)\n")
	case exitStopRel:
		b.WriteString("\t\ttry ready.Push(1)\n")
	}
	switch {
	case d.hasChild(exit) && exit == exitSuccess:
		// The branch that would stop the child is not taken, so the child
		// is still running when the block ends.
		b.WriteString("\t\tif len(\"\") > 0 {\n\t\t\tt.Stop()\n\t\t}\n")
	case d.hasChild(exit):
		b.WriteString("\t\tt.Stop()\n")
	}
	fmt.Fprintf(&b, "\t}\n\tConsole.Println(\"end %d %s\")\n}\n", d.k, exit)
	return b.String()
}

// call writes main's statements that run exit's function.
func (d *wireDAG) call(exit string) string {
	switch exit {
	case exitSuccess:
		if d.hasChild(exit) {
			return "\t\ttry " + d.fn(exit) + "()\n"
		}
		return "\t\t" + d.fn(exit) + "()\n"
	case exitStop, exitStopRel:
		return fmt.Sprintf("\t\tlet r%d = queue.New[int](0)\n\t\tlet t%d = task.Go() { %s(r%d) }\n\t\ttry r%d.Pop()\n\t\tt%d.Stop()\n\t\tConsole.Println(\"stopped %d\")\n", d.k, d.k, d.fn(exit), d.k, d.k, d.k, d.k)
	case exitDefect:
		if d.hasChild(exit) {
			return "\t\ttry " + d.fn(exit) + "()\n"
		}
		return "\t\t" + d.fn(exit) + "()\n"
	}
	catch := fmt.Sprintf("\t\tcase Fault:\n\t\t\tConsole.Println(\"failure %d %s\")\n", d.k, exit)
	if d.hasChild(exit) {
		catch += "\t\tcase queue.Closed:\n\t\t\tConsole.Println(\"closed\")\n"
	}
	return "\t\t" + d.fn(exit) + "() catch {\n" + catch + "\t\t}\n"
}

const wirePrelude = `type Fault {}
func boom() fails Fault { return Fault }
let Live = wire { Scheduler: LiveScheduler }
`

// wireProgram is one generated program: its DAGs and the exits main runs,
// in order, the types whose Close fails and the types whose Close sleeps.
type wireProgram struct {
	name  string
	dags  []*wireDAG
	exits []string
	fail  map[string]bool
	slow  map[string]bool
	o     *lowered
}

func (p *wireProgram) src() string {
	var b strings.Builder
	b.WriteString(wirePrelude)
	for _, d := range p.dags {
		b.WriteString(d.decls(p.fail, p.slow))
		for _, exit := range p.exits {
			b.WriteString(d.function(exit))
		}
	}
	b.WriteString("func main() fails queue.Closed needs Console {\n\twith Live {\n")
	for _, d := range p.dags {
		for _, exit := range p.exits {
			b.WriteString(d.call(exit))
		}
	}
	b.WriteString("\t}\n}\n")
	var imports strings.Builder
	for _, pkg := range []string{"queue", "scope", "task"} {
		if strings.Contains(b.String(), pkg+".") {
			imports.WriteString("import " + pkg + "\n")
		}
	}
	return imports.String() + "\n" + b.String()
}

// acquired is the checker's fact for the with of wire name in fn: each
// acquisition's provider type, the capabilities it binds, and its Close.
type acquired struct {
	typ  string
	caps []string
	res  bool
}

func wireFact(t *testing.T, o *lowered, fn, wire string) []acquired {
	t.Helper()
	var out []acquired
	found := 0
	for _, decl := range o.file.Decls {
		fd, ok := decl.(*syntax.FuncDecl)
		if !ok || fd.Name.Name != fn || fd.Body == nil {
			continue
		}
		syntax.Inspect(fd.Body, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			w := o.pkg.Info.Withs[x]
			if w == nil || len(w.Contexts) != 1 || w.Contexts[0].Name != wire {
				return true
			}
			found++
			for _, a := range w.Acquired {
				var caps []string
				for _, i := range a.Caps {
					caps = append(caps, strings.TrimPrefix(a.Entry.Caps[i].String(), "main."))
				}
				out = append(out, acquired{strings.TrimPrefix(fmt.Sprint(o.pkg.Info.Types[a.Entry.Provider]), "main."), caps, a.Close != nil})
			}
			return true
		})
	}
	if found != 1 {
		t.Fatalf("%s has %d withs of %s, want 1", fn, found, wire)
	}
	return out
}

// checkFact compares the checker's acquisitions of W in fn with the model:
// the reached entries, each once with every capability it binds, a
// provider after the entries it uses, and no overridden provider.
func checkFact(t *testing.T, d *wireDAG, fact []acquired) {
	t.Helper()
	reached := d.reached()
	pos := map[string]int{}
	for i, a := range fact {
		if _, dup := pos[a.typ]; dup {
			t.Errorf("W%d: %s acquired twice", d.k, a.typ)
		}
		pos[a.typ] = i
		if a.typ == d.baseTyp() {
			t.Errorf("W%d: the overridden provider %s is acquired", d.k, a.typ)
		}
	}
	for e, en := range d.entries {
		i, ok := pos[d.typ(e)]
		if ok != reached[e] {
			t.Errorf("W%d: %s acquired %v, model %v (uses %v)", d.k, d.typ(e), ok, reached[e], d.uses)
			continue
		}
		if !ok {
			continue
		}
		var caps []string
		for _, c := range en.caps {
			caps = append(caps, d.cap(c))
		}
		if a := fact[i]; !sameSet(a.caps, caps) || a.res != en.res {
			t.Errorf("W%d: %s binds %v closes %v, model %v %v", d.k, a.typ, a.caps, a.res, caps, en.res)
		}
		for _, c := range en.deps {
			if j := pos[d.typ(d.binder(c))]; j >= i {
				t.Errorf("W%d: %s is built before %s, whose capability it uses", d.k, d.typ(e), d.typ(d.binder(c)))
			}
		}
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// closes is the release order of a with: its acquired resources, last
// acquired first.
func closes(fact []acquired) []string {
	return closeLines(fact, nil)
}

// closeLines is what a with's releases print: closes, and a second line
// after the sleep of each type in slow.
func closeLines(fact []acquired, slow map[string]bool) []string {
	var out []string
	for i := len(fact) - 1; i >= 0; i-- {
		if fact[i].res {
			out = append(out, "close "+fact[i].typ)
			if slow[fact[i].typ] {
				out = append(out, "closed "+fact[i].typ)
			}
		}
	}
	return out
}

// expect is what exit's run of d prints, from the checker's fact.
func expect(t *testing.T, o *lowered, d *wireDAG, exit string, slow map[string]bool) []string {
	t.Helper()
	fact := wireFact(t, o, d.fn(exit), fmt.Sprintf("W%d", d.k))
	checkFact(t, d, fact)
	bound := func(c int) string {
		for _, a := range fact {
			if slices.Contains(a.caps, d.cap(c)) {
				return a.typ
			}
		}
		t.Fatalf("W%d: no acquisition binds %s", d.k, d.cap(c))
		return ""
	}
	var lines []string
	for _, c := range d.uses {
		lines = append(lines, "use "+bound(c))
	}
	if d.hasInner(exit) {
		inner := wireFact(t, o, d.fn(exit), fmt.Sprintf("V%d", d.k))
		lines = append(lines, "inner "+d.innerTyp())
		lines = append(lines, closes(inner)...)
		lines = append(lines, "after "+bound(d.inner))
	}
	if d.hasChild(exit) {
		lines = append(lines, "child "+bound(d.child))
	}
	lines = append(lines, closeLines(fact, slow)...)
	switch exit {
	case exitSuccess:
		lines = append(lines, fmt.Sprintf("end %d %s", d.k, exit))
	case exitTail, exitMid:
		lines = append(lines, fmt.Sprintf("failure %d %s", d.k, exit))
	case exitStop, exitStopRel:
		lines = append(lines, fmt.Sprintf("stopped %d", d.k))
	}
	return lines
}

func TestRuntime199WireProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs generated programs")
	}
	r := rand.New(rand.NewPCG(199, 3))
	var dags []*wireDAG
	for k := 0; k < 24; k++ {
		dags = append(dags, genWireDAG(r, k))
	}
	// The generator reaches every shape the property claims.
	shapes := map[string]bool{}
	for _, d := range dags {
		reached := d.reached()
		for e, en := range d.entries {
			shapes["shared"] = shapes["shared"] || reached[e] && len(en.caps) == 2
			shapes["dependency"] = shapes["dependency"] || reached[e] && len(en.deps) > 0
			shapes["unreached resource"] = shapes["unreached resource"] || !reached[e] && en.res
		}
		shapes["overridden"] = shapes["overridden"] || d.base >= 0
		shapes["nested"] = shapes["nested"] || d.inner >= 0
		shapes["child"] = shapes["child"] || d.child >= 0
	}
	for _, s := range []string{"shared", "dependency", "unreached resource", "overridden", "nested", "child"} {
		if !shapes[s] {
			t.Fatalf("the generator reaches no %s entry", s)
		}
	}
	programs := []*wireProgram{{name: "wire_exits", dags: dags, exits: []string{exitSuccess, exitTail, exitMid, exitStop}}}
	type releaseCase struct {
		p        *wireProgram
		failures int
	}
	var released []releaseCase
	positions := map[string]bool{}
	for _, d := range dags {
		programs = append(programs, &wireProgram{name: fmt.Sprintf("wire_defect%d", d.k), dags: []*wireDAG{d}, exits: []string{exitDefect}})
	}
	for _, p := range programs {
		p.o = mustLower(t, p.name, p.src())
	}
	// The exits a release defect is generated under: every exit class.
	releaseExits := []string{exitDefect, exitSuccess, exitTail, exitMid, exitStop}
	releasedUnder := map[string]bool{}
	var stopRel []*wireProgram
	for _, p := range programs[1:] {
		// A release defect at one or two positions of W's release order
		// (the checker's fact), under each exit class in turn.
		d := p.dags[0]
		order := closes(wireFact(t, p.o, d.fn(exitDefect), fmt.Sprintf("W%d", d.k)))
		if len(order) == 0 {
			continue
		}
		at := func(i int) string { return strings.TrimPrefix(order[i%len(order)], "close ") }
		fail := map[string]bool{at(d.k % 3 * (len(order) - 1) / 2): true}
		if d.k%2 == 0 {
			fail[at(d.k/2)] = true
		}
		exit := releaseExits[d.k%len(releaseExits)]
		releasedUnder[exit] = true
		rp := &wireProgram{name: fmt.Sprintf("wire_release%d", d.k), dags: []*wireDAG{d}, exits: []string{exit}, fail: fail}
		rp.o = mustLower(t, rp.name, rp.src())
		released = append(released, releaseCase{rp, len(fail)})
		if d.k%3 == 1 {
			// A stop that arrives while the first release sleeps.
			sp := &wireProgram{name: fmt.Sprintf("wire_stoprel%d", d.k), dags: []*wireDAG{d}, exits: []string{exitStopRel}, slow: map[string]bool{at(0): true}}
			sp.o = mustLower(t, sp.name, sp.src())
			stopRel = append(stopRel, sp)
		}
	}
	for _, exit := range releaseExits {
		if !releasedUnder[exit] {
			t.Errorf("no release defect is generated under the %s exit", exit)
		}
	}
	if len(stopRel) == 0 {
		t.Error("no program stops a task while its with releases")
	}
	for _, rc := range released {
		programs = append(programs, rc.p)
	}
	programs = append(programs, stopRel...)
	var all []*lowered
	counted := 0
	for _, p := range programs {
		all = append(all, p.o)
		// Every provider a with builds counts in its frame's footprint (M1).
		counted += checkWithFootprints(t, p.o)
	}
	if counted == 0 {
		t.Error("no acquired provider has a footprint: the footprint check is vacuous")
	}
	exes := buildAll(t, all, true)
	if exes == nil {
		t.FailNow()
	}
	releaseFailures := map[string]int{}
	for _, rc := range released {
		releaseFailures[rc.p.name] = rc.failures
	}
	for _, p := range programs {
		t.Run(p.name, func(t *testing.T) {
			var want []string
			defect := false
			for _, d := range p.dags {
				for _, exit := range p.exits {
					lines := expect(t, p.o, d, exit, p.slow)
					if len(p.fail) > 0 {
						// A release defect raises at the with's end, or in
						// the task that stops the with's task: the releases
						// still print, the line that would follow does not
						// (the end of a success, the catch of a typed
						// failure, the line after a Stop).
						if exit != exitDefect {
							lines = lines[:len(lines)-1]
						}
						fact := wireFact(t, p.o, d.fn(exit), fmt.Sprintf("W%d", d.k))
						order := closes(fact)
						for i, c := range order {
							if p.fail[strings.TrimPrefix(c, "close ")] {
								switch {
								case i == 0:
									positions["first"] = true
								case i == len(order)-1:
									positions["last"] = true
								default:
									positions["middle"] = true
								}
							}
						}
						if len(p.fail) > 1 {
							positions["two"] = true
						}
					}
					want = append(want, lines...)
					defect = defect || exit == exitDefect
				}
			}
			stdout, stderr, code := runExe(t, exes[p.name])
			got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("stdout:\n%s\nwant:\n%s\nstderr:\n%s\nsource:\n%s", stdout, strings.Join(want, "\n"), stderr, p.o.src)
			}
			fails := releaseFailures[p.name]
			causes := fails
			if defect {
				causes++
			}
			if causes == 0 {
				if code != 0 {
					t.Errorf("exit %d, stderr:\n%s", code, stderr)
				}
				return
			}
			if code == 0 {
				t.Errorf("exit 0 after %d defects", causes)
			}
			if n := strings.Count("\n"+stderr, "\npanic: "); n != causes {
				t.Errorf("%d causes on stderr, want %d:\n%s", n, causes, stderr)
			}
			if n := strings.Count(stderr, "(in an exit action)"); n != fails {
				t.Errorf("%d release defects on stderr, want %d:\n%s", n, fails, stderr)
			}
		})
	}
	t.Logf("release defect positions %v", positions)
	for _, pos := range []string{"first", "middle", "last", "two"} {
		if !positions[pos] {
			t.Errorf("no generated release defect at the %s position", pos)
		}
	}
}

// checkWithFootprints asserts, for every with in o, that everything the
// lowering runs for each entry of its With.Acquired fact contributes its
// footprint to the frame the with runs in (runtime-199 stage 3c r1 M1, r2
// M1 and m2): the provider expression, with each capability call, each
// call of a function or method, each nested with's acquisitions and each
// call of unknown code, and the provider's Close (Acquisition.Close), which
// the with runs when its block ends. It returns how many acquisitions had a
// non-empty footprint, so a caller can see it is not vacuous.
func checkWithFootprints(t *testing.T, o *lowered) int {
	t.Helper()
	info := o.pkg.Info
	frame := func(x syntax.Node) (syntax.Node, check.Footprint) {
		var best syntax.Node
		for k := range info.Footprints {
			if k.Pos() <= x.Pos() && x.End() <= k.End() && (best == nil || best.Pos() <= k.Pos() && k.End() <= best.End()) {
				best = k
			}
		}
		if best == nil {
			t.Fatalf("%s: the with at %d has no enclosing frame", o.name, x.Pos())
		}
		return best, info.Footprints[best]
	}
	covers := func(outer, inner check.Footprint) bool {
		if outer.Opaque {
			return true
		}
		if inner.Opaque && !outer.Opaque || inner.Tasks && !outer.Tasks {
			return false
		}
		for _, c := range inner.Caps {
			if !slices.ContainsFunc(outer.Caps, func(o check.Type) bool { return o.String() == c.String() }) {
				return false
			}
		}
		return true
	}
	join := func(inner *check.Footprint, key syntax.Node) {
		g, ok := info.Footprints[key]
		if !ok {
			inner.Opaque = true
			return
		}
		inner.Caps = append(inner.Caps, g.Caps...)
		inner.Opaque = inner.Opaque || g.Opaque
		inner.Tasks = inner.Tasks || g.Tasks
	}
	// acquisition adds what the lowering runs for a: its constructor's
	// expression and its Close.
	var acquisition func(inner *check.Footprint, a *check.Acquisition)
	acquisition = func(inner *check.Footprint, a *check.Acquisition) {
		if cl := a.Close; cl != nil {
			if cl.Func == nil || cl.Func.Decl == nil {
				inner.Opaque = true
			} else {
				join(inner, cl.Func.Decl)
			}
		}
		syntax.Inspect(a.Entry.Provider, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.CallExpr:
				c := info.Calls[n]
				switch {
				case c == nil:
					inner.Opaque = true
				case c.Kind == check.CallConversion || c.Kind == check.CallLen || c.Kind == check.CallPrelude:
				case c.Kind == check.CallProviderOp:
					if sel, ok := n.Fun.(*syntax.SelectorExpr); ok {
						inner.Caps = append(inner.Caps, info.Types[sel.X])
					} else {
						inner.Opaque = true
					}
				case c.Kind == check.CallFunc && c.Func != nil && c.Func.Decl != nil:
					join(inner, c.Func.Decl)
				case c.Kind == check.CallWith && info.Withs[n] != nil:
					for _, b := range info.Withs[n].Acquired {
						acquisition(inner, b)
					}
				default:
					inner.Opaque = true
				}
			}
			return true
		})
	}
	counted := 0
	syntax.Inspect(o.file, func(n syntax.Node) bool {
		x, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		w := info.Withs[x]
		if w == nil {
			return true
		}
		key, outer := frame(x)
		for _, a := range w.Acquired {
			var inner check.Footprint
			acquisition(&inner, a)
			if !inner.Empty() {
				counted++
			}
			if !covers(outer, inner) {
				t.Errorf("%s: the frame at %d has footprint %+v, which omits the footprint %+v of the provider at %d its with at %d builds",
					o.name, key.Pos(), outer, inner, a.Entry.Provider.Pos(), x.Pos())
			}
		}
		return true
	})
	return counted
}

// A with's footprint covers every provider its With.Acquired fact builds,
// whatever the wire's spelling (stage 3c r1 M1). idle's own footprint is
// empty; its checked need B reaches mk, which uses A. PX.Use asks the
// running task to stop; a stopped work must not return from its loop. The
// inline form used to drop mk's footprint, so its loop had no checkpoint.
func TestRuntime199WireEmbeddedFootprint(t *testing.T) {
	const src = `type A { Use() }
type B { Use() }
type PX {}
type PY {}
func Use(_ PX) {}
func Use(_ PY) {}
func mk() PY needs A {
	A.Use()
	PY
}
let W = wire { A: PX, B: mk() }
func idle() needs B {}
func work() {
	with W { idle() }
	for n in 0..3 {
		let _ = n
	}
}
func main() { work() }
`
	twins := []*lowered{
		mustLower(t, "embedded_named", src),
		mustLower(t, "embedded_inline", strings.Replace(src, "with W {", "with wire { W } {", 1)),
	}
	for _, o := range twins {
		if checkWithFootprints(t, o) == 0 {
			t.Errorf("%s: no acquired provider has a footprint", o.name)
		}
		for _, d := range o.file.Decls {
			if f, ok := d.(*syntax.FuncDecl); ok && f.Name.Name == "work" {
				if got := o.pkg.Info.Footprints[f]; len(got.Caps) != 1 || got.Caps[0].String() != "main.A" || got.Opaque || got.Tasks {
					t.Errorf("%s: work's footprint %+v, want A", o.name, got)
				}
			}
		}
		// The test seam: PX.Use interrupts the task; returning from work
		// after the three back-edges panics with a sentinel.
		text := o.mainGo()
		const hook, end = "func (_p PX) Use(env *Env) {\n", "\twork(env)\n"
		if strings.Count(text, hook) != 1 || strings.Count(text, end) != 1 {
			t.Fatalf("%s: the seam drifted:\n%s", o.name, text)
		}
		text = strings.Replace(text, hook, hook+"\tenv.task.Interrupt()\n", 1)
		text = strings.Replace(text, end, end+"\tpanic(\"BAD: stopped work returned after three back-edges\")\n", 1)
		for i := range o.files {
			if o.files[i].Path == "main.go" {
				o.files[i].Data = []byte(text)
			}
		}
	}
	if testing.Short() {
		return
	}
	exes := buildAll(t, twins, true)
	if exes == nil {
		t.FailNow()
	}
	for _, o := range twins {
		stdout, stderr, code := runExe(t, exes[o.name])
		if code != 130 || strings.Contains(stderr, "BAD:") {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q; want exit 130, stopped at a checkpoint before work returns", o.name, code, stdout, stderr)
		}
	}
}

// A with runs the Close of each provider it acquired when its block ends,
// in its own frame, so the frame's footprint holds the Close's footprint
// (stage 3c r2 M1), in each spelling. idle and PR's constructor are pure;
// PR's Close uses A, whose PX.Use asks the running task to stop. The
// release does not observe the stop, so a stopped work must reach a
// checkpoint in its loop and never return. Without the Close's footprint
// work was pure, its loop had no checkpoint, and it returned.
func TestRuntime199WireCloseFootprint(t *testing.T) {
	const src = `type A { Use() }
type R { Use() }
type PX {}
type PR {}
func Use(_ PX) {}
func Use(_ PR) {}
func Close(_ PR) needs A { A.Use() }
func idle() needs R {}
func after() {}
let WR = wire { R: PR }
let VR = wire { WR }
func work() needs A {
	with wire { R: PR } { idle() }
	for n in 0..3 {
		let _ = n
	}
	after()
}
func main() { with wire { A: PX } { work() } }
`
	const spelling = "with wire { R: PR } {"
	twins := []*lowered{
		mustLower(t, "close_inline", src),
		mustLower(t, "close_named", strings.Replace(src, spelling, "with WR {", 1)),
		mustLower(t, "close_embedded", strings.Replace(src, spelling, "with wire { WR } {", 1)),
		mustLower(t, "close_nested", strings.Replace(src, spelling, "with wire { VR } {", 1)),
	}
	for _, o := range twins {
		if checkWithFootprints(t, o) == 0 {
			t.Errorf("%s: no acquisition has a footprint", o.name)
		}
		for _, d := range o.file.Decls {
			if f, ok := d.(*syntax.FuncDecl); ok && f.Name.Name == "work" {
				if got := o.pkg.Info.Footprints[f]; len(got.Caps) != 1 || got.Caps[0].String() != "main.A" || got.Opaque || got.Tasks {
					t.Errorf("%s: work's footprint %+v, want A", o.name, got)
				}
			}
		}
		// The test seam: PX.Use interrupts the task; reaching the call of
		// after, past the three back-edges and before work's frame ends,
		// panics with a sentinel.
		text := o.mainGo()
		const hook, end = "func (_p PX) Use(env *Env) {\n", "\tafter(env)\n"
		if strings.Count(text, hook) != 1 || strings.Count(text, end) != 1 {
			t.Fatalf("%s: the seam drifted:\n%s", o.name, text)
		}
		text = strings.Replace(text, hook, hook+"\tenv.task.Interrupt()\n", 1)
		text = strings.Replace(text, end, "\tpanic(\"BAD: stopped work passed three back-edges\")\n", 1)
		for i := range o.files {
			if o.files[i].Path == "main.go" {
				o.files[i].Data = []byte(text)
			}
		}
	}
	if testing.Short() {
		return
	}
	exes := buildAll(t, twins, true)
	if exes == nil {
		t.FailNow()
	}
	for _, o := range twins {
		stdout, stderr, code := runExe(t, exes[o.name])
		if code != 130 || strings.Contains(stderr, "BAD:") {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q; want exit 130, stopped at a checkpoint before work returns", o.name, code, stdout, stderr)
		}
	}
}

// The head refuses this program. If a freshness regression admits it, run
// it so the witness shows the same package resource closing twice.
func TestRuntime199WireFreshProvider(t *testing.T) {
	const tail = `
func main() needs Console {
	with W { A.Use() }
	with W { A.Use() }
	Console.Println("end")
}
type Fault {}
func risky() int fails Fault { return Fault }
func run(n int, f func()) { f() }
`
	conn := func(body string) string {
		return `type A { Use() }
type Conn { n int }
func Use(_ Conn) {}
func Close(_ Conn) needs Console { Console.Println("closed") }
let shared = Conn{n: 1}
func get() Conn { ` + body + ` }
let W = wire { A: get() }` + tail
	}
	// The prelude's bodiless Delete returns its argument, the package map
	// (stage 3b round 3 review, x1).
	const externDelete = `type A { Use() }
type Registry Map[string, int]
func Use(_ Registry) {}
func Close(_ Registry) needs Console { Console.Println("closed") }
let shared = Map[string, int]{"a": 1}
// The with owns no value of this provider.
let W = wire { A: Registry(shared.Delete("zz")) }` + tail
	for _, tc := range []struct{ name, src string }{
		{"shared", conn("shared")},
		{"trailing catch return", conn("run(risky() catch { case Fault: return shared }) {}; Conn{n: 2}")},
		{"trailing binary operand", conn("run(1) {}; run((risky() catch { case Fault: return shared }) + 1) {}; Conn{n: 2}")},
		{"extern returning its argument", externDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.src
			o := lowerSource("wire_reused", src, true)
			if len(o.diags) == 1 && o.diags[0].Code == "S109" && o.diags[0].Line == 7 && strings.Contains(o.diags[0].Message, "#197") {
				return
			}
			if o.err != nil || o.panicked != nil || len(o.diags) != 0 || len(o.refused) != 0 {
				t.Fatalf("want located S109 for a borrowed call result; got err=%v panic=%v diagnostics=%v refusals=%v", o.err, o.panicked, o.diags, o.refused)
			}
			if testing.Short() {
				t.Fatal("borrowed call result admitted")
			}
			exes := buildAll(t, []*lowered{o}, true)
			if exes == nil {
				t.FailNow()
			}
			stdout, stderr, code := runExe(t, exes[o.name])
			t.Fatalf("borrowed call result admitted: exit=%d stdout=%q stderr=%q; want S109 before any acquisition", code, stdout, stderr)
		})
	}
}

// Frame actions run after open withs unwind, even when registered before
// the with; registering an action inside a with still gives it to the
// frame. These orders are observable independently of acquisition facts.
func TestRuntime199WireExitActions(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs cleanup ordering witnesses")
	}
	const prelude = `import scope
type A { Name() string }
type B { Name() string }
type TA {}
type TB {}
func Name(_ TA) string { "A" }
func Name(_ TB) string { "B" }
func Close(_ TA) needs Console { Console.Println("A closed") }
func Close(_ TB) needs Console { Console.Println("B closed") }
let W = wire { A: TA }
let V = wire { B: TB }
`
	cases := []struct {
		name, body, want string
		defect           bool
	}{
		{"nested_abnormal_exit", `func run() needs Console {
	scope.OnExit() { Console.Println("frame exit") }
	with W {
		Console.Println("use " + A.Name())
		with V {
			Console.Println("inner " + B.Name())
			let xs = []int{}
			let _ = xs[0]
		}
		Console.Println("unreached")
	}
}
func main() needs Console { run() }
`, "use A\ninner B\nB closed\nA closed\nframe exit\n", true},
		{"action_in_with", `func run() needs Console {
	with W {
		Console.Println("use " + A.Name())
		scope.OnExit() { Console.Println("frame exit") }
	}
	Console.Println("after with")
}
func main() needs Console { run(); Console.Println("end") }
`, "use A\nA closed\nafter with\nframe exit\nend\n", false},
	}
	var programs []*lowered
	for _, tc := range cases {
		programs = append(programs, mustLower(t, tc.name, prelude+tc.body))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.FailNow()
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runExe(t, exes[tc.name])
			if stdout != tc.want {
				t.Errorf("stdout=%q, want %q; stderr=%s", stdout, tc.want, stderr)
			}
			if tc.defect {
				if code == 0 || strings.Count("\n"+stderr, "\npanic: ") != 1 || !strings.Contains(stderr, "index out of range") {
					t.Errorf("want the body defect kept: exit=%d stderr=%s", code, stderr)
				}
			} else if code != 0 || stderr != "" {
				t.Errorf("exit=%d stderr=%s", code, stderr)
			}
		})
	}
}

// TestRuntime199WireOwner fails a with lifetime that bypasses its owner:
// only the with and its builder emit the with scope's calls, and only the
// owner reads the checker's ownership fact.
func TestRuntime199WireOwner(t *testing.T) {
	writers := map[string]bool{"with": true, "withClose": true, "blocking.go:package": true}
	readers := map[string]bool{"ownsResources": true, "acquiredSet": true, "buildProviders": true, "withsOf": true}
	i1LowerSource(t, func(owner string, node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind != token.STRING {
					break
				}
				value, err := strconv.Unquote(x.Value)
				if err != nil {
					break
				}
				for _, name := range []string{"OpenWith", "WithClose", "WithAcquire", "EndWith"} {
					if strings.Contains(value, name) && !writers[owner] {
						t.Errorf("%s writes the with scope's %s outside the with lifetime owner", owner, name)
					}
				}
			case *ast.SelectorExpr:
				if x.Sel.Name == "Acquired" && !readers[owner] {
					t.Errorf("%s reads the checker's ownership fact outside the owner", owner)
				}
			}
			return true
		})
	})
}
