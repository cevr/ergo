package std_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
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

// readLedger is the Std laws table of stdlib.md (lawledger.Read).
func readLedger(t *testing.T) []lawledger.Row {
	t.Helper()
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	return rows
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
// text, its runner is a Go test that a runner package declares and that runs
// its laws through lawledger.Run (or #162 for a law no Go test can run
// yet), and its state follows from the runner. lawledger.Run then holds,
// when the runner runs, that it restates each of its rows' laws with the
// row's statement and runs it.
func TestStdLawLedger(t *testing.T) {
	laws := sourceLaws(t, stdLawPackages(t))
	tests := repoTests(t)
	rows := map[string]bool{}
	for _, r := range readLedger(t) {
		key := r.Key()
		if rows[key] {
			t.Errorf("stdlib.md:%d: law %s has a second row", r.Line, key)
		}
		rows[key] = true
		statement, ok := laws[key]
		if !ok {
			t.Errorf("stdlib.md:%d: row %s names no law of std source", r.Line, key)
			continue
		}
		if r.Statement != statement {
			t.Errorf("stdlib.md:%d: the statement of %s is\n\t%s\nthe source says\n\t%s", r.Line, key, r.Statement, statement)
		}
		runsLaws, declared := tests[r.Runner]
		switch {
		case r.Runner == "#162":
			if r.State != "checked" {
				t.Errorf("stdlib.md:%d: %s has no Go runner, so its state is checked, not %q", r.Line, key, r.State)
			}
		case !declared:
			t.Errorf("stdlib.md:%d: the runner %s of %s is no func Test… of the runner packages %v", r.Line, r.Runner, key, runnerPackages)
		case !runsLaws:
			t.Errorf("stdlib.md:%d: the runner %s of %s does not call lawledger.Run, so nothing holds that it runs the law", r.Line, r.Runner, key)
		case r.State != "tested":
			t.Errorf("stdlib.md:%d: %s runs in %s, so its state is tested, not %q", r.Line, key, r.Runner, r.State)
		}
	}
	for key := range laws {
		if !rows[key] {
			t.Errorf("std law %s has no row in the Std laws table of docs/spec/stdlib.md", key)
		}
	}
}

// runnerPackages are the directories, from the module root, whose tests
// may run std laws. repoTests reads only their test files (a glob each,
// one level, so the closure sweep can declare the read), not the whole
// tree: a runner in another package is "no func Test… of the runner
// packages" until its directory is added here and to the sweep's grants
// (internal/closure declared; batch-10 gate 1).
var runnerPackages = []string{"internal/sketch/lower", "internal/sketch/std", "runtime/v1"}

// repoTests are the names of the top-level func Test… declarations of the
// _test.go files of runnerPackages, each with whether its body calls
// lawledger.Run.
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
	for _, dir := range runnerPackages {
		paths, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			t.Fatalf("the runner package %s has no test file", dir)
		}
		for _, p := range paths {
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
					out[fn.Name.Name] = out[fn.Name.Name] || callsLedgerRun(fn)
				}
			}
		}
	}
	if _, ok := out["TestStdLawLedger"]; !ok {
		t.Fatal("the scan missed this test; it reads no test file")
	}
	if !out["TestStdHostLawsHold"] {
		t.Fatal("the scan sees no call of lawledger.Run in TestStdHostLawsHold, which makes one")
	}
	return out
}

// TestCallsLedgerRun: a runner is a test whose own body calls
// lawledger.Run with its own t; a test that calls nothing, another Run,
// or lawledger.Run with another t is not.
func TestCallsLedgerRun(t *testing.T) {
	const src = `package p

func TestRunner(t *testing.T) { lawledger.Run(t, laws) }
func TestNested(t *testing.T) { t.Run("x", func(t *testing.T) {}); if ok { lawledger.Run(t, map[string]lawledger.Law{}) } }
func TestLawless(t *testing.T) { check(t) }
func TestOtherRun(t *testing.T) { t.Run("lawledger", nil) }
func TestOtherT(t *testing.T) { lawledger.Run(other, laws) }
func TestNoParam() { lawledger.Run(t, laws) }
`
	f, err := parser.ParseFile(token.NewFileSet(), "p_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"TestRunner": true, "TestNested": true, "TestLawless": false, "TestOtherRun": false, "TestOtherT": false, "TestNoParam": false}
	for _, decl := range f.Decls {
		fn := decl.(*ast.FuncDecl)
		if got := callsLedgerRun(fn); got != want[fn.Name.Name] {
			t.Errorf("callsLedgerRun(%s) = %v, want %v", fn.Name.Name, got, want[fn.Name.Name])
		}
	}
}

// callsLedgerRun reports whether fn's body calls lawledger.Run with fn's
// own t as its first argument.
func callsLedgerRun(fn *ast.FuncDecl) bool {
	if fn.Body == nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) != 1 {
		return false
	}
	param := fn.Type.Params.List[0].Names[0].Name
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" || len(call.Args) != 2 {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		arg, isIdent := call.Args[0].(*ast.Ident)
		found = ok && pkg.Name == "lawledger" && isIdent && arg.Name == param
		return !found
	})
	return found
}

// TestStdHostLawsHold runs the member laws of the host packages against
// their host provider, through the Go bodies that the provider's Ergo
// functions call (os.Host's Getenv is hostHasEnv then hostGetenv; fs.Host
// calls FSReadFile, FSMkdirAll, FSWriteFile, FSRemove and FSRename), over
// seeded cases. It is the ledger's runner for os.Process.GetenvIsStable;
// it runs fs.Files.ReadIsRepeatable, fs.Writes.MkdirAllIsIdempotent,
// fs.Writes.RemoveThenNotFound and fs.Writes.RenameMoves on the host's
// edge cases as plain subtests.
func TestStdHostLawsHold(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	dir := t.TempDir()
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
	laws := map[string]lawledger.Law{
		// Set keys, empty values and unset keys alike.
		"os Process.GetenvIsStable": {Statement: `GetenvIsStable(key string) { Getenv(key) == Getenv(key) }`, Check: func(t *testing.T) {
			for _, key := range keys {
				v, ok := getenv(key)
				w, ok2 := getenv(key)
				if v != w || ok != ok2 {
					t.Errorf("GetenvIsStable(%q): %q,%v then %q,%v", key, v, ok, w, ok2)
				}
			}
		}},
		// With no writer, two reads agree in value and in failure.
		"fs Files.ReadIsRepeatable": {Statement: `ReadIsRepeatable(path string) { let first = try ReadFile(path) try ReadFile(path) == first }`, Check: func(t *testing.T) {
			for _, p := range paths {
				p = filepath.Join(dir, p)
				a, errA := v1.FSReadFile(p)
				b, errB := v1.FSReadFile(p)
				if a != b || !sameFailure(errA, errB) {
					t.Errorf("ReadIsRepeatable(%q): %q, %v then %q, %v", p, a, errA, b, errB)
				}
			}
		}},
		// When MkdirAll(path) succeeds, a second MkdirAll(path) succeeds.
		"fs Writes.MkdirAllIsIdempotent": {Statement: `MkdirAllIsIdempotent(path string) { MkdirAll(path) catch { case Denied, Failed: return true } try MkdirAll(path) true }`, Check: func(t *testing.T) {
			for _, p := range append([]string{"a/b/c", "sub", "doc", "doc/x", ""}, paths[6:]...) {
				p = filepath.Join(dir, "mk", p)
				if err := v1.FSMkdirAll(p); err != nil {
					continue
				}
				if err := v1.FSMkdirAll(p); err != nil {
					t.Errorf("MkdirAllIsIdempotent(%q): the second MkdirAll failed: %v", p, err)
				}
			}
		}},
		// When Remove(path) succeeds, a second Remove(path) fails NotFound.
		"fs Writes.RemoveThenNotFound": {Statement: `RemoveThenNotFound(path string) { Remove(path) catch { case NotFound, Denied, Failed: return true } Remove(path) catch { case NotFound: return true case Denied, Failed: return false } false }`, Check: func(t *testing.T) {
			rm := filepath.Join(dir, "rm")
			removed := 0
			for i, p := range append([]string{"f", "d", "full", "link", "", "f/x", "d/"}, paths[6:]...) {
				base := filepath.Join(rm, strconv.Itoa(i))
				for _, err := range []error{os.MkdirAll(filepath.Join(base, "d"), 0o755), os.MkdirAll(filepath.Join(base, "full", "in"), 0o755), os.WriteFile(filepath.Join(base, "f"), []byte("x"), 0o644), os.Symlink("f", filepath.Join(base, "link"))} {
					if err != nil {
						t.Fatal(err)
					}
				}
				p = base + "/" + p
				if v1.FSRemove(p) != nil {
					continue
				}
				if err := v1.FSRemove(p); err != (v1.NotFound{Path: p}) {
					t.Errorf("RemoveThenNotFound(%q): the second Remove gave %#v", p, err)
				}
				removed++
			}
			if removed < 4 {
				t.Errorf("only %d cases reached the second Remove", removed)
			}
		}},
		// When a file is at from and none at to, and Rename(from, to)
		// succeeds, a second Rename(from, to) fails NotFound.
		"fs Writes.RenameMoves": {Statement: `RenameMoves(from string, to string, text string) { if from == to { return true } WriteFile(from, text) catch { case NotFound, Denied, Failed: return true } Remove(to) catch { case NotFound: case Denied, Failed: return true } Rename(from, to) catch { case NotFound, Denied, Failed: return true } Rename(from, to) catch { case NotFound: return true case Denied, Failed: return false } false }`, Check: func(t *testing.T) {
			mv := filepath.Join(dir, "mv")
			moved := 0
			names := append([]string{"a", "b", "./a", "sub", "sub/a", "link", "a/", "d", "d/x"}, paths[6:]...)
			for i := range 300 {
				base := filepath.Join(mv, strconv.Itoa(i))
				for _, err := range []error{os.MkdirAll(filepath.Join(base, "sub"), 0o755), os.MkdirAll(filepath.Join(base, "d"), 0o755), os.Symlink("b", filepath.Join(base, "link"))} {
					if err != nil {
						t.Fatal(err)
					}
				}
				from, to := names[r.IntN(len(names))], names[r.IntN(len(names))]
				text := randomText(r)
				if from == to {
					continue
				}
				f, g := base+"/"+from, base+"/"+to
				if v1.FSWriteFile(f, text) != nil {
					continue
				}
				if err := v1.FSRemove(g); err != nil && err != (v1.NotFound{Path: g}) {
					continue
				}
				if v1.FSRename(f, g) != nil {
					continue
				}
				if err := v1.FSRename(f, g); err != (v1.NotFound{Path: f}) {
					t.Errorf("RenameMoves(%q, %q): the second Rename gave %#v", from, to, err)
				}
				moved++
			}
			// The law is not vacuous: most cases reach the second Rename.
			if moved < 100 {
				t.Errorf("only %d of 300 cases reached the second Rename", moved)
			}
		}},
	}
	// The ledger runner of fs's laws is lower TestFSMemoryAgreesWithHost,
	// which runs each against Host and Memory alike. The checks here stay:
	// they reach what Memory has not (symbolic links, a file no one may
	// read, paths with "..").
	for _, key := range slices.Sorted(maps.Keys(laws)) {
		if strings.HasPrefix(key, "fs ") {
			t.Run("host."+strings.ReplaceAll(key, " ", "."), laws[key].Check)
			delete(laws, key)
		}
	}
	lawledger.Run(t, laws)
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
