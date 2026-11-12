package irspike

import (
	"go/ast"
	"go/parser"
	"go/token"
	"iter"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"
)

// A split cuts the domain of one large test into parts, as the checker's
// and the lowering's splits do (internal/sketch/lower, split_test.go): each
// part is a top-level test of its own, so the gate's duration shards
// spread the parts over processes and no part runs long. The IR tests of
// this package all build Go, so a split here has no short subset: its
// parts skip under -short with needsBuild.
//
// Part k of a split named N is the test function NPart<k>, and its body is
// the one statement `<split>.Part(t, k)`. Part k runs the cases whose index
// i in the domain has i % parts == k, in index order. No domain is pinned
// by a count or a digest (AGENTS.md, Tests): TestSplitsCoverTheirDomain
// checks that the declared parts run every case of the domain once.
type split[C any] struct {
	// name is the name of the test that the parts replace.
	name  string
	parts int
	// cases yields the domain, in index order.
	cases func(t *testing.T) []C
	// key is a stable text of one case.
	key func(C) string
	// run checks one case.
	run func(t *testing.T, c C)
}

// splits are the package's splits, by newSplit.
var splits []splitEntry

type splitEntry interface {
	splitName() string
	splitParts() int
	keys(t *testing.T) []string
	// ran is the keys of the cases that part k runs, by share, the path
	// Part takes.
	ran(t *testing.T, k int) []string
}

// newSplit registers s for TestSplitsCoverTheirDomain.
func newSplit[C any](s split[C]) *split[C] {
	p := &s
	splits = append(splits, p)
	return p
}

func (s *split[C]) splitName() string { return s.name }
func (s *split[C]) splitParts() int   { return s.parts }

func (s *split[C]) keys(t *testing.T) []string {
	var out []string
	for _, c := range s.cases(t) {
		out = append(out, s.key(c))
	}
	return out
}

func (s *split[C]) ran(t *testing.T, k int) []string {
	var out []string
	for c := range s.share(t, k) {
		out = append(out, s.key(c))
	}
	return out
}

// Part runs part k of the split: each of its cases in a subtest named by
// its key.
func (s *split[C]) Part(t *testing.T, k int) {
	t.Helper()
	if want := s.name + "Part" + strconv.Itoa(k); t.Name() != want {
		t.Fatalf("part %d of %s runs in %s, want %s", k, s.name, t.Name(), want)
	}
	if k < 0 || k >= s.parts {
		t.Fatalf("%s has parts 0 to %d, not %d", s.name, s.parts-1, k)
	}
	if testing.Short() {
		t.Skipf("%s builds Go: full tier only", s.name)
	}
	for c := range s.share(t, k) {
		t.Run(s.key(c), func(t *testing.T) { s.run(t, c) })
	}
}

// share yields the cases of part k, in domain order.
func (s *split[C]) share(t *testing.T, k int) iter.Seq[C] {
	return func(yield func(C) bool) {
		for i, c := range s.cases(t) {
			mine := i%s.parts == k
			if mine && !yield(c) {
				return
			}
		}
	}
}

var partTestName = regexp.MustCompile(`^(Test\w+)Part(\d+)$`)

// declaredParts reads the package's test files and returns, for each
// split name, the parts whose test functions exist. A function named like
// a part must be exactly `<split>.Part(t, k)`.
func declaredParts(t *testing.T) map[string][]int {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files: %v", err)
	}
	out := map[string][]int{}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			m := partTestName.FindStringSubmatch(fn.Name.Name)
			if m == nil {
				continue
			}
			k, _ := strconv.Atoi(m[2])
			if !isPartCall(fn, m[2]) {
				t.Errorf("%s: %s must be the one statement `<split>.Part(t, %d)`", fset.Position(fn.Pos()), fn.Name.Name, k)
				continue
			}
			out[m[1]] = append(out[m[1]], k)
		}
	}
	return out
}

func isPartCall(fn *ast.FuncDecl, k string) bool {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return false
	}
	stmt, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Part" {
		return false
	}
	tArg, ok := call.Args[0].(*ast.Ident)
	lit, ok2 := call.Args[1].(*ast.BasicLit)
	return ok && ok2 && tArg.Name == "t" && lit.Kind == token.INT && lit.Value == k
}

// TestSplitsCoverTheirDomain: for every split, the parts that exist run
// every case of the domain once, and the domain is not empty. It runs in
// the fast tier: the domains are the example folders, no build. Mutants:
// a part leaves out a case of its share (irt-split-case-dropped); a part
// is missing (irt-split-part-missing).
func TestSplitsCoverTheirDomain(t *testing.T) {
	declared := declaredParts(t)
	names := map[string]bool{}
	for _, s := range splits {
		name := s.splitName()
		if names[name] {
			t.Errorf("two splits are named %s", name)
		}
		names[name] = true
		t.Run(name, func(t *testing.T) { coversDomain(t, s, declared[name]) })
	}
	for name := range declared {
		if !names[name] {
			t.Errorf("%sPart tests exist, but no split is named %s", name, name)
		}
	}
	if len(splits) == 0 {
		t.Fatal("no splits registered")
	}
}

// coversDomain checks that the declared parts of s run its domain, each
// case once.
func coversDomain(t *testing.T, s splitEntry, declared []int) {
	name := s.splitName()
	keys := s.keys(t)
	index := map[string]int{}
	for i, key := range keys {
		if j, ok := index[key]; ok {
			t.Fatalf("%s: cases %d and %d have one key; the coverage check needs distinct keys", name, j, i)
		}
		index[key] = i
	}
	ks := slices.Clone(declared)
	slices.Sort(ks)
	seen := make([]int, len(keys))
	for _, k := range ks {
		if k >= s.splitParts() {
			t.Errorf("%sPart%d: %s has %d parts", name, k, name, s.splitParts())
			continue
		}
		for _, key := range s.ran(t, k) {
			i, ok := index[key]
			if !ok {
				t.Errorf("%sPart%d runs a case outside the domain", name, k)
				continue
			}
			seen[i]++
		}
	}
	for k := range s.splitParts() {
		if !slices.Contains(ks, k) {
			t.Errorf("%s: no test %sPart%d", name, name, k)
		}
	}
	for i, n := range seen {
		if n != 1 {
			t.Errorf("%s: case %s runs %d times", name, keys[i], n)
		}
	}
	if len(keys) == 0 {
		t.Errorf("%s: the domain is empty", name)
	}
}
