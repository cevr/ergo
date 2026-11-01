package lower

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os/exec"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// #199 stage 1 (runtime.md, I1): a frame whose footprint is not empty
// checks for a stop before each capability call and at the head of each
// loop body. Recursive or opaque impure frames also poll at entry, so
// recursive back-edges pass a check; a pure frame has none. The oracle is
// the source's shape, never
// Info.Footprints: each clause below states whether its frame is pure.

// i1Clause is one footprint clause: the statements that head a frame's
// body, and what they make of its footprint.
type i1Clause struct {
	name   string
	prefix string
	needs  bool   // the frame needs Probe
	param  string // a parameter the clause reads
	impure bool
	// site is the capability call the frame may make inside its loops,
	// "" when the clause gives it no Probe to call.
	site string
	// provider: admitted in an operation's implementation, whose
	// signature has no row and no free parameter.
	provider bool
}

var i1Clauses = []i1Clause{
	{name: "pure", provider: true},
	{name: "pure-call", prefix: "pureHelper()\n", provider: true},
	{name: "recursive-pure", prefix: "let _ = even(3)\n", provider: true},
	{name: "capability", prefix: "Probe.Tick()\n", needs: true, impure: true, site: "Probe.Mark()\n"},
	{name: "transitive", prefix: "helper()\n", needs: true, impure: true, site: "Probe.Mark()\n"},
	{name: "recursive", prefix: "ping(2)\n", needs: true, impure: true, site: "Probe.Mark()\n"},
	{name: "own-with", prefix: "with Local {\nProbe.Tick()\n}\n", impure: true, site: "with Local {\nProbe.Mark()\n}\n", provider: true},
	{name: "unknown-value", prefix: "cb()\n", param: "cb func()", impure: true},
	{name: "host", prefix: "let xs = []int{}\nlet _ = xs.Append(1)\n", impure: true, provider: true},
	{name: "task-start", prefix: "let t0 = task.Go() { 1 }\nt0.Stop()\n", impure: true, provider: true},
}

// i1Loop is one loop form: what precedes it, its head and the statements
// that open its body, for nesting depth d.
type i1Loop struct {
	name  string
	parts func(d int) (pre, head, open string)
	param string // a parameter the loop reads
	seq   bool   // iterating calls a Seq, a function value: the frame is opaque
}

var i1Loops = []i1Loop{
	{name: "range", parts: func(d int) (string, string, string) {
		return "", fmt.Sprintf("for x%d in 0..3 {", d), fmt.Sprintf("let _ = x%d\n", d)
	}},
	{name: "slice", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let xs%d = []int{0, 1, 2}\n", d), fmt.Sprintf("for x%d in xs%d {", d, d), fmt.Sprintf("let _ = x%d\n", d)
	}},
	{name: "map", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let m%d = Map[string, int]{\"a\": 0, \"b\": 1}\n", d), fmt.Sprintf("for x%d in m%d {", d, d), fmt.Sprintf("let _ = x%d\n", d)
	}},
	{name: "seq", param: "s Seq[int]", seq: true, parts: func(d int) (string, string, string) {
		return "", fmt.Sprintf("for x%d in s {", d), fmt.Sprintf("let _ = x%d\n", d)
	}},
	{name: "cond", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let mut x%d = 0\n", d), fmt.Sprintf("for x%d < 3 {", d), fmt.Sprintf("x%d += 1\n", d)
	}},
	{name: "emitting-cond", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let mut x%d = 0\n", d), fmt.Sprintf("for below(x%d) {", d), fmt.Sprintf("x%d += 1\n", d)
	}},
	{name: "infinite", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let mut x%d = 0\n", d), "for {", fmt.Sprintf("if x%d == 3 {\nbreak\n}\nx%d += 1\n", d, d)
	}},
}

// i1Variant is what the innermost loop body does besides counting.
type i1Variant struct {
	name                          string
	cont, brk, fail, site, nested bool
}

var i1Variants = []i1Variant{
	{name: "plain"},
	{name: "continue", cont: true},
	{name: "break", brk: true},
	{name: "failure", fail: true},
	{name: "site", site: true, cont: true},
}

const i1Defs = `import task
type Probe {
	Tick(),
	Mark(),
}
type Silent {}
type Fault {}
func Tick(_ Silent) {}
func Mark(_ Silent) {}
let Local = wire { Probe: Silent }
func helper() needs Probe { Probe.Tick() }
func pureHelper() {}
func below(n int) bool { n < 3 }
func guard(n int) fails Fault {
	if n > 100 {
		return Fault
	}
}
func even(n int) bool {
	if n == 0 {
		return true
	}
	odd(n - 1)
}
func odd(n int) bool {
	if n == 0 {
		return false
	}
	even(n - 1)
}
func ping(n int) needs Probe {
	if n > 0 {
		pong(n - 1)
	}
}
func pong(n int) needs Probe {
	Probe.Tick()
	ping(n)
}
func started() {
	let t = task.Go() { 1 }
	t.Stop()
}
`

// i1Loops builds the loops of one frame: the given forms from the outside
// in, the innermost body doing v. It declares and counts sum.
func i1Body(prefix string, loops []i1Loop, v i1Variant, site string) string {
	last := len(loops) - 1
	inner := "sum += 1\n"
	if v.brk {
		inner = fmt.Sprintf("if x%d == 2 {\nbreak\n}\n", last) + inner
	}
	if v.cont {
		inner = fmt.Sprintf("if x%d == 1 {\ncontinue\n}\n", last) + inner
	}
	if v.fail {
		inner = fmt.Sprintf("try guard(x%d)\n", last) + inner
	}
	if v.site {
		inner = site + inner
	}
	for d := last; d >= 0; d-- {
		pre, head, open := loops[d].parts(d)
		inner = pre + head + "\n" + open + inner + "}\n"
	}
	return "let mut sum = 0\n" + prefix + inner
}

// i1Case is one generated program and, for the Go frames that hold its
// loops, whether each must poll: "decl" is a Go function, "lit" a closure.
type i1Case struct {
	name        string
	src         string
	want        map[string]bool
	site        bool            // the program calls Probe.Mark in a polling frame
	entries     map[string]bool // named Go frames whose entry rule is known from the source
	markerEntry *bool           // entry rule for the source frame declaring entryMarker
}

// i1Cases generates every frame class × clause × loop form × variant the
// rules admit. A class that cannot hold a clause or a form skips it: a
// provider method has no row and no free parameter.
func i1Cases() []i1Case {
	var out []i1Case
	shapes := [][]i1Loop{}
	for i, l := range i1Loops {
		shapes = append(shapes, []i1Loop{l}, []i1Loop{l, i1Loops[(i+1)%len(i1Loops)]})
	}
	for _, cl := range i1Clauses {
		for _, shape := range shapes {
			seq := false
			var params []string
			if cl.param != "" {
				params = append(params, cl.param)
			}
			var names []string
			for _, l := range shape {
				names = append(names, l.name)
				if l.seq && !seq {
					seq = true
					params = append(params, l.param)
				}
			}
			impure := cl.impure || seq
			for _, v := range i1Variants {
				if v.site && cl.site == "" {
					continue
				}
				body := i1Body(cl.prefix, shape, v, cl.site)
				row, try := "", ""
				if v.fail {
					row, try = " fails Fault", "try "
				}
				if cl.needs {
					row += " needs Probe"
				}
				ps := strings.Join(params, ", ")
				name := fmt.Sprintf("%s/%s/%s", cl.name, strings.Join(names, "+"), v.name)
				add := func(class, src string, want map[string]bool) {
					out = append(out, i1Case{name: class + "/" + name, src: i1Defs + src + "func main() {}\n", want: want, site: v.site})
				}
				add("decl", "func subject("+ps+") int"+row+" {\n"+body+"sum\n}\n", map[string]bool{"decl": impure})
				add("literal", "func subject("+ps+") int"+row+" {\nlet local = func() int"+row+" {\n"+body+"sum\n}\n"+try+"local()\n}\n", map[string]bool{"lit": impure})
				add("task", "func subject("+ps+") int"+row+" {\nlet t = task.Go() {\n"+body+"sum\n}\n"+try+"t.Wait()\n}\n", map[string]bool{"lit": impure})
				if cl.provider && !seq && !v.fail {
					add("provider", "type Worker {\n\tRun(n int) int,\n}\ntype Impl {}\nfunc Run(_ Impl, n int) int {\n"+body+"sum\n}\nlet Work = wire { Worker: Impl }\nfunc subject() int {\nwith Work {\nWorker.Run(1)\n}\n}\n", map[string]bool{"decl": impure})
				}
				if cl.name == "pure" && !seq && !v.fail && !v.site {
					// Nested frames of opposite footprints: an impure
					// function's pure literal, and a pure function that
					// makes an impure literal and never runs it.
					add("impure-parent", "func subject() int needs Probe {\nProbe.Tick()\nlet local = func() int {\n"+body+"sum\n}\n"+body+"sum + local()\n}\n", map[string]bool{"decl": true, "lit": false})
					add("pure-parent", "func subject() int {\nlet _ = func() int needs Probe {\nProbe.Tick()\n"+body+"sum\n}\n"+body+"sum\n}\n", map[string]bool{"decl": false, "lit": true})
				}
			}
		}
	}
	return out
}

func TestRuntime199I1Property(t *testing.T) {
	// Ordinary trailing blocks and with bodies use the reviewer's grid.
	cases := append(append(i1Cases(), i1BlockCases()...), i1RecursionCases()...)
	cases = append(cases, i1EntryCases()...)
	classes := map[string]int{}
	for _, c := range cases {
		classes[strings.SplitN(c.name, "/", 2)[0]]++
	}
	for _, class := range []string{"decl", "literal", "task", "provider", "impure-parent", "pure-parent", "trailing", "with-body", "recursion"} {
		if classes[class] == 0 {
			t.Errorf("no generated case of the frame class %s", class)
		}
	}
	t.Logf("%d generated programs: %v", len(cases), classes)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := mustLower(t, "i1", c.src)
			i1CheckFacts(t, o)
			i1CheckPolls(t, o, c)
		})
	}
	t.Run("runtime-primitives", i1CheckPrimitives)
	t.Run("live", i1Live)
}

// Adapted from the round 1 reviewer's zz_ext_test.go: block frames, generated
// over the same clauses, loop forms and exits as i1Cases.
//   - trailing: the block of a call of a program function (a BlockStmt
//     frame that is not a task body);
//   - with-body: the loops run inside a with block, in the enclosing frame.
func i1BlockCases() []i1Case {
	var out []i1Case
	shapes := [][]i1Loop{}
	for i, l := range i1Loops {
		shapes = append(shapes, []i1Loop{l}, []i1Loop{l, i1Loops[(i+1)%len(i1Loops)]})
	}
	for _, cl := range i1Clauses {
		for _, shape := range shapes {
			seq := false
			var params []string
			if cl.param != "" {
				params = append(params, cl.param)
			}
			var names []string
			for _, l := range shape {
				names = append(names, l.name)
				if l.seq && !seq {
					seq = true
					params = append(params, l.param)
				}
			}
			impure := cl.impure || seq
			for _, v := range i1Variants {
				if v.site && cl.site == "" {
					continue
				}
				body := i1Body(cl.prefix, shape, v, cl.site)
				row, try := "", ""
				if v.fail {
					row, try = " fails Fault", "try "
				}
				if cl.needs {
					row += " needs Probe"
				}
				ps := strings.Join(params, ", ")
				name := fmt.Sprintf("%s/%s/%s", cl.name, strings.Join(names, "+"), v.name)
				add := func(class, src string, want map[string]bool) {
					out = append(out, i1Case{name: class + "/" + name, src: i1Defs + src + "func main() {}\n", want: want, site: v.site})
				}
				add("trailing", "func run(f func() int"+row+") int"+row+" {\n"+try+"f()\n}\nfunc subject("+ps+") int"+row+" {\n"+try+"run() {\n"+body+"sum\n}\n}\n", map[string]bool{"lit": impure})
				add("with-body", "func subject("+ps+") int"+row+" {\nwith Local {\n"+body+"sum\n}\n}\n", map[string]bool{"decl": impure})
			}
		}
	}
	return out
}

// i1EntryCases separates entry placement from loop/capability polls. The
// marker is the first source statement, so a later capability checkpoint
// cannot stand in for an entry poll. These frames need no owned scope.
func i1EntryCases() []i1Case {
	var out []i1Case
	for _, effect := range []string{"pure", "capability", "opaque"} {
		body := "let entryMarker = 0\nlet _ = entryMarker\n"
		switch effect {
		case "capability":
			body += "with Local { Probe.Tick() }\n"
		case "opaque":
			body += "let xs = []int{}\nlet _ = xs.Append(1)\n"
		}
		frames := map[string]string{
			"decl":     "func subject() {\n" + body + "}\n",
			"literal":  "func subject() {\nlet local = func() {\n" + body + "}\nlocal()\n}\n",
			"task":     "func subject() {\nlet t = task.Go() {\n" + body + "1\n}\nlet _ = t.Wait()\n}\n",
			"provider": "type Worker { Run(), }\ntype Impl {}\nfunc Run(_ Impl) {\n" + body + "}\nlet Work = wire { Worker: Impl }\nfunc subject() { with Work { Worker.Run() } }\n",
			"trailing": "func run(cb func()) { cb() }\nfunc subject() { run() {\n" + body + "} }\n",
		}
		for class, src := range frames {
			want := effect == "opaque"
			out = append(out, i1Case{name: "entry-" + class + "/" + effect, src: i1Defs + src + "func main() {}\n", markerEntry: &want})
		}
	}
	return out
}

// i1RecursionCases varies the call graph independently of the footprint:
// a pure recursive SCC stays pure; a capability on an unexecuted branch
// still makes recursion impure; an opaque value can re-enter its caller.
// EntryPoll is not inherited by a nonrecursive, transparent caller.
func i1RecursionCases() []i1Case {
	var out []i1Case
	for _, graph := range []string{"self", "mutual", "value-reentry"} {
		for _, effect := range []string{"pure", "capability", "opaque"} {
			row, clause := "", ""
			switch effect {
			case "capability":
				row, clause = " needs Probe", "if n < 0 { Probe.Tick() }\n"
			case "opaque":
				clause = "if n < 0 { let xs = []int{}; let _ = xs.Append(1) }\n"
			}
			call := "left(n - 1)"
			if graph == "mutual" {
				call = "right(n - 1)"
			}
			if graph == "value-reentry" {
				call = "let next = left\nnext(n - 1)"
			}
			src := "func left(n int) int" + row + " {\n" + clause + "if n < 2 { return n }\n" + call + "\n}\n"
			want := effect != "pure" || graph == "value-reentry"
			entries := map[string]bool{"left": want, "subject": effect == "opaque" || graph == "value-reentry"}
			if graph == "mutual" {
				src += "func right(n int) int" + row + " { left(n) }\n"
				entries["right"] = want
			}
			src += "func subject() int" + row + " { left(3) }\nfunc main() {}\n"
			out = append(out, i1Case{name: "recursion/" + graph + "/" + effect, src: i1Defs + src, entries: entries})
		}
	}
	return out
}

// i1CheckFacts walks the checked tree: every frame key is a known frame
// class, and every loop's frame has a footprint the lowering reads, so a
// newly admitted frame form fails here first.
func i1CheckFacts(t *testing.T, o *lowered) {
	t.Helper()
	info := o.pkg.Info
	for key := range info.Footprints {
		switch key.(type) {
		case *syntax.FuncDecl, *syntax.FuncLit, *syntax.BlockStmt, *syntax.WireDecl, *syntax.VarSpec:
		default:
			t.Errorf("a footprint keyed by an unknown frame form %T", key)
		}
	}
	var in func(n, frame syntax.Node)
	in = func(n, frame syntax.Node) {
		syntax.Inspect(n, func(m syntax.Node) bool {
			switch x := m.(type) {
			case *syntax.FuncLit:
				if x != frame {
					in(x, x)
					return false
				}
			case *syntax.CallExpr:
				if c := info.Calls[x]; x.Trailing != nil && (c == nil || c.Kind != check.CallWith) {
					in(x.Fun, frame)
					for _, a := range x.Args {
						in(a, frame)
					}
					in(x.Trailing, x.Trailing)
					return false
				}
			case *syntax.ForStmt, *syntax.RangeStmt:
				if _, ok := info.Footprints[frame]; !ok {
					t.Errorf("a loop at %d in a frame %T with no footprint", m.Pos(), frame)
				}
			}
			return true
		})
	}
	for _, d := range o.file.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			in(d, d)
		case *syntax.WireDecl:
			in(d, d)
		case *syntax.VarDecl:
			for _, s := range d.Specs {
				in(s, s)
			}
		}
	}
}

// i1CheckPolls reads the emitted Go: each loop's frame is the innermost Go
// function or closure around it; an impure frame's loop body starts with
// the check of the frame's own task, a pure frame's has none, and the Go
// loops are exactly the source's (no loop is lost and none is generated).
// Each capability call Mark follows the check of the task it is passed.
func i1CheckPolls(t *testing.T, o *lowered, c i1Case) {
	t.Helper()
	code := o.mainGo()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", code, 0)
	if err != nil {
		t.Fatal(err)
	}
	source := 0
	syntax.Inspect(o.file, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.ForStmt, *syntax.RangeStmt:
			source++
		}
		return true
	})
	goLoops, marks := 0, 0
	entries := 0
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if want, ok := c.entries[fd.Name.Name]; ok {
			entries++
			got := len(fd.Body.List) > 0 && isOwnCheck(fd.Body.List[0], i1EnvParam(fd.Type))
			if got != want {
				t.Errorf("%s entry poll = %v, want %v\nGo:\n%s", fd.Name.Name, got, want, stripDirectives(code))
			}
		}
	}
	if entries != len(c.entries) {
		t.Errorf("found %d of %d expected entry frames", entries, len(c.entries))
	}
	var stack []ast.Node
	marked := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		var env *ast.Ident
		for i := len(stack) - 1; i >= 0; i-- {
			if fd, ok := stack[i].(*ast.FuncDecl); ok {
				env = i1EnvParam(fd.Type)
				break
			}
			if fl, ok := stack[i].(*ast.FuncLit); ok {
				env = i1EnvParam(fl.Type)
				break
			}
		}
		var body *ast.BlockStmt
		switch n := n.(type) {
		case *ast.FuncDecl:
			if c.markerEntry != nil && i1HasEntryMarker(n.Body) {
				marked++
				i1CheckEntry(t, n.Body, env, *c.markerEntry)
			}
		case *ast.FuncLit:
			if c.markerEntry != nil && i1HasEntryMarker(n.Body) {
				marked++
				i1CheckEntry(t, n.Body, env, *c.markerEntry)
			}
		case *ast.ExprStmt:
			// Check every point kind, including entries and capability
			// sites. Resolve with's plain Env copies back to this frame's
			// parameter; a captured parent parameter has a different object.
			if isCheckOf(n, "") && !isOwnCheck(n, env) {
				t.Errorf("poll %s does not check its frame's own env parameter\nGo:\n%s", types.ExprString(stmtCall(n, "Check")), stripDirectives(code))
			}
		case *ast.ForStmt:
			body = n.Body
		case *ast.RangeStmt:
			body = n.Body
		case *ast.BlockStmt:
			for i, s := range n.List {
				// A call site passes its frame's task; the conversion's
				// adapter method, which passes an Env, is no call site.
				if call := stmtCall(s, "Mark"); call != nil && len(call.Args) == 1 && isTaskOf(call.Args[0]) {
					marks++
					if !isFrameTask(call.Args[0], env) || i == 0 || !isOwnCheck(n.List[i-1], env) {
						t.Errorf("the capability call %s does not follow its own frame's Check\nGo:\n%s", types.ExprString(call), stripDirectives(code))
					}
				}
			}
		}
		if body == nil {
			return true
		}
		goLoops++
		class := "decl"
		for i := len(stack) - 2; i >= 0; i-- {
			if _, ok := stack[i].(*ast.FuncLit); ok {
				class = "lit"
				break
			}
			if _, ok := stack[i].(*ast.FuncDecl); ok {
				break
			}
		}
		want, ok := c.want[class]
		if !ok {
			t.Errorf("a Go loop in a %s frame, which holds no source loop\nGo:\n%s", class, stripDirectives(code))
			return true
		}
		polls := 0
		ast.Inspect(body, func(m ast.Node) bool {
			switch m.(type) {
			case *ast.FuncLit, *ast.ForStmt, *ast.RangeStmt:
				return false
			case *ast.ExprStmt:
				if isCheckOf(m.(ast.Stmt), "") {
					polls++
				}
			}
			return true
		})
		switch {
		case want && (len(body.List) == 0 || !isOwnCheck(body.List[0], env)):
			t.Errorf("an impure %s frame's loop body does not start with its task's Check\nGo:\n%s", class, stripDirectives(code))
		case !want && polls > 0:
			t.Errorf("a pure %s frame's loop body has %d checks\nGo:\n%s", class, polls, stripDirectives(code))
		}
		return true
	})
	if c.markerEntry != nil && marked != 1 {
		t.Errorf("found %d marked entry frames, want 1", marked)
	}
	if goLoops != source {
		t.Errorf("the Go has %d loops for %d source loops\nGo:\n%s", goLoops, source, stripDirectives(code))
	}
	if c.site && marks == 0 {
		t.Errorf("no capability call Mark in the Go")
	}
}

func i1HasEntryMarker(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	for _, s := range body.List {
		if d, ok := s.(*ast.DeclStmt); ok {
			if g, ok := d.Decl.(*ast.GenDecl); ok {
				for _, spec := range g.Specs {
					if v, ok := spec.(*ast.ValueSpec); ok {
						for _, id := range v.Names {
							if id.Name == "entryMarker" {
								return true
							}
						}
					}
				}
			}
		}
	}
	return false
}

func i1CheckEntry(t *testing.T, body *ast.BlockStmt, env *ast.Ident, want bool) {
	t.Helper()
	got := len(body.List) > 0 && isOwnCheck(body.List[0], env)
	if got != want {
		t.Errorf("marked frame entry poll = %v, want %v", got, want)
	}
}

// stmtCall is the call of the method name that s makes as a statement.
func stmtCall(s ast.Stmt, name string) *ast.CallExpr {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return nil
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return nil
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
		return call
	}
	return nil
}

// isCheckOf reports the statement task.Check(), of the task expression
// task when it is not "".
func isCheckOf(s ast.Stmt, task string) bool {
	call := stmtCall(s, "Check")
	if call == nil || len(call.Args) != 0 {
		return false
	}
	recv := call.Fun.(*ast.SelectorExpr).X
	return isTaskOf(recv) && (task == "" || types.ExprString(recv) == task)
}

// isTaskOf reports an environment's task, ENV.task.
func isTaskOf(x ast.Expr) bool {
	sel, ok := x.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == envTask
}

func i1EnvParam(ft *ast.FuncType) *ast.Ident {
	for _, p := range ft.Params.List {
		ptr, ok := p.Type.(*ast.StarExpr)
		if !ok || len(p.Names) != 1 {
			continue
		}
		if name, ok := ptr.X.(*ast.Ident); ok && name.Name == "Env" {
			return p.Names[0]
		}
	}
	return nil
}

func isOwnCheck(s ast.Stmt, env *ast.Ident) bool {
	call := stmtCall(s, "Check")
	return call != nil && len(call.Args) == 0 && isFrameTask(call.Fun.(*ast.SelectorExpr).X, env)
}

// isFrameTask compares binding identities, not spelling. A with region
// copies its frame's Env, without changing the task. Only those local
// plain copies may resolve back to the innermost Env parameter.
func isFrameTask(x ast.Expr, env *ast.Ident) bool {
	if env == nil || env.Obj == nil || !isTaskOf(x) {
		return false
	}
	x = x.(*ast.SelectorExpr).X
	seen := map[*ast.Object]bool{}
	for {
		switch n := x.(type) {
		case *ast.ParenExpr:
			x = n.X
		case *ast.UnaryExpr:
			if n.Op != token.AND {
				return false
			}
			x = n.X
		case *ast.Ident:
			if n.Obj == env.Obj {
				return true
			}
			if n.Obj == nil || seen[n.Obj] {
				return false
			}
			seen[n.Obj] = true
			decl, ok := n.Obj.Decl.(*ast.AssignStmt)
			if !ok || decl.Tok != token.DEFINE || len(decl.Lhs) != 1 || len(decl.Rhs) != 1 {
				return false
			}
			copy, ok := decl.Rhs[0].(*ast.StarExpr)
			if !ok {
				return false
			}
			x = copy.X
		default:
			return false
		}
	}
}

// i1CheckPrimitives: the runtime's blocking primitives keep their own
// interruption points, whatever their caller polls (runtime.md, I1).
func i1CheckPrimitives(t *testing.T) {
	var src []byte
	for _, f := range Runtime() {
		if strings.HasSuffix(f.Path, "/queue.go") {
			src = f.Data
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "queue.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil {
			continue
		}
		switch fd.Name.Name {
		case "Push", "Pop", "Values":
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Check" {
						found[fd.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	for _, name := range []string{"Push", "Pop", "Values"} {
		if !found[name] {
			t.Errorf("the queue's %s has no interruption point of its own", name)
		}
	}
}

// The live witnesses: a task runs an unbounded loop or recursion in an
// impure frame of each class, its owner stops it, and the program ends.
// Without the frame's poll the loop never ends (a hang) and the recursion
// overflows the stack. The continue witness takes every back-edge through
// a continue; the order witness shows a capability call's operands run
// once, in source order, before the operation.

const i1LiveDefs = `import queue
import task
type Probe {
	Tick(),
}
type Silent {}
func Tick(_ Silent) {}
let Local = wire { Probe: Silent }
func nonnegative(n int) bool { n >= 0 }
`

const i1LiveMain = `func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let t = task.Go() { work(ready) }
	try ready.Pop()
	let mut delay = 0
	for delay < 20000000 {
		delay += 1
	}
	t.Stop()
	Console.Println("stopped")
}
`

// The frame's clause follows its loop, so the loop's own poll is the only
// interruption point the frame reaches once it runs; a counting loop in
// main before the stop lets the task enter its loop first, so a missing
// poll hangs instead of losing a race to an earlier one.
var i1LiveLoops = map[string]string{
	"infinite": "let mut c = 0\nfor {\nc += 1\n}\n",
	"range":    "let mut c = 0\nfor i in 0..1000000000000 {\nc += i\n}\n",
	"continue": "let mut c = 0\nfor i in 0..1000000000000 {\nif i >= 0 {\ncontinue\n}\nc += i\n}\n",
	"cond":     "let mut c = 0\nfor c >= 0 {\nc += 1\n}\n",
	// Adapted from the reviewer's cap-in-cond probe in zz_review_test.go.
	// The condition helper is pure so it cannot hide a missing loop poll.
	"emitting-cond": "let mut c = 0\nfor nonnegative(c) {\nc += 1\n}\n",
}

func i1LivePrograms() map[string]string {
	out := map[string]string{}
	// Adapted from the reviewer's child-direct witness: stopping the child
	// must work while the parent remains live (M-d polls the wrong task).
	out["child-direct"] = i1LiveDefs + `func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let t = task.Go() {
		try ready.Push(1)
		let mut c = 0
		for { c += 1 }
		with Local { Probe.Tick() }
		c
	}
	try ready.Pop()
	let mut delay = 0
	for delay < 20000000 { delay += 1 }
	t.Stop()
	Console.Println("stopped")
}
`
	work := func(decls, call string) string {
		return i1LiveDefs + decls + "func work(ready queue.Queue[int]) int fails queue.Closed {\n\ttry ready.Push(1)\n" + call + "}\n" + i1LiveMain
	}
	for kind, loop := range i1LiveLoops {
		out["decl-"+kind] = work("func spin() int {\n"+loop+"let xs = []int{}\nlet _ = xs.Append(1)\nc\n}\n", "spin()\n")
		out["literal-"+kind] = work("", "let local = func() int {\n"+loop+"with Local {\nProbe.Tick()\n}\nc\n}\nlocal()\n")
		out["task-"+kind] = work("", "let u = task.Go() {\n"+loop+"let t0 = task.Go() { 1 }\nt0.Stop()\nc\n}\nu.Wait()\n")
		out["provider-"+kind] = work("type Worker {\n\tRun(n int) int,\n}\ntype Impl {}\nfunc Run(_ Impl, n int) int {\n"+loop+"with Local {\nProbe.Tick()\n}\nc\n}\nlet Work = wire { Worker: Impl }\n", "with Work {\nWorker.Run(1)\n}\n")
	}
	out["capability-recursion"] = work("func spinCap() needs Probe {\nProbe.Tick()\nspinCap()\n}\n", "with Local {\nspinCap()\n}\n0\n")
	out["capability-mutual"] = work("func ping() needs Probe {\npong()\n}\nfunc pong() needs Probe {\nProbe.Tick()\nping()\n}\n", "with Local {\nping()\n}\n0\n")
	// Adapted from the round 1 reviewer's zz_ext_test.go. Its recursive
	// path never calls Probe: only an entry poll can stop fib's work.
	out["impure-recursion"] = work(`func fib(n int) int needs Probe {
	if n < 0 { Probe.Tick() }
	if n < 2 { return n }
	fib(n - 1) + fib(n - 2)
}
`, "with Local { fib(90) }\n")
	// No capability is reached until after the unbounded loop. Its
	// footprint must travel leaf -> bridge -> spin across two SCC edges.
	out["transitive-components"] = work(`func leaf() needs Probe { Probe.Tick() }
func bridge() needs Probe { leaf() }
func spin() int needs Probe {
	let mut c = 0
	for { c += 1 }
	bridge()
	c
}
`, "with Local { spin() }\n")
	out["order"] = `type Pair {
	Join(a string, b string) string,
}
type Glue {}
func Join(_ Glue, a string, b string) string { a + b }
let Joined = wire { Pair: Glue }
func step(s string) string needs Console {
	Console.Println(s)
	s
}
func main() needs Console {
	with Joined {
		Console.Println(Pair.Join(step("a"), step("b")))
	}
}
`
	return out
}

func i1Live(t *testing.T) {
	progs := i1LivePrograms()
	var lowered []*lowered
	names := map[string]string{}
	i := 0
	for name, src := range progs {
		pkg := fmt.Sprintf("live%d", i)
		i++
		names[name] = pkg
		lowered = append(lowered, mustLower(t, pkg, src))
	}
	exes := buildAll(t, lowered, true)
	if exes == nil {
		return
	}
	for name, pkg := range names {
		want := "stopped\n"
		if name == "order" {
			want = "a\nb\nab\n"
		}
		exe := exes[pkg]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, exe).CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("the program did not end within 60s: the stop never reached the frame\n%s", progs[name])
			}
			if err != nil || string(out) != want {
				t.Fatalf("run: %v\n%s\nwant %q\n%s", err, firstLines(string(out), 20), want, progs[name])
			}
		})
	}
}
