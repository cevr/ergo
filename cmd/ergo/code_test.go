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
//     candidate when several do.
//
// The examples are drawn from the seed: two under -short, every one
// otherwise. ERGO_CODE_SEED=N replays a seed. Mutants code-root-main,
// code-short-first, code-site-dropped (cmd/ergo/testdata/mutants.json).
func TestCodeAnswersAgreeWithTheTables(t *testing.T) {
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
	if testing.Short() {
		programs, perPackage = programs[:2], 4
	}
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
		var declared []facts.Symbol
		for _, s := range pt.table.Symbols {
			if s.Declared && s.Handle != "" {
				declared = append(declared, s)
			}
		}
		for range min(len(declared), perPackage) {
			s := declared[r.IntN(len(declared))]
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
