package lower

import (
	"context"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Context values through with (runtime-199 stage 4a; runtime.md, G1).
// TestRuntime199ContextValueProperty generates functions whose bodies nest
// context regions (log.Annotate, trace.Span and a user type that satisfies
// the protocol), shadow one key with another region of it, mix them with
// context withs whose providers' Close reads the context, and start tasks
// before, inside and after a binding. Each task is a delayed child (it
// waits on a queue until the parent releases it, at a generated later
// point, maybe inside a later binding or after its own region ended) or an
// immediate one, and the parent joins each through a second queue, so two
// tasks pending at once are siblings with bindings opened between their
// starts. Some operands are evaluated through a step that can fail.
//
// The model takes each with's kind from the checker's fact (With.Value
// for a context binding, With.Acquired for a context's Close), never from the
// generator alone. It runs every function natively once normally and once
// for every parent step and every exit at that step: a typed failure, a
// defect and a stop, at a read in a region or while an operand is
// evaluated. Each run checks:
//
//   - every read, in the parent and in each child, sees exactly the
//     innermost binding of each key in force, a child the bindings in
//     force when it started, after the parent's region ended or while a
//     later one is open, and its own binding inside its own region only;
//   - each context's Close reads the regions around the with, not inside it;
//   - the caller's values are restored after every exit (a v1 test seam
//     binds an outer value around the call and reads it after);
//   - every stored value is a string, and no snapshot a read saw stays
//     reachable after the run's tasks end (a weak pointer to each, through
//     an rt test seam), so retention is bounded by the task lifecycle;
//   - a context read is effectful in the checker's footprints.

// The statements of a generated body.
const (
	cxRead    = "read"    // the parent reads the context at a step
	cxCtx     = "ctx"     // with of a context value
	cxContext = "context" // with of a named context of one provider
	cxFork    = "fork"    // task.Go of a child that reads the context
	cxRelease = "release" // the parent releases a delayed child
	cxJoin    = "join"    // the parent waits for a child's reads
)

type cxNode struct {
	kind string
	id   int
	key  string // a ctx node's slot: a or b (log.Annotate), s (trace.Span), u (the user type tag)
	op   bool   // a ctx node's operand runs the parent step id first
	res  bool   // a context node's provider has a Close
	kids []*cxNode
	fork *cxNode // a release or join node's child; the child a ctx node's block yields

	delayed, bind bool   // a fork: waits for its release; binds b itself
	released      bool   // generation state
	slot          string // the slot the checker's fact binds (cxFacts, cxSlot)
}

type cxSeq struct {
	k        int
	body     []*cxNode
	ids      int
	forks    []*cxNode
	contexts []*cxNode
	pends    []*[]*cxNode // forks not yet joined, by the scope that owns them
	withs    []*cxNode    // the withs in source order; a fork's own binding is the fork
}

func (s *cxSeq) next() int { s.ids++; return s.ids }

func genCxSeq(r *rand.Rand, k int) *cxSeq {
	for {
		s := &cxSeq{k: k}
		s.body = s.block(r, 0)
		if len(s.forks) > 0 && s.ids >= 4 {
			return s
		}
	}
}

func (s *cxSeq) newFork(r *rand.Rand) *cxNode {
	f := &cxNode{kind: cxFork, id: s.next(), delayed: r.IntN(2) == 0, bind: r.IntN(2) == 0}
	s.forks = append(s.forks, f)
	return f
}

// block generates one to three statements, the releases and joins it
// settles between them, and at its end the joins of the children whose
// task it names. A task is a local, so it is released and joined in the
// block that names it or one nested there. A with of a context value can
// yield a task its block starts last: `let t = with v { …; task.Go() {…} }`,
// so its child starts inside the region and is named after it ends.
func (s *cxSeq) block(r *rand.Rand, depth int) []*cxNode {
	var out []*cxNode
	mine := []*cxNode{}
	s.pends = append(s.pends, &mine)
	n := 1 + r.IntN(3)
	for range n {
		out = append(out, s.settle(r)...)
		var x *cxNode
		switch c := r.IntN(10); {
		case c < 3 || depth >= 3:
			x = &cxNode{kind: cxRead, id: s.next()}
		case c < 6:
			keys := []string{"a", "a", "b", "s", "u"}
			x = &cxNode{kind: cxCtx, id: s.next(), key: keys[r.IntN(len(keys))], op: r.IntN(3) == 0}
			x.kids = s.block(r, depth+1)
			if r.IntN(2) == 0 {
				x.fork = s.newFork(r)
				mine = append(mine, x.fork)
			}
		case c < 8:
			// A context with may own the tasks its block starts (resources.md,
			// Context ownership); they are named only inside it, so they are
			// joined there.
			x = &cxNode{kind: cxContext, id: s.next(), res: r.IntN(3) != 0}
			s.contexts = append(s.contexts, x)
			x.kids = s.block(r, depth+1)
		default:
			x = s.newFork(r)
			mine = append(mine, x)
		}
		out = append(out, x)
	}
	out = append(out, s.settle(r)...)
	out = append(out, s.flush(&mine)...)
	s.pends = s.pends[:len(s.pends)-1]
	return out
}

// settle moves some pending children one step: a delayed child is
// released, a released or immediate one is joined.
func (s *cxSeq) settle(r *rand.Rand) []*cxNode {
	var out []*cxNode
	for _, p := range s.pends {
		kept := (*p)[:0]
		for _, f := range *p {
			switch {
			case r.IntN(3) != 0:
				kept = append(kept, f)
			case f.delayed && !f.released:
				f.released = true
				out = append(out, &cxNode{kind: cxRelease, fork: f})
				kept = append(kept, f)
			default:
				out = append(out, &cxNode{kind: cxJoin, fork: f})
			}
		}
		*p = kept
	}
	return out
}

// flush releases and joins every child still pending in p.
func (s *cxSeq) flush(p *[]*cxNode) []*cxNode {
	var out []*cxNode
	for _, f := range *p {
		if f.delayed && !f.released {
			f.released = true
			out = append(out, &cxNode{kind: cxRelease, fork: f})
		}
		out = append(out, &cxNode{kind: cxJoin, fork: f})
	}
	*p = nil
	return out
}

func (s *cxSeq) fn() string { return fmt.Sprintf("run%d", s.k) }

const contextValuePrelude = `import log
import queue
import task
import trace
import example.test/userctx

type Probe { Step(line string) bool }
type Fault {}
func get(o Option[string]) string {
	match o {
	case Some[string] as v:
		v.Value
	case None:
		"-"
	}
}
func show() string {
	"a=" + get(log.Annotation("a")) + " b=" + get(log.Annotation("b")) + " s=" + get(trace.Current())
}
func step(line string) fails Fault needs Probe {
	if Probe.Step(line) { return Fault }
}
func opnd(line string, text string) string fails Fault needs Probe {
	try step(line)
	text
}
`

func (s *cxSeq) src() string {
	var b strings.Builder
	for _, w := range s.contexts {
		c, t := fmt.Sprintf("C%d_%d", s.k, w.id), fmt.Sprintf("T%d_%d", s.k, w.id)
		fmt.Fprintf(&b, "type %s { Name() string }\ntype %s {}\nfunc Name(_ %s) string { %q }\n", c, t, t, t)
		if w.res {
			fmt.Fprintf(&b, "func Close(_ %s) needs Probe { let _ = Probe.Step(\"w%d \" + show()) }\n", t, w.id)
		}
		fmt.Fprintf(&b, "let W%d_%d = context { %s: %s }\n", s.k, w.id, c, t)
	}
	row := "Fault"
	var queues strings.Builder
	for _, f := range s.forks {
		if f.delayed {
			row = "Fault | queue.Closed"
			fmt.Fprintf(&queues, "\tlet g%d = queue.New[int](0)\n", f.id)
		}
	}
	fmt.Fprintf(&b, "func %s() fails %s needs Probe {\n%s", s.fn(), row, queues.String())
	s.withs = nil
	s.write(&b, s.body, "\t")
	b.WriteString("}\n")
	return b.String()
}

func (s *cxSeq) write(b *strings.Builder, nodes []*cxNode, in string) {
	for _, x := range nodes {
		switch x.kind {
		case cxRead:
			fmt.Fprintf(b, "%stry step(\"p%d \" + show())\n", in, x.id)
		case cxCtx:
			text := fmt.Sprintf("%q", fmt.Sprintf("v%d", x.id))
			if x.op {
				text = fmt.Sprintf("try opnd(\"p%d \" + show(), %s)", x.id, text)
			}
			operand := map[string]string{
				"a": `log.Annotate("a", ` + text + `)`,
				"b": `log.Annotate("b", ` + text + `)`,
				"s": `trace.Span(` + text + `)`,
				"u": `userctx.Make(` + text + `)`,
			}[x.key]
			s.withs = append(s.withs, x)
			head := ""
			if x.fork != nil {
				head = fmt.Sprintf("let t%d = ", x.fork.id)
			}
			fmt.Fprintf(b, "%s%swith %s {\n", in, head, operand)
			s.write(b, x.kids, in+"\t")
			if x.fork != nil {
				s.task(b, x.fork, in+"\t")
			}
			fmt.Fprintf(b, "%s}\n", in)
		case cxContext:
			s.withs = append(s.withs, x)
			fmt.Fprintf(b, "%swith W%d_%d {\n%s\tlet _ = C%d_%d.Name()\n", in, s.k, x.id, in, s.k, x.id)
			s.write(b, x.kids, in+"\t")
			fmt.Fprintf(b, "%s}\n", in)
		case cxFork:
			fmt.Fprintf(b, "%slet t%d = ", in, x.id)
			s.task(b, x, in)
		case cxRelease:
			fmt.Fprintf(b, "%stry g%d.Push(1)\n", in, x.fork.id)
		case cxJoin:
			try := ""
			if x.fork.delayed {
				try = "try "
			}
			fmt.Fprintf(b, "%s%st%d.Wait()\n", in, try, x.fork.id)
		}
	}
}

// task writes the task.Go of the child x, from its first token.
func (s *cxSeq) task(b *strings.Builder, x *cxNode, in string) {
	if !strings.HasSuffix(b.String(), " = ") {
		b.WriteString(in)
	}
	b.WriteString("task.Go() {\n")
	if x.delayed {
		fmt.Fprintf(b, "%s\ttry g%d.Pop()\n", in, x.id)
	}
	fmt.Fprintf(b, "%s\tlet _ = Probe.Step(\"c%d \" + show())\n", in, x.id)
	if x.bind {
		s.withs = append(s.withs, x)
		fmt.Fprintf(b, "%s\twith log.Annotate(\"b\", \"k%d\") {\n%s\t\tlet _ = Probe.Step(\"c%di \" + show())\n%s\t}\n%s\tlet _ = Probe.Step(\"c%do \" + show())\n", in, x.id, in, x.id, in, in, x.id)
	}
	fmt.Fprintf(b, "%s}\n", in)
}

// cxFacts maps each generated with of s to the checker's fact for it, in
// source order, and checks the fact's kind: a context binding of the
// generated type, or a context whose Close the fact records as the model's.
func cxFacts(t *testing.T, o *lowered, s *cxSeq) map[*cxNode]*check.With {
	t.Helper()
	var facts []*check.With
	for _, decl := range o.file.Decls {
		fd, ok := decl.(*syntax.FuncDecl)
		if !ok || fd.Name.Name != s.fn() || fd.Body == nil {
			continue
		}
		syntax.Inspect(fd.Body, func(n syntax.Node) bool {
			if x, ok := n.(*syntax.CallExpr); ok {
				if w := o.pkg.Info.Withs[x]; w != nil {
					facts = append(facts, w)
				}
			}
			return true
		})
	}
	if len(facts) != len(s.withs) {
		t.Fatalf("%s: %d withs have facts, the generator wrote %d", s.fn(), len(facts), len(s.withs))
	}
	ctors := cxCtors(t, o)
	out := map[*cxNode]*check.With{}
	for i, x := range s.withs {
		w := facts[i]
		out[x] = w
		switch x.kind {
		case cxContext:
			closes := cxCloses(w)
			if w.Value != nil || len(w.Contexts) != 1 || closes != x.res {
				t.Errorf("%s: with W%d_%d: fact context %v contexts %d closes %v, model closes %v", s.fn(), s.k, x.id, w.Value != nil, len(w.Contexts), closes, x.res)
			}
		default:
			if w.Value == nil {
				t.Errorf("%s: the with of %s (id %d) is not a context binding in the fact", s.fn(), x.key, x.id)
				continue
			}
			generated := x.key
			if x.kind == cxFork {
				generated = "b"
			}
			x.slot = cxSlot(t, w, generated, ctors)
		}
	}
	return out
}

// cxCtors are the result types of the constructors the generator writes
// for each slot, read from the signatures of the functions the program
// calls: log.Annotate for a and b, trace.Span for s, userctx.Make for u.
func cxCtors(t *testing.T, o *lowered) map[string]check.Type {
	t.Helper()
	byName := map[string]check.Type{}
	for _, call := range o.pkg.Info.Calls {
		if call != nil && call.Func != nil && call.Func.Pkg != nil && call.Func.Sig != nil && len(call.Func.Sig.Results) == 1 {
			byName[call.Func.Pkg.Path+"."+call.Func.Name] = call.Func.Sig.Results[0]
		}
	}
	out := map[string]check.Type{}
	for slot, name := range map[string]string{"a": "log.Annotate", "b": "log.Annotate", "s": "trace.Span", "u": "example.test/userctx.Make"} {
		if out[slot] = byName[name]; out[slot] == nil {
			t.Fatalf("the program calls no %s", name)
		}
	}
	return out
}

// cxCloses reports a with whose fact acquires a provider with a Close.
func cxCloses(w *check.With) bool {
	for _, a := range w.Acquired {
		if a.Close != nil {
			return true
		}
	}
	return false
}

// cxSlot is the slot a context binding fills, from the checker's fact: the
// binding's type is the result type of the constructor generated for the
// slot (an annotation's key is its operand's, a or b), and its key function
// is declared beside that type, so the key is private to its package. No
// type name is read (runtime-199 stage 4a r1 m3).
func cxSlot(t *testing.T, w *check.With, generated string, ctors map[string]check.Type) string {
	t.Helper()
	cb := w.Value
	n, ok := cb.Type.(*check.Named)
	if want := ctors[generated]; want == nil || cb.Type != want || !ok || cb.Key == nil || cb.Key.Pkg != n.Obj.Pkg {
		t.Fatalf("a context binding of type %v (key %v), generated as slot %s of %v", cb.Type, cb.Key, generated, ctors[generated])
	}
	return generated
}

type cxBind struct{ slot, text string }

func cxShow(stack []cxBind) string {
	var parts []string
	for _, slot := range []string{"a", "b", "s", "u"} {
		v := "-"
		for _, b := range stack {
			if b.slot == slot {
				v = b.text
			}
		}
		parts = append(parts, slot+"="+v)
	}
	return strings.Join(parts, " ")
}

// cxCase is one native run of a generated function: the exit at the
// parent step target, the parent's lines in order, and the children's
// lines, required, allowed or (otherwise) forbidden.
type cxCase struct {
	name, mode, target, exit string
	k                        int
	parent, req, opt         []string
}

// cxSim runs the model of s with the exit mode at step target (0: none).
type cxSim struct {
	t        *testing.T
	s        *cxSeq
	facts    map[*cxNode]*check.With
	target   int
	mode     string
	stack    []cxBind
	parent   []string
	children map[int][]string // a child's lines, from its start's snapshot
	started  map[int]bool
	released map[int]bool
	joined   map[int]bool
}

func (m *cxSim) step(id int) bool {
	m.parent = append(m.parent, fmt.Sprintf("p%d %s", id, cxShow(m.stack)))
	return id == m.target && m.mode != acqNone
}

// run runs nodes and reports an exit that leaves them.
func (m *cxSim) run(nodes []*cxNode) bool {
	for _, x := range nodes {
		switch x.kind {
		case cxRead:
			if m.step(x.id) {
				return true
			}
		case cxCtx:
			if x.op && m.step(x.id) {
				return true // the operand failed: nothing is bound
			}
			n := len(m.stack)
			m.stack = append(m.stack, cxBind{x.slot, fmt.Sprintf("v%d", x.id)})
			exited := m.run(x.kids)
			if !exited && x.fork != nil {
				m.run([]*cxNode{x.fork}) // the task the block yields starts in the region
			}
			m.stack = m.stack[:n]
			if exited {
				return true
			}
		case cxContext:
			exited := m.run(x.kids)
			if cxCloses(m.facts[x]) {
				m.parent = append(m.parent, fmt.Sprintf("w%d %s", x.id, cxShow(m.stack)))
			}
			if exited {
				return true
			}
		case cxFork:
			snap := slices.Clone(m.stack)
			lines := []string{fmt.Sprintf("c%d %s", x.id, cxShow(snap))}
			if x.bind {
				inner := append(slices.Clone(snap), cxBind{x.slot, fmt.Sprintf("k%d", x.id)})
				lines = append(lines, fmt.Sprintf("c%di %s", x.id, cxShow(inner)), fmt.Sprintf("c%do %s", x.id, cxShow(snap)))
			}
			m.children[x.id] = lines
			m.started[x.id] = true
		case cxRelease:
			m.released[x.fork.id] = true
		case cxJoin:
			m.joined[x.fork.id] = true
		}
	}
	return false
}

func (s *cxSeq) expect(t *testing.T, facts map[*cxNode]*check.With, mode string, target int) cxCase {
	m := &cxSim{t: t, s: s, facts: facts, target: target, mode: mode, stack: []cxBind{{"a", "outer"}},
		children: map[int][]string{}, started: map[int]bool{}, released: map[int]bool{}, joined: map[int]bool{}}
	m.run(s.body)
	c := cxCase{name: fmt.Sprintf("%d/%s%d", s.k, mode, target), mode: mode, target: fmt.Sprintf("p%d", target), k: s.k, exit: "ok", parent: m.parent}
	switch mode {
	case acqFail:
		c.parent = append(c.parent, "failure")
	case acqDefect:
		c.exit = "defect"
	case acqStop:
		c.exit = "interrupted"
	}
	for _, f := range s.forks {
		switch {
		case m.joined[f.id]:
			c.req = append(c.req, m.children[f.id]...)
		case m.started[f.id] && (!f.delayed || m.released[f.id]):
			// Started and free to run, but not joined before the exit: the
			// frame stops it, before or after its reads.
			c.opt = append(c.opt, m.children[f.id]...)
		}
	}
	return c
}

// steps are the parent steps of nodes, in source order.
func cxSteps(nodes []*cxNode) []int {
	var out []int
	for _, x := range nodes {
		if x.kind == cxRead || x.kind == cxCtx && x.op {
			out = append(out, x.id)
		}
		out = append(out, cxSteps(x.kids)...)
	}
	return out
}

func TestRuntime199ContextLexicalOperand(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import log
type A { Use() }
type Quiet {}
func Use(_ Quiet) {}
let W = context { A: Quiet }
func label() string {
	match log.Annotation("k") {
	case Some[string] as v: v.Value
	case None: "none"
	}
}
func shadow() string {
	let W = log.Annotate("k", "local")
	let first = with W { label() }
	let V = W
	let second = with V { label() }
	first + "/" + second
}
type Tag { text string }
func contextKey(_ Tag) string { "k" }
func contextText(t Tag) string { t.text }
func parameter(W Tag) string { with ((W)) { "parameter" } }
func main() needs Console {
	Console.Println(shadow())
	Console.Println(parameter(Tag{text: "parameter"}))
}
`, "local/local\nparameter\n")
}

// Neither a current std key nor its old spelling can forge a std value.
func TestRuntime199ContextPackageKeys(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import log
import trace
type tag { key string, text string }
func contextKey(t tag) string { t.key }
func contextText(t tag) string { t.text }
func span() string {
	match trace.Current() {
	case Some[string] as v: v.Value
	case None: "none"
	}
}
func label() string {
	match log.Annotation("k") {
	case Some[string] as v: v.Value
	case None: "none"
	}
}
func main() needs Console {
	with trace.Span("real") {
		with (tag{key: "span", text: "forged"}) { Console.Println(span()) }
		with (tag{key: "trace.span", text: "forged"}) { Console.Println(span()) }
	}
	with log.Annotate("k", "real-log") {
		with (tag{key: "k", text: "forged"}) { Console.Println(label()) }
		with (tag{key: "log.k", text: "forged"}) { Console.Println(label()) }
	}
}
`, "real\nreal\nreal-log\nreal-log\n")
}

// cxImporter serves the units of the test's imported packages by path, and
// std otherwise; the lowering takes the same units (Unit.Imports).
type cxImporter map[string]Unit

func (p cxImporter) Import(path string) (*check.Package, error) {
	if u, ok := p[path]; ok {
		return u.Package, nil
	}
	return check.Std.Import(path)
}

// units are the served units, in path order.
func (p cxImporter) units() []Unit {
	paths := make([]string, 0, len(p))
	for path := range p {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	out := make([]Unit, 0, len(paths))
	for _, path := range paths {
		out = append(out, p[path])
	}
	return out
}

func cxPackage(t *testing.T, path, src string) Unit {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check(path, []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatalf("%s: %v", path, ds)
	}
	return Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: "/ergo-test/" + path + "/lib.ergo", Text: src}}}
}

func cxLower(t *testing.T, name, src string, imports cxImporter) *lowered {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, imports)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: src}}, Imports: imports.units()}
	code, err := lowerPackage(u, false)
	if err != nil {
		t.Fatal(err)
	}
	return &lowered{name: name, src: src, pkg: pkg, file: f, files: []File{{Path: "main.go", Data: code}}}
}

// The checker Importer is the existing multi-package seam. Identical
// source in two packages binds its own key: a binding of one package's
// type is never the other's, even when main opens their regions. This also
// exercises a region inside an imported body.
//
// Only std binds the runtime (check-v1 E4, C26), and no user read API is
// specified (runtime.md, stdlib.md), so the bindings are observed from the
// Go side: the test's Console provider reads both packages' keys through
// the runtime at each line the program prints.
func TestRuntime199ContextPackageRead(t *testing.T) {
	needsBuild(t)
	const library = `type Tag { text string }
func New(text string) Tag { Tag{text: text} }
func contextKey(_ Tag) string { "shared" }
func contextText(t Tag) string { t.text }
func Own() needs Console { with New("own") { Console.Println("own") } }
`
	imports := cxImporter{}
	for _, path := range []string{"example.test/a", "example.test/b"} {
		imports[path] = cxPackage(t, path, library)
	}
	const src = `import example.test/a
import example.test/b
func reads() needs Console {
	a.Own()
	b.Own()
	with a.New("a") {
		Console.Println("a")
		with b.New("b") { Console.Println("a b") }
		Console.Println("a")
	}
	Console.Println("none")
}
func main() {}
`
	o := cxLower(t, "package-read", src, imports)
	contextModuleTest(t, o, packageReadTest)
}

// packageReadTest runs reads with a Console that appends, to each line,
// the binding of the key "shared" in package a and in package b.
const packageReadTest = `package main
import (
 "strings"
 "testing"
 rt "ergo.local/prototype/runtime/rt"
 ergo "ergo.local/prototype/runtime/v1"
)
type console struct{ lines []string }
func read(t *rt.Task, pkg ergo.ContextPackage) string {
 if ergo.ContextBound(t, pkg, "shared") { return ergo.ContextValue(t, pkg, "shared") }
 return "none"
}
func (c *console) Println(t *rt.Task, line string) {
 c.lines = append(c.lines, line+" "+read(t, "example.test/a")+"/"+read(t, "example.test/b"))
}
func TestPackageRead(t *testing.T) {
 c := &console{}
 if err := ergo.ContextRun(func(task *rt.Task) error { reads(&Env{task: task, Console: c}); return nil }); err != nil {
  t.Fatal(err)
 }
 want := "own own/none\nown none/own\na a/none\na b a/b\na a/none\nnone none/none"
 if got := strings.Join(c.lines, "\n"); got != want {
  t.Fatalf("package reads:\n%s\nwant:\n%s", got, want)
 }
}
`

func TestRuntime199ContextValueProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a generated native program")
	}
	r := rand.New(rand.NewPCG(199, 7))
	var seqs []*cxSeq
	var src strings.Builder
	src.WriteString(contextValuePrelude)
	for k := 0; k < 24; k++ {
		s := genCxSeq(r, k)
		seqs = append(seqs, s)
		src.WriteString(s.src())
	}
	src.WriteString("func main() {}\n")
	// The user type binds in its own private namespace. Only std binds the
	// runtime (check-v1 E4, C26) and no user read API is specified, so the
	// probe reads the user key from the Go side at each step and appends it
	// to the line (contextNativeTest, probe.Step): the line is the one an
	// Ergo read in show would print. show reads log and trace and retains
	// empty rows, so its footprint assertion still discriminates a context
	// read marked pure.
	const user = `type tag { text string }
func Make(text string) tag { tag{text: text} }
func contextKey(_ tag) string { "u" }
func contextText(t tag) string { t.text }
`
	o := cxLower(t, "context", src.String(), cxImporter{"example.test/userctx": cxPackage(t, "example.test/userctx", user)})
	// A context read is effectful for footprints, though its rows are empty.
	for _, decl := range o.file.Decls {
		if fd, ok := decl.(*syntax.FuncDecl); ok && (fd.Name.Name == "show" || fd.Name.Name == "get") {
			fp := o.pkg.Info.Footprints[fd]
			if fd.Name.Name == "show" && fp.Empty() {
				t.Error("show reads the context and its footprint is pure")
			}
			if fd.Name.Name == "get" && !fp.Empty() {
				t.Error("get reads no context and its footprint is not pure")
			}
		}
	}
	shapes := map[string]bool{}
	var cases []cxCase
	for _, s := range seqs {
		facts := cxFacts(t, o, s)
		cases = append(cases, s.expect(t, facts, acqNone, 0))
		for _, id := range cxSteps(s.body) {
			for _, mode := range []string{acqFail, acqDefect, acqStop} {
				cases = append(cases, s.expect(t, facts, mode, id))
			}
		}
		cxShapes(s, shapes)
	}
	for _, c := range cases {
		shapes["required child"] = shapes["required child"] || len(c.req) > 0
		shapes["optional child"] = shapes["optional child"] || len(c.opt) > 0
	}
	want := []string{"shadow", "span", "user tag", "operand step", "context in context", "context in context",
		"closing context", "fork inside, read after", "fork before, read inside", "siblings", "child binds",
		"immediate child", "required child", "optional child"}
	for _, shape := range want {
		if !shapes[shape] {
			t.Fatalf("the generator reaches no %s shape", shape)
		}
	}
	contextNative(t, o, cases, len(seqs))
}

// cxShapes records the shapes s reaches, by walking its body with the
// regions open at each point.
func cxShapes(s *cxSeq, shapes map[string]bool) {
	starts := map[int][]*cxNode{} // a fork's regions at its start
	var walk func(nodes []*cxNode, regions []*cxNode, inContext bool)
	walk = func(nodes []*cxNode, regions []*cxNode, inContext bool) {
		for _, x := range nodes {
			switch x.kind {
			case cxCtx:
				for _, r := range regions {
					if r.kind == cxCtx && r.key == x.key {
						shapes["shadow"] = true
					}
				}
				shapes["span"] = shapes["span"] || x.key == "s"
				shapes["user tag"] = shapes["user tag"] || x.key == "u"
				shapes["operand step"] = shapes["operand step"] || x.op
				shapes["context in context"] = shapes["context in context"] || inContext
				inner := append(slices.Clone(regions), x)
				walk(x.kids, inner, inContext)
				if x.fork != nil {
					walk([]*cxNode{x.fork}, inner, inContext)
				}
			case cxContext:
				for _, r := range regions {
					shapes["context in context"] = shapes["context in context"] || r.kind == cxCtx
				}
				shapes["closing context"] = shapes["closing context"] || x.res
				walk(x.kids, append(slices.Clone(regions), x), true)
			case cxFork:
				// Another child started and not yet joined: siblings.
				shapes["siblings"] = shapes["siblings"] || len(starts) > 0
				starts[x.id] = slices.Clone(regions)
				shapes["child binds"] = shapes["child binds"] || x.bind
				shapes["immediate child"] = shapes["immediate child"] || !x.delayed
			case cxRelease, cxJoin:
				at, ok := starts[x.fork.id]
				if !ok {
					break
				}
				if x.kind == cxRelease || !x.fork.delayed {
					for _, r := range at {
						if r.kind == cxCtx && !slices.Contains(regions, r) {
							shapes["fork inside, read after"] = true
						}
					}
					for _, r := range regions {
						if r.kind == cxCtx && !slices.Contains(at, r) {
							shapes["fork before, read inside"] = true
						}
					}
				}
				if x.kind == cxJoin {
					delete(starts, x.fork.id)
				}
			}
		}
	}
	walk(s.body, nil, false)
}

// contextNative runs the cases against the emitted program and a copy of
// the runtime with two test seams: rt.ContextProbe, which returns a weak
// pointer to a task's current values and whether each is a string, and v1's
// ContextBind, which binds a caller's value around a run. The emitted code
// is not changed.
func contextNative(t *testing.T, o *lowered, cases []cxCase, funcs int) {
	t.Helper()
	var table strings.Builder
	for _, c := range cases {
		fmt.Fprintf(&table, "\t{name: %q, mode: %q, target: %q, exit: %q, parent: %#v, req: %#v, opt: %#v, run: func(env *Env, p *probe) error {\n\t\tif err := run%d(env); err != nil {\n\t\t\tif _, ok := err.(Fault); ok { p.add(\"failure\"); return nil }\n\t\t\treturn err\n\t\t}\n\t\treturn nil\n\t}},\n",
			c.name, c.mode, c.target, c.exit, c.parent, c.req, c.opt, c.k)
	}
	contextModuleTest(t, o, strings.Replace(contextNativeTest, "CASES", table.String(), 1))
	t.Logf("%d native cases: every parent step under a failure, a defect and a stop, and a normal run of each of %d functions; native PASS", len(cases), funcs)
}

// contextModuleTest writes the emitted program, a copy of the runtime with
// the context test seams (contextProbeRT, contextProbeV1) and test as one
// module, and runs its go test. The emitted code is not changed.
func contextModuleTest(t *testing.T, o *lowered, test string) {
	t.Helper()
	fastTierGuard(t, "a go command")
	dir := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		path = filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	write("main.go", []byte(o.mainGo()))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	write("ergo/runtime/rt/context_probe.go", []byte(contextProbeRT))
	write("ergo/runtime/v1/context_probe.go", []byte(contextProbeV1))
	write("context_test.go", []byte(test))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout=3m", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native %s: %v\n%s\nErgo:\n%s", o.name, err, firstLines(string(output), 120), o.src)
	}
}

const contextProbeRT = `package rt

import "weak"

// ContextProbe returns whether t's current values stay reachable, as a weak
// pointer, and whether every stored value is a string.
func ContextProbe(t *Task) (alive func() bool, bound, strings bool) {
	strings = true
	for v := t.values; v != nil; v = v.next {
		if _, ok := v.value.(string); !ok {
			strings = false
		}
	}
	if t.values == nil {
		return func() bool { return false }, false, strings
	}
	w := weak.Make(t.values)
	return func() bool { return w.Value() != nil }, true, strings
}
`

const contextProbeV1 = `package v1

import "ergo.local/prototype/runtime/rt"

func ContextRun(body func(*rt.Task) error) error { return runMain(body) }

// ContextBind binds a caller's value around a run and returns its end.
func ContextBind(t *rt.Task, key, text string) func() {
	c := t.WithValue(contextKey{"std:log", key}, text)
	return c.End
}
`

const contextNativeTest = `package main
import (
 "fmt"
 "runtime"
 "slices"
 "strings"
 "sync"
 "testing"
 rt "ergo.local/prototype/runtime/rt"
 ergo "ergo.local/prototype/runtime/v1"
)
type probe struct {
 mu sync.Mutex
 lines []string
 alive []func() bool
 bound int
 nonString []string
 mode, target string
}
func (p *probe) add(line string) { p.mu.Lock(); p.lines = append(p.lines, line); p.mu.Unlock() }
// user reads the user type's key from the Go side: the program has no read
// for it (only std binds the runtime).
func user(t *rt.Task) string {
 if ergo.ContextBound(t, "example.test/userctx", "u") { return ergo.ContextValue(t, "example.test/userctx", "u") }
 return "-"
}
// Step is the probe capability. It appends the user key's binding in force
// for the calling task to the line. At the target parent step it fails,
// panics or stops the calling task, by mode; the stop is observed here.
func (p *probe) Step(t *rt.Task, line string) bool {
 line += " u=" + user(t)
 alive, bound, strs := rt.ContextProbe(t)
 p.mu.Lock()
 p.lines = append(p.lines, line)
 p.alive = append(p.alive, alive)
 if bound { p.bound++ }
 if !strs { p.nonString = append(p.nonString, line) }
 p.mu.Unlock()
 if strings.Fields(line)[0] != p.target { return false }
 switch p.mode {
 case "fail": return true
 case "defect": panic("defect " + p.target)
 case "stop": t.Interrupt(); t.Check()
 }
 return false
}
func values(t *rt.Task) string {
 var parts []string
 for _, k := range []string{"a", "b"} {
  v := "-"
  if ergo.ContextBound(t, "std:log", k) { v = ergo.ContextValue(t, "std:log", k) }
  parts = append(parts, k+"="+v)
 }
 parts = append(parts, "u="+user(t))
 if ergo.ContextBound(t, "std:trace", "span") { parts = append(parts, "s="+ergo.ContextValue(t, "std:trace", "span")) }
 return strings.Join(parts, " ")
}
type cxCase struct {
 name, mode, target, exit string
 parent, req, opt []string
 run func(*Env, *probe) error
}
var cases = []cxCase{
CASES}
func classify(err error, target string) string {
 switch {
 case err == nil: return "ok"
 case err == rt.Interrupted: return "interrupted"
 case strings.Contains(err.Error(), "defect "+target): return "defect"
 }
 return "unexpected: " + err.Error()
}
func TestContext(t *testing.T) {
 alive := 0
 var leaked []string
 for _, c := range cases {
  t.Run(c.name, func(t *testing.T) {
   p := &probe{mode: c.mode, target: c.target}
   var after, restored string
   err := ergo.ContextRun(func(task *rt.Task) error {
    end := ergo.ContextBind(task, "a", "outer")
    defer func() { restored = values(task) }()
    defer func() { after = values(task); end() }()
    return c.run(&Env{task: task, Probe: p}, p)
   })
   if got := classify(err, c.target); got != c.exit {
    t.Errorf("exit %s, want %s", got, c.exit)
   }
   if after != "a=outer b=- u=-" || restored != "a=- b=- u=-" {
    t.Errorf("the caller's values after the run: %q, after its own end: %q", after, restored)
   }
   var parent, children []string
   for _, l := range p.lines {
    if strings.HasPrefix(l, "c") { children = append(children, l) } else { parent = append(parent, l) }
   }
   if got, want := strings.Join(parent, "\n"), strings.Join(c.parent, "\n"); got != want {
    t.Errorf("parent:\n%s\nwant:\n%s", got, want)
   }
   seen := map[string]bool{}
   for _, l := range children {
    if seen[l] { t.Errorf("child line %q twice", l) }
    seen[l] = true
    if !slices.Contains(c.req, l) && !slices.Contains(c.opt, l) { t.Errorf("child line %q, want one of %q or %q", l, c.req, c.opt) }
   }
   for _, l := range c.req {
    if !seen[l] { t.Errorf("missing child line %q (got %q)", l, children) }
   }
   if len(p.nonString) > 0 { t.Errorf("non-string context values at %v", p.nonString) }
   if p.bound == 0 { t.Error("no read saw a bound value: the retention check is vacuous") }
   runtime.GC(); runtime.GC()
   for i, a := range p.alive {
    if a() { leaked = append(leaked, fmt.Sprintf("%s: snapshot of %q", c.name, p.lines[i])) }
   }
   alive += len(p.alive)
  })
 }
 if len(leaked) > 0 { t.Errorf("%d of %d snapshots outlive the run's tasks: %v", len(leaked), alive, leaked[:min(len(leaked), 10)]) }
}
`

// TestRuntime199ContextValueOwner fails a context region that bypasses its
// owner. The lowering emits Scope.OpenContext and Context.End only from
// contextWith, and the protocol functions are called only by contextCall.
// In the runtime, rt.Task.WithValue is called only by v1's OpenContext, a
// context region joins a scope's region list only there, and rt keeps a
// task's values only in its immutable list. Owners are qualified by their
// receiver ("Scope.OpenContext", not a bare "End"), names resolve through
// go/types, and the lowering's text is read as constant values, so a split
// or named constant cannot hide an emitted name (runtime-199 stage 4a r1
// m4).
func TestRuntime199ContextValueOwner(t *testing.T) {
	emits := map[string]int{}
	lowering := typedPackage(t, ".")
	lowering.owners(func(owner string, decl ast.Decl) {
		constStrings(lowering, decl, func(value string) {
			for _, name := range []string{"OpenContext", "Context.End"} {
				if !strings.Contains(value, name) {
					continue
				}
				switch owner {
				case "blocking.go:package":
					// the catalog keys
				case "funcCtx.contextWith":
					emits[name]++
				default:
					t.Errorf("%s emits %s outside the context owner", owner, name)
				}
			}
			for _, name := range []string{"WithValue", "ContextBind"} {
				if strings.Contains(value, name) {
					t.Errorf("%s emits %s, a context binding outside OpenContext", owner, name)
				}
			}
		})
	})
	for _, name := range []string{"OpenContext", "Context.End"} {
		if emits[name] != 1 {
			t.Errorf("the context owner emits %s %d times, want 1", name, emits[name])
		}
	}
	// The runtime: a use of rt's (*Task).WithValue (a call or a method
	// value) and a write of rt's Task.values or v1's Scope.withs, each
	// resolved to its declaration.
	// rt's own check names its package "rt"; v1 imports it by path.
	withValue := func(f *types.Func) bool {
		sig, _ := f.Type().(*types.Signature)
		if f.Name() != "WithValue" || f.Pkg() == nil || sig == nil || sig.Recv() == nil {
			return false
		}
		path := f.Pkg().Path()
		return (path == "rt" || strings.HasSuffix(path, "/runtime/rt")) && strings.HasSuffix(types.TypeString(sig.Recv().Type(), nil), "rt.Task")
	}
	uses := map[string]bool{}
	for _, pkg := range []string{"rt", "v1"} {
		src := typedPackage(t, filepath.Join("..", "..", "..", "runtime", pkg))
		var field types.Object
		var writers []string
		switch pkg {
		case "rt":
			field = src.method(t, "Task", "values")
			writers = []string{"rt.Task.init", "rt.Task.WithValue", "rt.Context.End", "rt.Task.detach"}
		case "v1":
			field = src.method(t, "Scope", "withs")
			writers = []string{"v1.Scope.OpenWith", "v1.Scope.OpenContext", "v1.Scope.EndWith", "v1.Context.End", "v1.Scope.close"}
		}
		src.owners(func(owner string, decl ast.Decl) {
			owner = pkg + "." + owner
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Ident:
					if f, ok := src.info.Uses[n].(*types.Func); ok && withValue(f) {
						uses[owner] = true
					}
				case *ast.AssignStmt:
					for _, l := range n.Lhs {
						sel, ok := ast.Unparen(l).(*ast.SelectorExpr)
						if ok && src.info.Uses[sel.Sel] == field && !slices.Contains(writers, owner) {
							t.Errorf("%s writes %s outside %v", owner, field.Name(), writers)
						}
					}
				}
				return true
			})
		})
	}
	if got := sortedKeys(uses); !slices.Equal(got, []string{"v1.Scope.OpenContext"}) {
		t.Errorf("the runtime binds context values in %v, want only v1.Scope.OpenContext", got)
	}
}

// constStrings visits the maximal constant string expressions under n, by
// their go/types values: a literal, a concatenation of constants, a named
// constant. A concatenation that is not constant as a whole is visited as
// its constant operands joined, the others left out, so a name split
// around a variable (scope + ".Open" + "Context()") is still read; its
// other operands are then searched in turn.
func constStrings(src *typedSource, n ast.Node, visit func(string)) {
	str := func(e ast.Expr) (string, bool) {
		if tv, ok := src.info.Types[e]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
			return constant.StringVal(tv.Value), true
		}
		return "", false
	}
	ast.Inspect(n, func(n ast.Node) bool {
		e, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		if s, ok := str(e); ok {
			visit(s)
			return false
		}
		if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
			var parts []string
			var rest []ast.Expr
			var flat func(x ast.Expr)
			flat = func(x ast.Expr) {
				if s, ok := str(x); ok {
					parts = append(parts, s)
				} else if b, ok := ast.Unparen(x).(*ast.BinaryExpr); ok && b.Op == token.ADD {
					flat(b.X)
					flat(b.Y)
				} else {
					rest = append(rest, x)
				}
			}
			flat(b)
			if len(parts) > 0 {
				visit(strings.Join(parts, ""))
			}
			for _, x := range rest {
				constStrings(src, x, visit)
			}
			return false
		}
		return true
	})
}
