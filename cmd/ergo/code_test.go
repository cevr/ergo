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
// code-short-first, code-site-dropped, code-call-value-dropped
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
	slices.Sort(programs)
	r.Shuffle(len(programs), func(i, j int) { programs[i], programs[j] = programs[j], programs[i] })
	perPackage := 2
	for _, dir := range programs {
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
}

// reachAgrees holds `ergo code reach` to the checker on the program a,
// for each capability a with of it discharges and up to three more the
// seed draws:
//   - the functions answered are those whose derived needs row
//     (Signature.Uses) holds the capability, by their declaring name;
//   - the discharges are the calls in a with's block whose row at the
//     site (Info.Calls) holds a capability the with discharges, found by
//     walking each with's block (the tables find them by offsets);
//   - each call of a chain charges the capability and is not one of them.
//
// Mutant reach-with-ignored (cmd/ergo/testdata/mutants.json).
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
	add := func(m map[string]map[string]bool, k, v string) {
		if m[k] == nil {
			m[k] = map[string]bool{}
		}
		m[k][v] = true
	}
	var withCaps []string
	for _, p := range a.Packages() {
		if p.Package == nil {
			continue
		}
		info := p.Package.Info
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
			}
			syntax.Inspect(cf.Syntax, func(n syntax.Node) bool {
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
					}
				}
				w := info.Withs[x]
				if w == nil || x.Trailing == nil {
					return true
				}
				var caps []string
				for ; w != nil; w = w.Next {
					for _, ty := range w.Discharged {
						if k := capOf(ty); k != "" {
							caps = append(caps, k)
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
						}
					}
					return true
				})
				return true
			})
		}
	}
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
		status, out, errText := codeRunOn(t, text, "reach", "--capability", k, "--limit", "1000")
		var page struct {
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
			fail("reach --capability %s: status %d %q %q", k, status, out, errText)
		}
		var got, want []string
		for _, rc := range page.Reached {
			st, o, e := codeRunOn(t, text, "symbol", rc.Function)
			var d struct {
				File         string
				Line, Column int
			}
			if st != 0 || json.Unmarshal([]byte(o), &d) != nil {
				fail("reach --capability %s: function %s: status %d %q %q", k, rc.Function, st, o, e)
			}
			got = append(got, fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Column))
			for _, c := range rc.Chain {
				p := fmt.Sprintf("%s:%d:%d", c.File, c.Line, c.Column)
				if !charged[k][p] || discharged[k][p] {
					fail("reach --capability %s: the chain of %s passes %s, which does not bring it (charged %v, discharged %v)", k, rc.Function, p, charged[k][p], discharged[k][p])
				}
			}
		}
		for p := range needers[k] {
			want = append(want, p)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			fail("reach --capability %s: functions\n got %v\nwant %v", k, got, want)
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
			fail("reach --capability %s: discharges\n got %v\nwant %v", k, got, want)
		}
	}
}

// TestCodeFailureText: an address that names nothing is NoSymbol, and a
// short name two packages declare is Ambiguous with both handles, as the
// generated command line prints a typed failure (its tag and its JSON on
// standard error, status 1; on standard output with --json). A package
// that does not check gives the rows the checker resolved, with complete
// false and its error count. Mutants code-complete-ignores-errors,
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
