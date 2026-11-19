package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/facts"
	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

// graphProgram is a generated module "gm" of packages p0..p(n-1) and the
// root cmd/gm. Package i imports a set of earlier packages and uses each
// in F<i> (a call) and Wrap<i> (a record built and opened in the
// imported package); it declares K<i>, Box<i>, Open<i>, a test T<i> and a
// law L<i> about F<i>, and Box<i> and K<i> sit in a second file when
// split[i]. One package (ops) holds the operation type Counter with its
// law, the provider Tally (NewTally, Add) and the named context Live, so
// its test build runs Counter.AddsSome for Tally and under Live. tweak
// names the declarations whose tokens are changed; comments and layout
// change no token.
type graphProgram struct {
	n       int
	imports [][]int
	split   []bool
	ops     int
	tweak   map[string]bool // "<pkg index>.<decl name>"
	comment bool
	layout  bool
}

// graphTweaks are the declarations an edit may change the tokens of in
// any package; graphOpsTweaks those of the ops package too.
var graphTweaks = []string{"F", "Open", "K", "T", "L", "Wrap"}
var graphOpsTweaks = []string{"NewTally", "Add", "Live"}

func newGraphProgram(r *rand.Rand) graphProgram {
	p := graphProgram{n: 2 + r.IntN(3), tweak: map[string]bool{}}
	p.imports = make([][]int, p.n+1)
	p.split = make([]bool, p.n+1)
	for i := range p.n + 1 {
		for j := range i {
			if r.IntN(2) == 0 {
				p.imports[i] = append(p.imports[i], j)
			}
		}
		p.split[i] = r.IntN(2) == 0
	}
	// The root imports every package no other imports, so the program
	// reaches each.
	for j := range p.n {
		imported := false
		for i := j + 1; i < p.n; i++ {
			imported = imported || slices.Contains(p.imports[i], j)
		}
		if !imported && !slices.Contains(p.imports[p.n], j) {
			p.imports[p.n] = append(p.imports[p.n], j)
		}
	}
	slices.Sort(p.imports[p.n])
	p.ops = r.IntN(p.n + 1)
	return p
}

func (p graphProgram) pkgPath(i int) string {
	if i == p.n {
		return "gm/cmd/gm"
	}
	return "gm/p" + strconv.Itoa(i)
}

func (p graphProgram) pkgDir(i int) string {
	return strings.TrimPrefix(p.pkgPath(i), "gm/")
}

// decls are package i's declarations by file name, in order.
func (p graphProgram) files(i int) map[string][]string {
	tw := func(name string) bool { return p.tweak[strconv.Itoa(i)+"."+name] }
	s := strconv.Itoa(i)
	var imports []string
	f := "x + " + s
	wrap := "n"
	for _, j := range p.imports[i] {
		js := strconv.Itoa(j)
		imports = append(imports, "import "+p.pkgPath(j))
		f += " + p" + js + ".F" + js + "(x)"
		wrap += " + p" + js + ".Open" + js + "(p" + js + ".Box" + js + "{V: n})"
	}
	if tw("F") {
		f += " + 0"
	}
	if tw("Wrap") {
		wrap += " * 1"
	}
	main := []string{
		"func F" + s + "(x int) int { " + f + " }",
		"func Wrap" + s + "(n int) int { " + wrap + " }",
	}
	test := "assert F" + s + "(1) == F" + s + "(1)"
	if tw("T") {
		test = "assert F" + s + "(2) == F" + s + "(2)"
	}
	main = append(main, "test T"+s+" {\n\t"+test+"\n}")
	law := "F" + s + "(x) == F" + s + "(x)"
	if tw("L") {
		law = "F" + s + "(x) + 0 == F" + s + "(x)"
	}
	main = append(main, "law L"+s+"(x int) { "+law+" }")
	open := "b.V"
	if tw("Open") {
		open = "b.V + 0"
	}
	k := 3
	if tw("K") {
		k = 4
	}
	other := []string{
		fmt.Sprintf("let K%s = %d", s, k),
		"type Box" + s + " { V int }",
		"func Open" + s + "(b Box" + s + ") int { " + open + " + K" + s + " }",
	}
	if i == p.ops {
		tally, add, live := "Tally{base: 0}", "t.base + n", "NewTally()"
		if tw("NewTally") {
			tally = "Tally{base: 0 * 1}"
		}
		if tw("Add") {
			add = "n + t.base"
		}
		if tw("Live") {
			live = "(NewTally())"
		}
		other = append(other,
			"type Counter {\n\tAdd(n int) int,\n\tlaw AddsSome(n int) { Add(n) - n == 0 },\n}",
			"type Tally { base int } implements Counter",
			"func NewTally() Tally { "+tally+" }",
			"func Add(t Tally, n int) int { "+add+" }",
			"let Live = context { Counter: "+live+" }",
		)
	}
	if i == p.n {
		main = append(main, "func main() {}")
	}
	name := "p" + s
	if i == p.n {
		name = "main"
	}
	out := map[string][]string{}
	if p.split[i] {
		out[name+".ergo"] = main
		out["more.ergo"] = other
	} else {
		out[name+".ergo"] = append(main, other...)
	}
	for f, ds := range out {
		if len(imports) > 0 && f == name+".ergo" {
			out[f] = append([]string{strings.Join(imports, "\n")}, ds...)
		}
	}
	return out
}

// source is the module's files by module-relative path.
func (p graphProgram) source() map[string]string {
	out := map[string]string{"ergo.mod": "module gm\n"}
	for i := range p.n + 1 {
		for f, ds := range p.files(i) {
			ds = slices.Clone(ds)
			for k, d := range ds {
				if p.comment {
					d = "// a note\n" + strings.Replace(d, "{ ", "{ /* a note */ ", 1)
				}
				if p.layout {
					d = strings.ReplaceAll(d, " + ", "  +  ")
				}
				ds[k] = d
			}
			sep := "\n\n"
			if p.layout {
				sep = "\n\n\n\n"
			}
			out[p.pkgDir(i)+"/"+f] = strings.Join(ds, sep) + "\n"
		}
	}
	return out
}

// graphOf checks the program and returns its analysis and rows.
func graphOf(t *testing.T, p graphProgram, fail func(string, ...any)) (Analysis, *graphrows.Table) {
	t.Helper()
	dir := writeTree(t, p.source())
	a, err := CheckDir(filepath.Join(dir, "cmd", "gm"))
	if err != nil {
		fail("%v", err)
	}
	if len(a.Diagnostics) > 0 {
		fail("diagnostics: %v\n%s", a.Diagnostics, a.Text())
	}
	return a, a.Graph()
}

// fullName is a declaration node's handle: its package's path, then its
// name within the package.
func fullName(g *graphrows.Table, n uint32) string {
	name := g.Str(g.Nodes[n].Name)
	for p := g.Nodes[n].Parent; p != graphrows.NoNode; p = g.Nodes[p].Parent {
		if g.Nodes[p].Kind == graphrows.Package {
			return g.Str(g.Nodes[p].Name) + "." + name
		}
	}
	return name
}

// fileName is the module-relative name of a file node.
func fileName(g *graphrows.Table, n uint32) string { return g.Str(g.Nodes[n].Name) }

// rowsWithoutPositions is every node and edge row with no position, one
// string per row, sorted: what a comment or layout edit must not change.
func rowsWithoutPositions(g *graphrows.Table) []string {
	var out []string
	for i, n := range g.Nodes {
		out = append(out, fmt.Sprintf("n%d %s %s %d %s %s %d", i, n.Kind, g.SubName(n), n.Parent, g.Str(n.Name), g.Str(n.Digest), n.Subject))
	}
	for _, e := range g.Edges {
		out = append(out, fmt.Sprintf("e %s %s %d %d", e.Kind, e.How, e.From, e.To))
	}
	sort.Strings(out)
	return out
}

// tasks are the rows' tasks by label: key and the full names of the
// declarations of its Input rows.
type graphTask struct {
	key    string
	inputs map[string]bool
}

func graphTasks(g *graphrows.Table) map[string]graphTask {
	out := map[string]graphTask{}
	for _, n := range g.Nodes {
		if n.Kind == graphrows.Task {
			out[g.Str(n.Name)] = graphTask{key: g.Str(n.Digest), inputs: map[string]bool{}}
		}
	}
	for _, e := range g.Edges {
		if e.Kind == graphrows.Input {
			out[g.Str(g.Nodes[e.From].Name)].inputs[fullName(g, e.To)] = true
		}
	}
	return out
}

// TestGraphRowsAreTheCheckedProgram is GRAPH-TABLE's acceptance
// (CI-SELECT §12.8), a seeded property over generated modules and edits:
// (a) every import the program's files hold is an Imports row from its
// file at its path and there is no other; (b) every site of SEM's fact
// tables that names a declaration of the module, in any package, is a
// Uses row from its enclosing declaration with its kind and position,
// and every Uses row is such a site; (c) after an edit of one
// declaration's tokens, a task's key changes if and only if its Input
// rows named that declaration, and a law run's key is the verdict
// store's digest (lower.RunTarget.Digest); (d) a comment or layout edit
// changes no row but positions. In process: it reads the checker's
// result and the rows. Mutants: graph-drops-cross-package-uses (b),
// graph-input-misses-context (c), graph-rows-hold-comments (d).
func TestGraphRowsAreTheCheckedProgram(t *testing.T) {
	seeds := []uint64{}
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		n := uint64(8)
		if testing.Short() {
			n = 3
		}
		for i := range n {
			seeds = append(seeds, i+1)
		}
	}
	contextRuns, crossUses, keyChanges := 0, 0, 0
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x67a9))
		p := newGraphProgram(r)
		var edits []string
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d after %v: %s\nReplay: ERGO_SEED=%d go test -run '^TestGraphRowsAreTheCheckedProgram$' ./internal/sketchfront", seed, edits, fmt.Sprintf(format, args...), seed)
		}
		a, g := graphOf(t, p, fail)
		crossUses += graphImportsAndUses(t, p, a, g, fail)
		contextRuns += graphKeysAreTheStore(a, g, fail)
		for step := range 5 {
			// (c): one declaration's tokens; the last edit of each seed is
			// one of the ops package's (the context's in every third), so
			// a run under a context sees its declarations change.
			i := r.IntN(p.n + 1)
			names := graphTweaks
			if i == p.ops {
				names = append(slices.Clone(names), graphOpsTweaks...)
			}
			name := names[r.IntN(len(names))]
			if step == 4 {
				i, name = p.ops, graphOpsTweaks[seed%uint64(len(graphOpsTweaks))]
			}
			key := strconv.Itoa(i) + "." + name
			p.tweak[key] = !p.tweak[key]
			edits = append(edits, p.pkgPath(i)+"."+name)
			before := graphTasks(g)
			a, g = graphOf(t, p, fail)
			graphImportsAndUses(t, p, a, g, fail)
			graphKeysAreTheStore(a, g, fail)
			after := graphTasks(g)
			full := p.pkgPath(i) + "." + name
			switch {
			case name == "T":
				full += strconv.Itoa(i) + "#test"
			case name == "L":
				full += strconv.Itoa(i) + "#law"
			case !slices.Contains(graphOpsTweaks, name):
				full += strconv.Itoa(i)
			}
			for label, b := range before {
				af, ok := after[label]
				if !ok {
					fail("task %q is gone", label)
				}
				changed := af.key != b.key
				if changed {
					keyChanges++
				}
				if changed != b.inputs[full] {
					fail("task %q: key changed %v, but its Input rows name %s: %v (inputs %v)", label, changed, full, b.inputs[full], slices.Sorted(mapKeys(b.inputs)))
				}
			}
		}
		// (d): comments, then layout.
		for _, edit := range []string{"comments", "layout"} {
			before := rowsWithoutPositions(g)
			if edit == "comments" {
				p.comment = true
			} else {
				p.layout = true
			}
			edits = append(edits, edit)
			a, g = graphOf(t, p, fail)
			if after := rowsWithoutPositions(g); !slices.Equal(before, after) {
				fail("a %s edit changed rows:\n%s", edit, firstDiff(strings.Join(before, "\n"), strings.Join(after, "\n")))
			}
		}
	}
	if contextRuns == 0 || crossUses == 0 || keyChanges == 0 {
		t.Fatalf("vacuous: %d runs under a context, %d cross-package uses, %d key changes", contextRuns, crossUses, keyChanges)
	}
}

func mapKeys(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// graphImportsAndUses checks (a) and (b) and returns the cross-package
// uses it saw.
func graphImportsAndUses(t *testing.T, p graphProgram, a Analysis, g *graphrows.Table, fail func(string, ...any)) int {
	t.Helper()
	// (a): the model's imports, file by file.
	var want, got []string
	for i := range p.n + 1 {
		for f, ds := range p.files(i) {
			if len(ds) > 0 && strings.HasPrefix(ds[0], "import ") {
				for _, line := range strings.Split(ds[0], "\n") {
					want = append(want, p.pkgDir(i)+"/"+f+" -> "+strings.TrimPrefix(line, "import "))
				}
			}
		}
	}
	for i, e := range g.Edges {
		if e.Kind == graphrows.Imports {
			if at := g.EdgeAt[i]; at.File != e.From || at.Line == 0 {
				fail("import row %v at %v", e, at)
			}
			got = append(got, fileName(g, e.From)+" -> "+g.Str(g.Nodes[e.To].Name))
		}
	}
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(want, got) {
		fail("imports rows\n%s\nwant\n%s\nrows:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"), g.Text())
	}
	// (b): SEM's sites, read from the fact tables afresh.
	sites := map[string]bool{}  // every site that names a declaration
	module := map[string]bool{} // those of the module's declarations
	cross := 0
	for _, cp := range a.Packages() {
		var files []facts.File
		for _, f := range cp.Files {
			files = append(files, facts.File{Name: f.Rel, Syntax: f.Syntax})
		}
		ft, _ := facts.Build(cp.Package, files)
		handle := func(id uint32) string {
			h := ft.Symbols[id].Handle
			if ft.Path == "main" && strings.HasPrefix(h, "main.") {
				h = cp.Path + h[len("main"):]
			}
			return h
		}
		for _, s := range ft.Sites {
			sym := ft.Symbols[s.Sym]
			if s.Kind == facts.Def || s.Kind == facts.Import || s.Encl == facts.NoSym || sym.Handle == "" {
				continue
			}
			switch sym.Kind {
			case facts.Param, facts.TypeParam, facts.Local, facts.Package:
				continue
			}
			f := cp.Files[s.File]
			line, col := f.Syntax.Position(syntax.Pos(s.Off))
			row := fmt.Sprintf("%s:%d:%d %s %s -> %s", f.Rel, line, col, s.Kind, handle(s.Encl), handle(s.Sym))
			sites[row] = true
			if strings.HasPrefix(handle(s.Sym), "gm/") {
				module[row] = true
				if !strings.HasPrefix(handle(s.Sym), cp.Path+".") {
					cross++
				}
			}
		}
	}
	uses := map[string]bool{}
	for i, e := range g.Edges {
		if e.Kind != graphrows.Uses {
			continue
		}
		at := g.EdgeAt[i]
		row := fmt.Sprintf("%s:%d:%d %s %s -> %s", fileName(g, at.File), at.Line, at.Col, e.How, fullName(g, e.From), fullName(g, e.To))
		if !sites[row] {
			fail("Uses row %s is no site of the fact tables", row)
		}
		uses[row] = true
	}
	for row := range module {
		if !uses[row] {
			fail("the site %s is no Uses row", row)
		}
	}
	return cross
}

// graphKeysAreTheStore checks that each law run's task key is the verdict
// store's digest, and returns the runs under a context.
func graphKeysAreTheStore(a Analysis, g *graphrows.Table, fail func(string, ...any)) int {
	tasks := graphTasks(g)
	contexts := 0
	for _, cp := range a.Packages() {
		for _, rt := range lower.RunTargets(cp.Package) {
			got, ok := tasks[rt.Label]
			if !ok {
				fail("no task for the run %q", rt.Label)
			}
			if want := rt.Digest(cp.Package); got.key != want {
				fail("task %q key %s, the store's digest %s", rt.Label, got.key, want)
			}
			if rt.Context != nil {
				contexts++
			}
		}
		for _, test := range cp.Package.Tests {
			if _, ok := tasks["test "+test.Name]; !ok {
				fail("no task for the test %s", test.Name)
			}
		}
	}
	return contexts
}
