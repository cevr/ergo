package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/linked/code"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/facts"
	"ergo.local/prototype/internal/sketch/machinegraph"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// codeRun is `ergo code ARGS` over the checked program a, in process: its
// status, standard output and standard error.
func codeRun(t *testing.T, a sketchfront.Analysis, args ...string) (int, string, string) {
	t.Helper()
	return codeRunOn(t, codeJSON(t, a), args...)
}

// codeJSON is the tables of the program a as ergo code passes them.
func codeJSON(t *testing.T, a sketchfront.Analysis) string {
	t.Helper()
	tables, err := json.Marshal(codeTablesOf(a))
	if err != nil {
		t.Fatal(err)
	}
	return string(tables)
}

// codeRunOn is `ergo code ARGS` over the tables text tables.
func codeRunOn(t *testing.T, tables string, args ...string) (int, string, string) {
	t.Helper()
	out, err := code.Run(context.Background(), args, tables)
	if err != nil || len(out) != 3 {
		t.Fatalf("ergo code %v: %v %q", args, err, out)
	}
	status, err := strconv.Atoi(out[0])
	if err != nil {
		t.Fatal(err)
	}
	return status, out[1], out[2]
}

// TestCodeAnswersAgreeWithTheTables: on example programs, `ergo code`
// answers what the fact tables (internal/sketch/facts, held to the
// checker by TestTablesAgreeWithScan) say, through the whole path: the
// tables as JSON, std code's Index, its generated command line, and back.
// For seeded symbols of each package:
//   - `symbol HANDLE` is the declaration, its handle naming the package
//     by its path (the root package's too, which the checker names main);
//   - `references --of HANDLE` is the symbol's sites, in table order, at
//     their file, line, column and kind;
//   - `symbol SHORT` (the handle without its package) is that declaration
//     when one package declares the name, and Ambiguous with every
//     candidate when several do;
//   - `calls --of HANDLE` at depth 1 is the call sites in the declaration
//     (out) or of it (in): the sites of kind call the tables enclose in
//     it, or name it. Besides the drawn ones, each package's first
//     declaration that calls a parameter or a local (a function value) is
//     asked.
//
// Every example program, in an order the seed draws; full tier only. ERGO_CODE_SEED=N replays a seed. Mutants code-root-main,
// code-short-first, code-call-value-dropped
// (cmd/ergo/testdata/mutants.json).
func TestCodeAnswersAgreeWithTheTables(t *testing.T) {
	if testing.Short() {
		// The fast tier's CPU budget has no headroom (scripts/gate
		// fastCPUKnownGap); the full tier runs it on every batch.
		t.Skip("full tier: checks every example program")
	}
	seed := uint64(1)
	if s := os.Getenv("ERGO_CODE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	r := rand.New(rand.NewPCG(seed, 7))
	programs, err := filepath.Glob("../../examples/*/cmd/*")
	if err != nil {
		t.Fatal(err)
	}
	// The smallest repro of SEM2c's reach gaps: a context of another
	// package, a catch arm of a record pattern (testdata/code/reach).
	programs = append(programs, "testdata/code/reach/cmd/reach")
	slices.Sort(programs)
	r.Shuffle(len(programs), func(i, j int) { programs[i], programs[j] = programs[j], programs[i] })
	perPackage := 2
	for _, dir := range programs {
		// A Go binary of an example (lintpack's policy-lint) has no Ergo.
		if src, _ := filepath.Glob(filepath.Join(dir, "*.ergo")); len(src) == 0 {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatal(err)
		}
		a, err := sketchfront.CheckDir(abs)
		if err != nil {
			t.Fatal(err)
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d, %s: %s (replay: ERGO_CODE_SEED=%d go test -run TestCodeAnswersAgreeWithTheTables ./cmd/ergo)", seed, dir, fmt.Sprintf(format, args...), seed)
		}
		agreeOnProgram(t, a, r, perPackage, fail)
	}
}

func agreeOnProgram(t *testing.T, a sketchfront.Analysis, r *rand.Rand, perPackage int, fail func(string, ...any)) {
	t.Helper()
	type pkgTable struct {
		path  string
		table *facts.Table
		syn   map[string]*syntax.File
	}
	text := codeJSON(t, a)
	var tables []*facts.Table
	var pkgs []pkgTable
	for _, p := range a.Packages() {
		syn := map[string]*syntax.File{}
		var files []facts.File
		for _, f := range p.Files {
			syn[f.Rel] = f.Syntax
			files = append(files, facts.File{Name: f.Rel, Syntax: f.Syntax})
		}
		if p.Package == nil {
			fail("package %s does not check", p.Path)
		}
		tb, _ := facts.Build(p.Package, files)
		tables = append(tables, tb)
		pkgs = append(pkgs, pkgTable{p.Path, tb, syn})
	}
	// printed is a handle as ergo code prints it: the root package's path
	// for the checker's main.
	printed := func(pt pkgTable, h string) string {
		if rest, ok := strings.CutPrefix(h, "main."); ok && pt.table.Path == "main" {
			return pt.path + "." + rest
		}
		return h
	}
	// handleOf is symbol id's handle as the tables give it: printed, or
	// for a declared symbol without one, its position; "" for none.
	handleOf := func(pt pkgTable, id uint32) string {
		s := pt.table.Symbols[id]
		if s.Handle != "" {
			return printed(pt, s.Handle)
		}
		if !s.Declared {
			return ""
		}
		name := pt.table.Files[s.File]
		line, col := pt.syn[name].Position(syntax.Pos(s.Off))
		return fmt.Sprintf("%s:%d:%d", name, line, col)
	}
	// short is each short name's printed handles, over every package.
	short := map[string][]string{}
	for _, pt := range pkgs {
		for _, s := range pt.table.Symbols {
			if s.Declared && s.Handle != "" {
				h := printed(pt, s.Handle)
				name := strings.TrimPrefix(h, pt.path+".")
				short[name] = append(short[name], h)
			}
		}
	}
	for _, pt := range pkgs {
		var declared []uint32
		for id, s := range pt.table.Symbols {
			if s.Declared && s.Handle != "" {
				declared = append(declared, uint32(id))
			}
		}
		var picks []uint32
		for range min(len(declared), perPackage) {
			picks = append(picks, declared[r.IntN(len(declared))])
		}
		for _, site := range pt.table.Sites {
			if k := pt.table.Symbols[site.Sym].Kind; site.Kind == facts.Call && site.Encl != facts.NoSym && (k == facts.Param || k == facts.Local) && pt.table.Symbols[site.Encl].Handle != "" && handleOf(pt, site.Sym) != "" {
				picks = append(picks, site.Encl)
				break
			}
		}
		for _, id := range picks {
			s := pt.table.Symbols[id]
			h := printed(pt, s.Handle)
			status, out, errText := codeRunOn(t, text, "symbol", h)
			var d struct {
				Handle, Kind, Package string
			}
			if status != 0 || json.Unmarshal([]byte(out), &d) != nil || d.Handle != h || d.Kind != s.Kind.String() || d.Package != pt.path {
				fail("symbol %s: status %d %q %q; want the %s of package %s", h, status, out, errText, s.Kind, pt.path)
			}
			// The sites, from the tables.
			refs, _ := facts.References(tables, s.Handle)
			var want []string
			for _, ref := range refs {
				owner := pkgs[ref.Table]
				name := owner.table.Files[ref.Site.File]
				line, col := owner.syn[name].Position(syntax.Pos(ref.Site.Off))
				want = append(want, fmt.Sprintf("%s:%d:%d %s", name, line, col, ref.Site.Kind))
			}
			status, out, errText = codeRunOn(t, text, "references", "--of", h, "--limit", "1000")
			var page struct {
				Sites []struct {
					Symbol, File, Kind string
					Line, Column       int
				}
				Total int
			}
			if status != 0 || json.Unmarshal([]byte(out), &page) != nil {
				fail("references --of %s: status %d %q %q", h, status, out, errText)
			}
			var got []string
			for _, site := range page.Sites {
				if site.Symbol != h {
					fail("references --of %s: a site of %s", h, site.Symbol)
				}
				got = append(got, fmt.Sprintf("%s:%d:%d %s", site.File, site.Line, site.Column, site.Kind))
			}
			if !slices.Equal(got, want) || page.Total != len(want) {
				fail("references --of %s:\n got %d %v\nwant %d %v", h, page.Total, got, len(want), want)
			}
			// The calls at depth 1, out of it and into it.
			var out1, in1 []string
			for _, site := range pt.table.Sites {
				if site.Kind == facts.Call && site.Encl == id && handleOf(pt, site.Sym) != "" {
					name := pt.table.Files[site.File]
					line, col := pt.syn[name].Position(syntax.Pos(site.Off))
					out1 = append(out1, fmt.Sprintf("%s>%s %s:%d:%d", h, handleOf(pt, site.Sym), name, line, col))
				}
			}
			for _, ref := range refs {
				owner := pkgs[ref.Table]
				if ref.Site.Kind != facts.Call || ref.Site.Encl == facts.NoSym || handleOf(owner, ref.Site.Encl) == "" {
					continue
				}
				name := owner.table.Files[ref.Site.File]
				line, col := owner.syn[name].Position(syntax.Pos(ref.Site.Off))
				in1 = append(in1, fmt.Sprintf("%s>%s %s:%d:%d", handleOf(owner, ref.Site.Encl), h, name, line, col))
			}
			for _, dir := range []string{"out", "in"} {
				want := out1
				if dir == "in" {
					want = in1
				}
				status, out, errText = codeRunOn(t, text, "calls", "--of", h, "--direction", dir, "--limit", "1000")
				if k := s.Kind; k == facts.Type || k == facts.TypeParam || k == facts.Field || k == facts.Variant || k == facts.Package {
					if status != 1 || !strings.HasPrefix(errText, "NotCallable: ") {
						fail("calls --of %s (a %s): status %d %q %q; want NotCallable", h, k, status, out, errText)
					}
					continue
				}
				var calls struct {
					Hops []struct {
						Depth int
						Call  struct {
							From, To, File string
							Line, Column   int
						}
					}
				}
				if status != 0 || json.Unmarshal([]byte(out), &calls) != nil {
					fail("calls --of %s --direction %s: status %d %q %q", h, dir, status, out, errText)
				}
				var got []string
				for _, hop := range calls.Hops {
					c := hop.Call
					got = append(got, fmt.Sprintf("%s>%s %s:%d:%d", c.From, c.To, c.File, c.Line, c.Column))
				}
				slices.Sort(got)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					fail("calls --of %s --direction %s:\n got %v\nwant %v", h, dir, got, want)
				}
			}
			// The short name.
			name := strings.TrimPrefix(h, pt.path+".")
			status, out, errText = codeRunOn(t, text, "symbol", name)
			switch candidates := short[name]; {
			case len(candidates) == 1:
				if status != 0 || !strings.Contains(out, `"handle":"`+h+`"`) {
					fail("symbol %s: status %d %q %q; want %s", name, status, out, errText, h)
				}
			default:
				tagged, _ := json.Marshal(map[string]any{"_tag": "Ambiguous", "address": name, "candidates": candidates})
				if status != 1 || out != "" || errText != "Ambiguous: "+string(tagged)+"\n" {
					fail("symbol %s: status %d %q %q; want Ambiguous with %v", name, status, out, errText, candidates)
				}
			}
		}
	}
	reachAgrees(t, a, text, r, fail)
	machinesAgree(t, a, text, fail)
}

// machinesAgree holds `ergo code machine-of` and `ergo inspect machine`'s
// text to the graph machinegraph reads from the checked program, for every
// machine of the program: its states, signals and initial state, each arm
// (place, variants, partial, edges), the timers, the work, the exit
// actions and the unsure states; and the text of each package's machines
// (`ergo inspect machine` with the package as root) names each arm, edge,
// timer, work and exit action at its place.
//
// Mutants machine-timer-dropped, machine-text-timer-dropped,
// machine-exit-dropped, machine-text-exit-dropped
// (cmd/ergo/testdata/mutants.json).
func machinesAgree(t *testing.T, a sketchfront.Analysis, text string, fail func(string, ...any)) {
	t.Helper()
	pkgs := a.Packages()
	for _, p := range pkgs {
		if p.Package == nil {
			continue
		}
		fileOf := map[syntax.Node]sketchfront.CheckedFile{}
		for _, cf := range p.Files {
			if cf.Syntax == nil {
				continue
			}
			for _, d := range cf.Syntax.Decls {
				fileOf[d] = cf
			}
		}
		at := func(decl syntax.Node, pos syntax.Pos) machinegraph.Site {
			cf, ok := fileOf[decl]
			if !ok {
				return machinegraph.Site{}
			}
			return machinegraph.Site{File: cf.Rel, Line: cf.Syntax.Line(pos)}
		}
		var lines []string
		for _, g := range machinegraph.Machines(p.Package, at) {
			h := p.Path + "." + g.Machine
			status, out, errText := codeRunOn(t, text, "machine-of", h)
			type place struct {
				What, File string
				Line       int
			}
			var v struct {
				Graph struct {
					Name, Initial           string
					States, Signals, Unsure []string
					Arms                    []struct {
						At                  place
						States, Signals     []string
						AnyState, AnySignal bool
						Partial             bool
						Alts                []struct {
							States, Signals     []string
							AnyState, AnySignal bool
							Partial             bool
						}
						Edges []struct {
							Decision, To string
							At           place
						}
					}
					Timers []struct {
						State, Signal string
						At            place
					}
					Work []place
					Exit []place
				}
			}
			if status != 0 || json.Unmarshal([]byte(out), &v) != nil {
				fail("machine-of %s: status %d %q %q", h, status, out, errText)
			}
			m := v.Graph
			names := func(vs []machinegraph.Variant) []string {
				out := []string{}
				for _, x := range vs {
					out = append(out, x.Name)
				}
				return out
			}
			site := func(p place) machinegraph.Site { return machinegraph.Site{File: p.File, Line: p.Line} }
			ok := m.Name == g.Machine && m.Initial == g.Initial && slices.Equal(m.States, names(g.States)) && slices.Equal(m.Signals, names(g.Signals)) &&
				slices.Equal(m.Unsure, append([]string{}, g.Unsure...)) && len(m.Arms) == len(g.Arms) && len(m.Timers) == len(g.Timers) && len(m.Work) == len(g.Work) && len(m.Exit) == len(g.Exit)
			for k := 0; ok && k < len(g.Arms); k++ {
				x, y := m.Arms[k], g.Arms[k]
				ok = site(x.At) == y.Site && x.AnyState == (y.States == nil) && x.AnySignal == (y.Signals == nil) &&
					slices.Equal(x.States, append([]string{}, y.States...)) && slices.Equal(x.Signals, append([]string{}, y.Signals...)) && x.Partial == y.Partial && len(x.Edges) == len(y.Edges) && len(x.Alts) == len(y.Alts)
				for e := 0; ok && e < len(y.Alts); e++ {
					p, q := x.Alts[e], y.Alts[e]
					ok = p.AnyState == (q.States == nil) && p.AnySignal == (q.Signals == nil) && slices.Equal(p.States, append([]string{}, q.States...)) && slices.Equal(p.Signals, append([]string{}, q.Signals...)) && p.Partial == q.Partial
				}
				for e := 0; ok && e < len(y.Edges); e++ {
					ok = x.Edges[e].Decision == y.Edges[e].Decision && x.Edges[e].To == y.Edges[e].To && site(x.Edges[e].At) == y.Edges[e].Site
				}
			}
			for _, tm := range m.Timers {
				ok = ok && g.Timers[tm.State] == machinegraph.Timer{Signal: tm.Signal, Site: site(tm.At)}
			}
			for _, w := range m.Work {
				ok = ok && g.Work[w.What] == site(w)
			}
			for _, x := range m.Exit {
				ok = ok && g.Exit[x.What] == site(x)
			}
			if !ok {
				fail("machine-of %s:\n got %+v\nwant %+v", h, m, *g)
			}
			for _, y := range g.Arms {
				lines = append(lines, fmt.Sprintf("  arm %s (", y.Site))
				for _, e := range y.Edges {
					lines = append(lines, fmt.Sprintf("    %s", e.Decision), fmt.Sprintf(" at %s\n", e.Site))
				}
			}
			for k, tm := range g.Timers {
				lines = append(lines, fmt.Sprintf("  timer %s -> %s at %s\n", k, tm.Signal, tm.Site))
			}
			for k, w := range g.Work {
				lines = append(lines, fmt.Sprintf("  work %s at %s\n", k, w))
			}
			for k, x := range g.Exit {
				lines = append(lines, fmt.Sprintf("  exit %s at %s\n", k, x))
			}
		}
		if len(lines) == 0 {
			continue
		}
		// The text of this package's machines: the tables with it as root.
		ct := codeTablesOf(a)
		ct.Root = p.Path
		tables, err := json.Marshal(ct)
		if err != nil {
			t.Fatal(err)
		}
		out, err := code.Machines(context.Background(), string(tables), false)
		if err != nil || len(out) != 3 || out[0] != "0" {
			fail("inspect machine: %v %q", err, out)
		}
		for _, l := range lines {
			if !strings.Contains(out[1], l) {
				fail("inspect machine: no %q in\n%s", l, out[1])
			}
		}
	}
}

// reachAgrees holds `ergo code reach` to the checker on the program a,
// for each capability a with of it discharges, each failure a catch of it
// handles, each provider a with of it binds, and up to three more the seed
// draws:
//   - the functions answered for a capability or a failure are those
//     whose derived needs or fails row (Signature.Uses, Signature.Errors)
//     holds it, by their declaring name;
//   - the discharges are the calls in a with's block whose row at the
//     site (Info.Calls) holds a capability the with discharges, the calls
//     in a catch's expression whose error row at the site holds a failure
//     the catch handles (it is total, an arm catches the rest, or an arm
//     tests the failure's type or a union that holds it, or matches a
//     record pattern of it), and for a provider the calls whose
//     capability the innermost with that discharges it binds to the
//     provider, read in the package that declares the with's context;
//     all found by walking each with's block and each catch's expression
//     (the tables find them by offsets);
//   - a record pattern that tests a field, as written (a field's pattern
//     other than a name or _), takes only some values: unless another
//     catch around the call takes them all, the call still brings the
//     failure;
//   - each call of a chain charges it and does not stop it, and a
//     function whose own call charges it and does not stop it has a
//     chain.
//
// Mutants reach-with-ignored, reach-catch-ignored, reach-provider-dropped,
// reach-catch-record-ignored, reach-provider-other-package,
// reach-catch-record-partial-whole (cmd/ergo/testdata/mutants.json).
func reachAgrees(t *testing.T, a sketchfront.Analysis, text string, r *rand.Rand, fail func(string, ...any)) {
	t.Helper()
	root := ""
	for _, p := range a.Packages() {
		root = p.Path
	}
	capOf := func(ty check.Type) string {
		if p, ok := ty.(*check.Pointer); ok {
			ty = p.Elem
		}
		n, _ := ty.(*check.Named)
		if n != nil && n.Orig != nil {
			n = n.Orig
		}
		if n == nil || n.Obj == nil {
			return ""
		}
		if n.Obj.Pkg == nil || n.Obj.Pkg.Path == "" {
			return n.Obj.Name
		}
		if n.Obj.Pkg.Path == "main" {
			return root + "." + n.Obj.Name
		}
		return n.Obj.Pkg.Path + "." + n.Obj.Name
	}
	callName := func(x syntax.Expr) *syntax.Ident {
		for {
			switch e := x.(type) {
			case *syntax.Ident:
				return e
			case *syntax.SelectorExpr:
				return e.Sel
			case *syntax.IndexExpr:
				x = e.X
			case *syntax.ParenExpr:
				x = e.X
			default:
				return nil
			}
		}
	}
	// needers: capability → "file:line:col" of each function's name;
	// charged and discharged: capability → call positions.
	needers := map[string]map[string]bool{}
	charged := map[string]map[string]bool{}
	discharged := map[string]map[string]bool{}
	// whole and someOf: failure → the calls a catch takes every value of
	// it at, and some values only; a call it is only some of still raises.
	whole := map[string]map[string]bool{}
	someOf := map[string]map[string]bool{}
	add := func(m map[string]map[string]bool, k, v string) {
		if m[k] == nil {
			m[k] = map[string]bool{}
		}
		m[k][v] = true
	}
	stopped := func(k, p string) bool { return discharged[k][p] && (!someOf[k][p] || whole[k][p]) }
	var withCaps []string
	// callsIn is each call of a function that charges it something.
	type inCall struct{ what, fn, call string }
	var callsIn []inCall
	// provided: call position → capability → the provider's handle, the
	// innermost with's (a with is walked before the withs in its block).
	provided := map[string]map[string]string{}
	handleOf := func(o check.Object) string {
		switch x := o.(type) {
		case *check.Func:
			if x.Pkg != nil && x.Pkg.Path == "main" {
				return root + "." + x.Name
			} else if x.Pkg != nil && x.Decl != nil && x.Decl.Recv == nil {
				return x.Pkg.Path + "." + x.Name
			}
		case *check.TypeName:
			return capOf(x.Type)
		}
		return ""
	}
	// declaredIn is the Info of the package whose named context holds
	// each entry: a with of one package may bind another's context.
	declaredIn := map[*check.ContextEntry]*check.Info{}
	for _, p := range a.Packages() {
		if p.Package == nil {
			continue
		}
		for _, c := range p.Package.Contexts {
			for _, e := range c.Entries {
				declaredIn[e] = p.Package.Info
			}
		}
	}
	for _, p := range a.Packages() {
		if p.Package == nil {
			continue
		}
		info := p.Package.Info
		providerOf := func(entry *check.ContextEntry) string {
			info := info
			if in := declaredIn[entry]; in != nil {
				info = in
			}
			e := entry.Provider
			switch x := e.(type) {
			case *syntax.CallExpr:
				e = x.Fun
			case *syntax.CompositeLit:
				e = x.Type
			}
			name := callName(e)
			if name == nil {
				return ""
			}
			if sel, ok := e.(*syntax.SelectorExpr); ok && info.Uses[name] == nil {
				// pkg.Name: the package's declaration of that name.
				if q, ok := sel.X.(*syntax.Ident); ok {
					if pn, ok := info.Uses[q].(*check.PkgName); ok && pn.Pkg != nil {
						return pn.Pkg.Path + "." + name.Name
					}
				}
				return ""
			}
			if info.Uses[name] == nil {
				return ""
			}
			return handleOf(info.Uses[name])
		}
		for _, cf := range p.Files {
			if cf.Syntax == nil {
				continue
			}
			at := func(pos syntax.Pos) string {
				line, col := cf.Syntax.Position(pos)
				return fmt.Sprintf("%s:%d:%d", cf.Rel, line, col)
			}
			for _, fn := range p.Package.Funcs {
				if fn.Decl == nil || fn.Sig == nil || !slices.Contains(cf.Syntax.Decls, syntax.Decl(fn.Decl)) {
					continue
				}
				for _, ty := range fn.Sig.Uses.Elems() {
					if c := capOf(ty); c != "" {
						add(needers, c, at(fn.Decl.Name.NamePos))
					}
				}
				for _, ty := range fn.Sig.Errors.Elems() {
					if c := capOf(ty); c != "" {
						add(needers, c, at(fn.Decl.Name.NamePos))
					}
				}
				syntax.Inspect(fn.Decl, func(n syntax.Node) bool {
					y, ok := n.(*syntax.CallExpr)
					if !ok || info.Calls[y] == nil || info.Withs[y] != nil {
						return true
					}
					// A call of no declared function (a builtin such as
					// decode[T], a function value) has no address in the
					// tables: what it raises, the function raises itself.
					if info.Calls[y].Func == nil && info.Calls[y].Op == nil {
						return true
					}
					if name := callName(y.Fun); name != nil {
						for _, r := range []*check.Row{info.Calls[y].Needs, info.Calls[y].Fails} {
							for _, ty := range r.Elems() {
								if k := capOf(ty); k != "" {
									callsIn = append(callsIn, inCall{k, at(fn.Decl.Name.NamePos), at(name.NamePos)})
								}
							}
						}
					}
					return true
				})
			}
			syntax.Inspect(cf.Syntax, func(n syntax.Node) bool {
				if x, ok := n.(*syntax.CatchExpr); ok {
					m := info.Matches[x]
					all := m == nil || !m.Partial
					var handled, partly []string
					for _, arm := range x.Arms {
						for _, te := range arm.Types {
							if as, ok := te.(*syntax.AsPattern); ok {
								te = as.X
							}
							pt := info.Patterns[te]
							// A record pattern whose every field is a name or
							// _, as written, takes every value; one that
							// tests a field takes some.
							some := false
							if lit, ok := te.(*syntax.CompositeLit); ok {
								for _, el := range lit.Elts {
									v := el
									if kv, ok := el.(*syntax.KeyValueExpr); ok {
										v = kv.Value
									}
									vp := info.Patterns[v]
									_, isName := v.(*syntax.Ident)
									some = some || !isName || vp == nil || vp.Kind != check.PatWildcard && vp.Kind != check.PatBind
								}
							}
							switch {
							case pt == nil:
							case pt.Kind == check.PatWildcard || pt.Kind == check.PatBind:
								all = true
							case some:
								partly = append(partly, capOf(pt.Type))
							case pt.Kind == check.PatTypeTest || pt.Kind == check.PatRecord:
								handled = append(handled, capOf(pt.Type))
								if n, ok := pt.Type.(*check.Named); ok {
									if u, ok := n.Underlying().(*check.Union); ok {
										for _, v := range u.Variants {
											handled = append(handled, capOf(v))
										}
									}
								}
							}
						}
					}
					syntax.Inspect(x.X, func(n syntax.Node) bool {
						y, ok := n.(*syntax.CallExpr)
						if !ok || info.Calls[y] == nil || info.Withs[y] != nil {
							return true // a with is no call edge of the tables
						}
						if name := callName(y.Fun); name != nil {
							for _, ty := range info.Calls[y].Fails.Elems() {
								k := capOf(ty)
								if k == "" {
									continue
								}
								if all || slices.Contains(handled, k) {
									add(whole, k, at(name.NamePos))
								} else if slices.Contains(partly, k) {
									add(someOf, k, at(name.NamePos))
								} else {
									continue
								}
								add(discharged, k, at(name.NamePos))
								if !slices.Contains(withCaps, k) {
									withCaps = append(withCaps, k)
								}
							}
						}
						return true
					})
					return true
				}
				x, ok := n.(*syntax.CallExpr)
				if !ok {
					return true
				}
				if c := info.Calls[x]; c != nil {
					if name := callName(x.Fun); name != nil {
						for _, ty := range c.Needs.Elems() {
							if k := capOf(ty); k != "" {
								add(charged, k, at(name.NamePos))
							}
						}
						for _, ty := range c.Fails.Elems() {
							if k := capOf(ty); k != "" {
								add(charged, k, at(name.NamePos))
							}
						}
					}
				}
				w := info.Withs[x]
				if w == nil || x.Trailing == nil {
					return true
				}
				var caps []string
				providers := map[string]string{}
				for ; w != nil; w = w.Next {
					for i, ty := range w.Discharged {
						if k := capOf(ty); k != "" {
							caps = append(caps, k)
							if i < len(w.Entries) && w.Entries[i] != nil && w.Entries[i].Provider != nil {
								if h := providerOf(w.Entries[i]); h != "" {
									providers[k] = h
								}
							}
						}
					}
				}
				syntax.Inspect(x.Trailing, func(n syntax.Node) bool {
					y, ok := n.(*syntax.CallExpr)
					if !ok || info.Calls[y] == nil {
						return true
					}
					name := callName(y.Fun)
					if name == nil {
						return true
					}
					for _, ty := range info.Calls[y].Needs.Elems() {
						if k := capOf(ty); k != "" && slices.Contains(caps, k) {
							add(discharged, k, at(name.NamePos))
							if !slices.Contains(withCaps, k) {
								withCaps = append(withCaps, k)
							}
							if provided[at(name.NamePos)] == nil {
								provided[at(name.NamePos)] = map[string]string{}
							}
							provided[at(name.NamePos)][k] = providers[k]
						}
					}
					return true
				})
				return true
			})
		}
	}
	for p, byCap := range provided {
		for _, h := range byCap {
			if h != "" {
				add(discharged, h, p)
				if !slices.Contains(withCaps, h) {
					withCaps = append(withCaps, h)
				}
			}
		}
	}
	slices.Sort(withCaps)
	all := make([]string, 0, len(needers))
	for k := range needers {
		all = append(all, k)
	}
	slices.Sort(all)
	asked := slices.Clone(withCaps)
	for range min(3, len(all)) {
		asked = append(asked, all[r.IntN(len(all))])
	}
	for _, k := range asked {
		status, out, errText := codeRunOn(t, text, "reach", "--of", k, "--limit", "1000")
		var page struct {
			Kind    string
			Reached []struct {
				Function string
				Chain    []struct {
					File         string
					Line, Column int
				}
			}
			Discharges []struct {
				File         string
				Line, Column int
			}
		}
		if status != 0 || json.Unmarshal([]byte(out), &page) != nil {
			fail("reach --of %s: status %d %q %q", k, status, out, errText)
		}
		var got, want []string
		for _, rc := range page.Reached {
			st, o, e := codeRunOn(t, text, "symbol", rc.Function)
			var d struct {
				File         string
				Line, Column int
			}
			if st != 0 || json.Unmarshal([]byte(o), &d) != nil {
				fail("reach --of %s: function %s: status %d %q %q", k, rc.Function, st, o, e)
			}
			fp := fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Column)
			got = append(got, fp)
			if len(rc.Chain) == 0 {
				for _, c := range callsIn {
					if c.what == k && c.fn == fp && !stopped(k, c.call) {
						fail("reach --of %s: %s has no chain, but its call at %s brings it", k, rc.Function, c.call)
					}
				}
			}
			for _, c := range rc.Chain {
				p := fmt.Sprintf("%s:%d:%d", c.File, c.Line, c.Column)
				if !charged[k][p] || stopped(k, p) {
					fail("reach --of %s: the chain of %s passes %s, which does not bring it (charged %v, discharged %v)", k, rc.Function, p, charged[k][p], discharged[k][p])
				}
			}
		}
		for p := range needers[k] {
			want = append(want, p)
		}
		slices.Sort(got)
		slices.Sort(want)
		// A provider's functions are the callers of its discharges; the
		// discharges below hold the provider to the checker.
		if page.Kind != "provider" && !slices.Equal(got, want) {
			fail("reach --of %s: functions\n got %v\nwant %v", k, got, want)
		}
		got, want = nil, nil
		for _, c := range page.Discharges {
			got = append(got, fmt.Sprintf("%s:%d:%d", c.File, c.Line, c.Column))
		}
		for p := range discharged[k] {
			want = append(want, p)
		}
		slices.Sort(got)
		got = slices.Compact(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			fail("reach --of %s: discharges\n got %v\nwant %v", k, got, want)
		}
	}
}

// TestCodeFailureText: an address that names nothing is NoSymbol, and a
// short name two packages declare is Ambiguous with both handles, as the
// generated command line prints a typed failure (its tag and its JSON on
// standard error, status 1; on standard output with --json). A package
// that does not check gives the rows the checker resolved, with complete
// false and its error count. Mutant
// code-partial-dropped (cmd/ergo/testdata/mutants.json).
func TestCodeFailureText(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ergo.mod", "module shop\n")
	write("cart/cart.ergo", "func Total(n int) int { n * 2 }\n")
	write("stock/stock.ergo", "func Total(n int) int { n + 1 }\n\nfunc Broken() int { \"text\" }\n")
	write("cmd/shop/main.ergo", "import shop/cart\n\nfunc main() needs Console { Console.Println(\"\\(cart.Total(2))\") }\n")
	a, err := sketchfront.CheckDir(filepath.Join(dir, "cmd", "shop"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		args          []string
		status        int
		stdout, errOf string
	}{
		{[]string{"symbol", "nosuch"}, 1, "", `NoSymbol: {"_tag":"NoSymbol","address":"nosuch"}` + "\n"},
		{[]string{"--json", "symbol", "nosuch"}, 1, `{"_tag":"NoSymbol","address":"nosuch"}` + "\n", ""},
		{[]string{"references", "--of", "x.ergo:1:1"}, 1, "", `NoSymbol: {"_tag":"NoSymbol","address":"x.ergo:1:1"}` + "\n"},
	} {
		status, out, errText := codeRun(t, a, row.args...)
		if status != row.status || out != row.stdout || errText != row.errOf {
			t.Errorf("ergo code %v: %d %q %q; want %d %q %q", row.args, status, out, errText, row.status, row.stdout, row.errOf)
		}
	}
	// Two packages declare Total; the program imports one, so its check
	// reads only cart: ask in a program that imports both.
	write("cmd/both/main.ergo", "import shop/cart\nimport shop/stock\n\nfunc main() needs Console { Console.Println(\"\\(cart.Total(2) + stock.Total(1))\") }\n")
	both, err := sketchfront.CheckDir(filepath.Join(dir, "cmd", "both"))
	if err != nil {
		t.Fatal(err)
	}
	status, out, errText := codeRun(t, both, "symbol", "Total")
	if want := `Ambiguous: {"_tag":"Ambiguous","address":"Total","candidates":["shop/cart.Total","shop/stock.Total"]}` + "\n"; status != 1 || out != "" || errText != want {
		t.Errorf("ergo code symbol Total: %d %q %q; want 1 and %q", status, out, errText, want)
	}
	// stock does not check: its rows are there, and the answer says so.
	status, out, _ = codeRun(t, both, "describe")
	var summary struct {
		Complete    bool
		Diagnostics int
	}
	if status != 0 || json.Unmarshal([]byte(out), &summary) != nil || summary.Complete || summary.Diagnostics == 0 {
		t.Errorf("ergo code describe over a package with an error: %d %q; want complete false and its errors", status, out)
	}
	status, out, _ = codeRun(t, both, "symbol", "shop/stock.Broken")
	if status != 0 || !strings.Contains(out, `"handle":"shop/stock.Broken"`) {
		t.Errorf("ergo code symbol shop/stock.Broken: %d %q; want the declaration the checker resolved", status, out)
	}
}
