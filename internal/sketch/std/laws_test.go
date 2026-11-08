package std_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// The std law rule (docs/spec/stdlib.md, Std laws; std-align S1a): every
// operation type of every std package, the prelude included, has a member
// law or a written exemption, and every law of std source has exactly one
// row in the ledger, which names the Go test that runs it now.

// exemptPrefix starts the one doc-comment line that exempts an operation
// type from the rule. The text after it is the reason, never empty.
const exemptPrefix = "// law-exempt:"

// stdLawPackage is one checked std package with its parsed sources.
type stdLawPackage struct {
	path  string // "prelude" for the prelude
	pkg   *check.Package
	files []*syntax.File
}

// stdLawPackages checks the prelude and every importable std package with
// the checker that checks user code, and parses their sources. The list is
// std.Packages(), never a list written here.
func stdLawPackages(t *testing.T) []stdLawPackage {
	t.Helper()
	outside, ds := check.CheckSource("func uses(_ LiveScheduler) {}\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	var prelude *check.Package
	for id, obj := range outside.Info.Uses {
		if tn, ok := obj.(*check.TypeName); ok && id.Name == "LiveScheduler" {
			prelude = tn.Pkg
		}
	}
	if prelude == nil {
		t.Fatal("no prelude package behind LiveScheduler")
	}
	parse := func(path string, sources map[string]string) []*syntax.File {
		var out []*syntax.File
		names := make([]string, 0, len(sources))
		for name := range sources {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			f, err := syntax.Parse(sources[name])
			if err != nil {
				t.Fatalf("%s/%s: %v", path, name, err)
			}
			out = append(out, f)
		}
		return out
	}
	out := []stdLawPackage{{path: std.PreludePath, pkg: prelude, files: parse(std.PreludePath, std.Prelude())}}
	for _, path := range std.Packages() {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		sources, _ := std.Package(path)
		out = append(out, stdLawPackage{path: path, pkg: pkg, files: parse(path, sources)})
	}
	return out
}

// operationTypes are the operation types the checker declared in pkg, in
// source order: each type whose underlying type is an interface with
// operations (types.md, Laws 1).
func operationTypes(pkg *check.Package) []*check.TypeName {
	var out []*check.TypeName
	for _, tn := range pkg.Types {
		n, ok := tn.Type.(*check.Named)
		if !ok || tn.Decl == nil {
			continue
		}
		if it, ok := n.Underlying().(*check.Interface); ok && len(it.Methods) > 0 {
			out = append(out, tn)
		}
	}
	return out
}

// exemption is the reason of tn's law-exempt line and whether it has one.
// A line with an empty reason is reported as present with an empty reason.
func exemption(tn *check.TypeName) (string, bool) {
	if tn.Decl == nil || tn.Decl.Doc == nil {
		return "", false
	}
	for _, c := range tn.Decl.Doc.List {
		if rest, ok := strings.CutPrefix(c.Text, exemptPrefix); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// TestStdOperationTypesHaveLaws: each operation type of each std package
// and of the prelude has a member law, or one law-exempt line with a
// reason in its doc comment, and never both.
func TestStdOperationTypesHaveLaws(t *testing.T) {
	seen := 0
	for _, p := range stdLawPackages(t) {
		seen += len(operationTypes(p.pkg))
		for _, v := range lawRuleViolations(p.path, p.pkg) {
			t.Error(v)
		}
	}
	// The walk finds the operation types the checker declares; a walk that
	// finds none proves nothing.
	if seen < 10 {
		t.Errorf("the walk found %d operation types in std; want 10 or more", seen)
	}
}

// lawRuleViolations are the operation types of pkg that break the law
// rule, each with the way it breaks it.
func lawRuleViolations(path string, pkg *check.Package) []string {
	var out []string
	for _, tn := range operationTypes(pkg) {
		name := path + "." + tn.Name
		reason, exempt := exemption(tn)
		switch {
		case exempt && reason == "":
			out = append(out, name+": its "+exemptPrefix+" line gives no reason")
		case exempt && len(tn.Laws) > 0:
			out = append(out, name+" has laws and a "+exemptPrefix+" line; remove the exemption")
		case !exempt && len(tn.Laws) == 0:
			out = append(out, name+" is an operation type with no law; add a member law, or a doc line \""+exemptPrefix+" <reason>\" with the reason no law can state it")
		}
	}
	return out
}

// TestLawRuleFixtures: the rule's walk finds an operation type that no
// list names, in a package checked from source, and judges each case.
func TestLawRuleFixtures(t *testing.T) {
	const src = `type Record { n int }

type A {}
type B {}
type Union = A | B

type Fresh {
	Do() int,
}

// Doc.
// law-exempt: Mark returns nothing to observe.
type Exempt {
	Mark(),
}

// law-exempt:
type Blank {
	Mark(),
}

type Lawful {
	Get() int,

	law Stable() { Get() == Get() },
}

// law-exempt: both.
type Both {
	Get() int,

	law Stable() { Get() == Get() },
}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("fixture", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatalf("fixture: %v", ds)
	}
	var names []string
	for _, tn := range operationTypes(pkg) {
		names = append(names, tn.Name)
	}
	if want := []string{"Fresh", "Exempt", "Blank", "Lawful", "Both"}; !slices.Equal(names, want) {
		t.Errorf("the operation types are %v, want %v", names, want)
	}
	got := lawRuleViolations("fixture", pkg)
	want := []string{
		"fixture.Fresh is an operation type with no law; add a member law, or a doc line \"// law-exempt: <reason>\" with the reason no law can state it",
		"fixture.Blank: its // law-exempt: line gives no reason",
		"fixture.Both has laws and a // law-exempt: line; remove the exemption",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the violations are\n%q\nwant\n%q", got, want)
	}
}

// ledgerRow is one row of the Std laws table.
type ledgerRow struct {
	pkg, law, statement, runner, state string
	line                               int
}

// stdlibDoc is the spec file that holds the ledger.
const stdlibDoc = "../../../docs/spec/stdlib.md"

// readLedger parses the table of the "## Std laws" section of stdlib.md.
func readLedger(t *testing.T) []ledgerRow {
	t.Helper()
	data, err := os.ReadFile(stdlibDoc)
	if err != nil {
		t.Fatal(err)
	}
	var rows []ledgerRow
	in, header := false, false
	for i, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			in = line == "## Std laws"
			continue
		case !in || !strings.HasPrefix(line, "|"):
			continue
		}
		cells := splitRow(line)
		if !header {
			header = true
			if !slices.Equal(cells, []string{"Package", "Law", "Statement", "Runner", "State"}) {
				t.Fatalf("stdlib.md:%d: the Std laws table header is %q", i+1, cells)
			}
			continue
		}
		if strings.HasPrefix(cells[0], "---") {
			continue
		}
		if len(cells) != 5 {
			t.Errorf("stdlib.md:%d: a ledger row has %d cells, want 5: %q", i+1, len(cells), line)
			continue
		}
		rows = append(rows, ledgerRow{pkg: unquote(cells[0]), law: unquote(cells[1]), statement: unquote(cells[2]), runner: unquote(cells[3]), state: cells[4], line: i + 1})
	}
	if !header {
		t.Fatal("stdlib.md has no Std laws table")
	}
	return rows
}

// splitRow splits a Markdown table row at each unescaped |, and unescapes
// \| in each cell.
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
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

func unquote(cell string) string {
	if len(cell) >= 2 && cell[0] == '`' && cell[len(cell)-1] == '`' {
		return cell[1 : len(cell)-1]
	}
	return cell
}

var spaces = regexp.MustCompile(`\s+`)

// lawStatement is the source text of d from its name to its closing
// brace, with each run of white space made one space: the Statement cell
// of its ledger row.
func lawStatement(f *syntax.File, d *syntax.LawDecl) string {
	return spaces.ReplaceAllString(f.Src[d.Name.Pos():d.Body.End()], " ")
}

// sourceLaws are the laws of the std sources by ledger key, "pkg Law" for
// a top-level law and "pkg Type.Law" for a member, with their statements.
func sourceLaws(t *testing.T, pkgs []stdLawPackage) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(key, statement string) {
		if _, dup := out[key]; dup {
			t.Errorf("std law %s is declared twice", key)
		}
		out[key] = statement
	}
	for _, p := range pkgs {
		for _, f := range p.files {
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *syntax.LawDecl:
					add(p.path+" "+d.Name.Name, lawStatement(f, d))
				case *syntax.TypeDecl:
					if it, ok := d.Spec.(*syntax.InterfaceType); ok {
						for _, l := range it.Laws {
							add(p.path+" "+d.Name.Name+"."+l.Name.Name, lawStatement(f, l))
						}
					}
				}
			}
		}
		// The parse and the checker agree on the laws, so no law the
		// checker records escapes the ledger.
		for _, l := range p.pkg.Laws {
			key := p.path + " " + l.Name
			if l.Owner != nil {
				key = p.path + " " + l.Owner.Name + "." + l.Name
			}
			if _, ok := out[key]; !ok {
				t.Errorf("the checker records law %s, which the source walk missed", key)
			}
		}
	}
	return out
}

// TestStdLawLedger: each law of std source has exactly one ledger row, each
// row names a law of std source, the row's statement is the law's source
// text, its runner is a Go test that the repository declares (or #162 for a
// law no Go test can run yet), and its state follows from the runner.
func TestStdLawLedger(t *testing.T) {
	laws := sourceLaws(t, stdLawPackages(t))
	tests := repoTests(t)
	rows := map[string]bool{}
	for _, r := range readLedger(t) {
		key := r.pkg + " " + r.law
		if rows[key] {
			t.Errorf("stdlib.md:%d: law %s has a second row", r.line, key)
		}
		rows[key] = true
		statement, ok := laws[key]
		if !ok {
			t.Errorf("stdlib.md:%d: row %s names no law of std source", r.line, key)
			continue
		}
		if r.statement != statement {
			t.Errorf("stdlib.md:%d: the statement of %s is\n\t%s\nthe source says\n\t%s", r.line, key, r.statement, statement)
		}
		switch {
		case r.runner == "#162":
			if r.state != "checked" {
				t.Errorf("stdlib.md:%d: %s has no Go runner, so its state is checked, not %q", r.line, key, r.state)
			}
		case !tests[r.runner]:
			t.Errorf("stdlib.md:%d: the runner %s of %s is no func Test… of the repository", r.line, r.runner, key)
		case r.state != "tested":
			t.Errorf("stdlib.md:%d: %s runs in %s, so its state is tested, not %q", r.line, key, r.runner, r.state)
		}
	}
	for key := range laws {
		if !rows[key] {
			t.Errorf("std law %s has no row in the Std laws table of docs/spec/stdlib.md", key)
		}
	}
}

// repoTests are the names of the top-level func Test… declarations of the
// repository's _test.go files, testdata left out.
func repoTests(t *testing.T) map[string]bool {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no module root at %s: %v", root, err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); p != root && (n == "testdata" || strings.HasPrefix(n, ".") || n == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				out[fn.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out["TestStdLawLedger"] {
		t.Fatal("the scan missed this test; it reads no test file")
	}
	return out
}

// TestStdHostLawsHold runs the member laws of the host packages against
// their host provider, through the Go bodies that the provider's Ergo
// functions call (os.Host's Getenv is hostHasEnv then hostGetenv; fs.Host
// calls FSReadFile and FSMkdirAll), over seeded cases. It is the ledger's
// runner for os.Process.GetenvIsStable, fs.Files.ReadIsRepeatable and
// fs.Writes.MkdirAllIsIdempotent.
func TestStdHostLawsHold(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	dir := t.TempDir()
	// GetenvIsStable(key): Getenv(key) == Getenv(key), set keys, empty
	// values and unset keys alike.
	getenv := func(key string) (string, bool) {
		if v1.OSHasEnv(key) {
			return v1.OSGetenv(key), true
		}
		return "", false
	}
	t.Setenv("ERGO_LAW_SET", "value")
	t.Setenv("ERGO_LAW_EMPTY", "")
	keys := []string{"ERGO_LAW_SET", "ERGO_LAW_EMPTY", "ERGO_LAW_UNSET_7f3a", "", "="}
	for range 200 {
		keys = append(keys, randomText(r))
	}
	for _, key := range keys {
		v, ok := getenv(key)
		w, ok2 := getenv(key)
		if v != w || ok != ok2 {
			t.Errorf("GetenvIsStable(%q): %q,%v then %q,%v", key, v, ok, w, ok2)
		}
	}
	// ReadIsRepeatable(path): with no writer, two reads agree in value and
	// in failure.
	if err := os.WriteFile(filepath.Join(dir, "doc"), []byte("one\ntwo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "locked"), []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths := []string{"doc", "locked", "sub", "missing", "", "doc/x"}
	for range 50 {
		paths = append(paths, randomText(r))
	}
	for _, p := range paths {
		p = filepath.Join(dir, p)
		a, errA := v1.FSReadFile(p)
		b, errB := v1.FSReadFile(p)
		if a != b || !sameFailure(errA, errB) {
			t.Errorf("ReadIsRepeatable(%q): %q, %v then %q, %v", p, a, errA, b, errB)
		}
	}
	// MkdirAllIsIdempotent(path): when MkdirAll(path) succeeds, a second
	// MkdirAll(path) succeeds.
	for _, p := range append([]string{"a/b/c", "sub", "doc", "doc/x", ""}, paths[6:]...) {
		p = filepath.Join(dir, "mk", p)
		if err := v1.FSMkdirAll(p); err != nil {
			continue
		}
		if err := v1.FSMkdirAll(p); err != nil {
			t.Errorf("MkdirAllIsIdempotent(%q): the second MkdirAll failed: %v", p, err)
		}
	}
}

// sameFailure reports whether two results of a body fail alike: both
// succeed, or both fail with equal failure values.
func sameFailure(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return errors.Is(a, b) || a == b
}

// randomText is a short text over an alphabet with separators, a
// multi-byte rune and an invalid byte.
func randomText(r *rand.Rand) string {
	alphabet := []string{"a", "Z", "_", "/", ".", "..", "=", " ", "é", "\xff", "0"}
	var b strings.Builder
	for range r.IntN(6) {
		b.WriteString(alphabet[r.IntN(len(alphabet))])
	}
	return b.String()
}
