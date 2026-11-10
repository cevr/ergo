package std_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/std"
)

// The std strengths rule (docs/spec/stdlib.md, Std strengths over Effect;
// owner, 2026-11-08): each std package, the prelude included, has one row
// that names its Effect module, the Effect workarounds it does not copy
// (W1 to W20), the Ergo construct it uses with a spec link, and its
// lowering against hand Go: a benchmark the repository declares, or a
// "pending:" or "none:" line with its reason.

const strengthsHeading = "## Std strengths over Effect"

var strengthsHeader = []string{"Package", "Effect module", "Workarounds not copied", "Ergo construct", "Lowering against hand Go"}

// strengthRow is one row of the table, with its 1-based line in the doc.
type strengthRow struct {
	pkg, effect, workarounds, construct, lowering string
	line                                          int
}

var (
	workaroundID   = regexp.MustCompile(`\bW(\d+)\b`)
	benchQualified = regexp.MustCompile(`([A-Za-z0-9_./-]+)\.(Benchmark\w*)`)
	benchAny       = regexp.MustCompile(`Benchmark\w*`)
)

// TestStdStrengthsTable: the table has one row per std package, found on
// disk and in the embedded set, and each row's cells keep the rule.
func TestStdStrengthsTable(t *testing.T) {
	disk, err := stdPackageDirs(".")
	if err != nil {
		t.Fatal(err)
	}
	// The packages on disk are the packages the std embeds, so the table
	// cannot miss a package that std.Packages() serves.
	embedded := append([]string{std.PreludePath}, std.Packages()...)
	slices.Sort(embedded)
	if !slices.Equal(disk, embedded) {
		t.Fatalf("the std packages on disk are %v; std.Packages() and the prelude are %v", disk, embedded)
	}
	if len(disk) < 20 {
		t.Fatalf("the walk found %d std packages; want 20 or more", len(disk))
	}
	doc := readStdlibDoc(t)
	for _, problem := range strengthsProblems(t, ".", doc) {
		t.Error(problem)
	}
}

// TestStdStrengthsFixtures: the guard judges a fixture std tree and table,
// so a package added to the tree needs a row, and each broken cell is
// named.
func TestStdStrengthsFixtures(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"prelude.ergo", "alpha/a.ergo", "beta/b.ergo", "fresh/f.ergo", "notes/readme.md"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const doc = "# x\n\n" + strengthsHeading + "\n\nText.\n\n" +
		"| Package | Effect module | Workarounds not copied | Ergo construct | Lowering against hand Go |\n" +
		"| --- | --- | --- | --- | --- |\n" +
		"| `prelude` | `Option.ts` | W10 | unions ([types.md, Unions](types.md#unions)) | none: no body |\n" +
		"| `alpha` | `Array.ts` | W2, W21 | functions ([x](x.md)) | `runtime/v1.BenchmarkSlicesSort`: parity |\n" +
		"| `beta` | `String.ts` | W3 | plain functions | `runtime/v1.BenchmarkNoSuchThing`: 1 ns; `BenchmarkText` |\n" +
		"| `beta` | `String.ts` | none |  | pending: |\n" +
		"| `gone` | `Queue.ts` | W5 | rows ([rows.md, Catch](rows.md#catch)) | none: no body |\n" +
		"\n## Next\n"
	got := strengthsProblems(t, root, doc)
	want := []string{
		"stdlib.md:10: alpha: workaround id W21 is outside W1 to W20",
		"stdlib.md:11: beta: name each benchmark with its package dir (dir.BenchmarkName)",
		"stdlib.md:11: beta: no func runtime/v1.BenchmarkNoSuchThing in the _test.go files of runtime/v1",
		"stdlib.md:11: beta: the Ergo construct cell has no spec link",
		"stdlib.md:12: beta: a second row",
		"stdlib.md:12: beta: the Ergo construct cell is empty",
		"stdlib.md:12: beta: the Lowering cell names no benchmark and gives no reason after \"none:\" or \"pending:\"",
		"stdlib.md:13: gone: the row names no std package",
		"fresh: std package with no row in " + strengthsHeading,
	}
	if !slices.Equal(got, want) {
		t.Errorf("the problems are\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// readStdlibDoc is docs/spec/stdlib.md.
func readStdlibDoc(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRootDir(t), "docs", "spec", "stdlib.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func moduleRootDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no module root at %s: %v", root, err)
	}
	return root
}

// stdPackageDirs are the std packages of the source tree at root: each
// directory below root that holds an .ergo file, by its slash path, and
// "prelude" when root holds prelude.ergo. Sorted.
func stdPackageDirs(root string) ([]string, error) {
	var out []string
	if _, err := os.Stat(filepath.Join(root, "prelude.ergo")); err == nil {
		out = append(out, std.PreludePath)
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return err
		}
		if p == filepath.Join(root, "gostd") {
			return filepath.SkipDir // Go's bindings, not std packages (interop.md)
		}
		matches, err := filepath.Glob(filepath.Join(p, "*.ergo"))
		if err != nil {
			return err
		}
		if len(matches) > 0 {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out, err
}

// parseStrengths reads the table of the strengths section of doc.
func parseStrengths(doc string) ([]strengthRow, error) {
	var rows []strengthRow
	in, header := false, false
	for i, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "## ") {
			in = line == strengthsHeading
			continue
		}
		if !in || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := splitCells(line)
		if !header {
			header = true
			if !slices.Equal(cells, strengthsHeader) {
				return nil, fmt.Errorf("stdlib.md:%d: the strengths table header is %q, want %q", i+1, cells, strengthsHeader)
			}
			continue
		}
		if strings.HasPrefix(cells[0], "---") {
			continue
		}
		if len(cells) != len(strengthsHeader) {
			return nil, fmt.Errorf("stdlib.md:%d: a strengths row has %d cells, want %d", i+1, len(cells), len(strengthsHeader))
		}
		rows = append(rows, strengthRow{pkg: strings.Trim(cells[0], "`"), effect: cells[1], workarounds: cells[2], construct: cells[3], lowering: cells[4], line: i + 1})
	}
	if !header {
		return nil, fmt.Errorf("stdlib.md has no %q table", strengthsHeading)
	}
	return rows, nil
}

// splitCells splits a Markdown table row at each "|" that is not escaped.
func splitCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// strengthsProblems judges the table of doc against the std tree at root.
// Benchmarks are looked up in the module's test files of the named
// package directory.
func strengthsProblems(t *testing.T, root, doc string) []string {
	t.Helper()
	pkgs, err := stdPackageDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := parseStrengths(doc)
	if err != nil {
		return []string{err.Error()}
	}
	benches := benchmarkIndex{root: moduleRootDir(t), dirs: map[string]map[string]bool{}}
	var out []string
	seen := map[string]bool{}
	for _, r := range rows {
		at := fmt.Sprintf("stdlib.md:%d: %s: ", r.line, r.pkg)
		if seen[r.pkg] {
			out = append(out, at+"a second row")
		}
		seen[r.pkg] = true
		if !slices.Contains(pkgs, r.pkg) {
			out = append(out, at+"the row names no std package")
		}
		for name, cell := range map[string]string{"Effect module": r.effect, "Workarounds not copied": r.workarounds, "Ergo construct": r.construct, "Lowering": r.lowering} {
			if cell == "" {
				out = append(out, at+"the "+name+" cell is empty")
			}
		}
		ids := workaroundID.FindAllStringSubmatch(r.workarounds, -1)
		if len(ids) == 0 && r.workarounds != "" && r.workarounds != "none" {
			out = append(out, at+"the Workarounds cell names no id W1 to W20 and is not \"none\"")
		}
		for _, m := range ids {
			if n, _ := strconv.Atoi(m[1]); n < 1 || n > 20 {
				out = append(out, at+"workaround id "+m[0]+" is outside W1 to W20")
			}
		}
		if r.construct != "" && !strings.Contains(r.construct, "](") {
			out = append(out, at+"the Ergo construct cell has no spec link")
		}
		out = append(out, lowerProblems(at, r.lowering, &benches)...)
	}
	for _, p := range pkgs {
		if !seen[p] {
			out = append(out, p+": std package with no row in "+strengthsHeading)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int {
		// Row problems by line, then by text; package problems last.
		if la, lb := problemLine(a), problemLine(b); la != lb {
			return la - lb
		}
		return strings.Compare(a, b)
	})
	return out
}

// problemLine is the doc line a problem names, or a large number for a
// problem with no line.
func problemLine(p string) int {
	rest, ok := strings.CutPrefix(p, "stdlib.md:")
	if !ok {
		return 1 << 30
	}
	n, _ := strconv.Atoi(rest[:strings.Index(rest, ":")])
	return n
}

// lowerProblems judges a Lowering cell: each benchmark it names, as
// dir.BenchmarkName, is declared; a cell with no benchmark gives a reason
// after "none:" or "pending:".
func lowerProblems(at, cell string, benches *benchmarkIndex) []string {
	if cell == "" {
		return nil
	}
	var out []string
	qualified := benchQualified.FindAllStringSubmatch(cell, -1)
	for _, m := range qualified {
		ok, err := benches.declares(m[1], m[2])
		switch {
		case err != nil:
			out = append(out, at+err.Error())
		case !ok:
			out = append(out, at+"no func "+m[1]+"."+m[2]+" in the _test.go files of "+m[1])
		}
	}
	if len(benchAny.FindAllString(cell, -1)) != len(qualified) {
		out = append(out, at+"name each benchmark with its package dir (dir.BenchmarkName)")
	}
	if len(qualified) == 0 {
		reason, ok := strings.CutPrefix(cell, "none:")
		if !ok {
			reason, ok = strings.CutPrefix(cell, "pending:")
		}
		if !ok || strings.TrimSpace(reason) == "" {
			out = append(out, at+"the Lowering cell names no benchmark and gives no reason after \"none:\" or \"pending:\"")
		}
	}
	return out
}

// benchmarkIndex finds the top-level func Benchmark… declarations of the
// _test.go files of a package directory of the module, reading each
// directory once.
type benchmarkIndex struct {
	root string
	dirs map[string]map[string]bool
}

func (b *benchmarkIndex) declares(dir, name string) (bool, error) {
	names, ok := b.dirs[dir]
	if !ok {
		if strings.Contains(dir, "..") || filepath.IsAbs(dir) {
			return false, fmt.Errorf("the benchmark dir %q is not a package dir of the module", dir)
		}
		paths, err := filepath.Glob(filepath.Join(b.root, filepath.FromSlash(dir), "*_test.go"))
		if err != nil {
			return false, err
		}
		if len(paths) == 0 {
			return false, fmt.Errorf("the benchmark dir %s has no _test.go file", dir)
		}
		names = map[string]bool{}
		fset := token.NewFileSet()
		for _, p := range paths {
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				return false, err
			}
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Benchmark") {
					names[fn.Name.Name] = true
				}
			}
		}
		b.dirs[dir] = names
	}
	return names[name], nil
}
