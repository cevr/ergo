package check

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// A Split cuts the case domain of one large test into parts. Each part is
// a top-level test of its own, so the gate's duration shards can spread
// the parts over processes, and no part runs long (gate-fast plan T1).
//
// Part k of a split named N is the test function NPart<k>, and its body is
// the one statement `<split>.Part(t, k)`. Part k runs the cases whose index
// i in the full domain has i % Parts == k, in index order.
//
// The full domain is pinned: Count is its number of cases, and Digest is
// the SHA-256 of the case keys, each written as "<len>:<key>\n", in index
// order. TestSplitsCoverTheirDomain checks that the declared parts together
// run exactly that domain. So a dropped part, a part that runs another
// part's cases, or a generator that draws other cases fails that test.
//
// Under -short a part runs the cases at positions 0, Short, 2*Short, … of
// its share, or none when Short is 0. The seeds never change: the short
// cases are a fixed subset of the full domain, so a fast-tier result is a
// function of the source alone, and every short case is also a full case.
type Split[C any] struct {
	// Name is the name of the test that the parts replace.
	Name  string
	Parts int
	// Short is the stride of the cases a part keeps under -short; 0 skips
	// the part under -short.
	Short int
	// Workers, when more than 1, deals the cases of a part to that many
	// parallel subtests.
	Workers int
	// Count and Digest pin the full domain.
	Count  int
	Digest string
	// Pinned, when set and false, leaves the pin unchecked: the test runs
	// another domain on purpose (ERGO_CYCLE_GEN=full).
	Pinned func() bool
	// Cases yields the full domain, in index order. A case is used only
	// while it is yielded, so a generator may reuse its memory.
	Cases iter.Seq[C]
	// Key is a stable text of one case: what it checks, with its inputs.
	Key func(C) string
	// Run checks the cases of a part. It may range over them more than
	// once; each range generates them again.
	Run func(t *testing.T, cases iter.Seq[C])
}

// SeqOf yields the cases that f returns, calling f on each range.
func SeqOf[C any](f func() []C) iter.Seq[C] {
	return func(yield func(C) bool) {
		for _, c := range f() {
			if !yield(c) {
				return
			}
		}
	}
}

// splits are the splits of the package, by NewSplit.
var splits []splitEntry

type splitEntry interface {
	name() string
	parts() int
	pin() (count int, digest string, checked bool)
	keys() []string
	partIndexes(k int) []int
}

// NewSplit registers s for TestSplitsCoverTheirDomain.
func NewSplit[C any](s Split[C]) *Split[C] {
	p := &s
	splits = append(splits, p)
	return p
}

func (s *Split[C]) name() string { return s.Name }
func (s *Split[C]) parts() int   { return s.Parts }

func (s *Split[C]) pin() (int, string, bool) {
	return s.Count, s.Digest, s.Pinned == nil || s.Pinned()
}

func (s *Split[C]) keys() []string {
	var out []string
	for c := range s.Cases {
		out = append(out, s.Key(c))
	}
	return out
}

// splitDigest is the pin of a list of case keys.
func splitDigest(keys []string) string {
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s\n", len(k), k)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Part runs part k of the split.
func (s *Split[C]) Part(t *testing.T, k int) {
	t.Helper()
	if want := s.Name + "Part" + strconv.Itoa(k); t.Name() != want {
		t.Fatalf("part %d of %s runs in %s, want %s", k, s.Name, t.Name(), want)
	}
	if k < 0 || k >= s.Parts {
		t.Fatalf("%s has parts 0 to %d, not %d", s.Name, s.Parts-1, k)
	}
	if testing.Short() && s.Short == 0 {
		t.Skipf("%s runs in full mode only (the gate's full tier)", s.Name)
	}
	if s.Workers <= 1 {
		s.Run(t, s.cases(k, 0, 1, testing.Short()))
		return
	}
	for w := range s.Workers {
		t.Run(strconv.Itoa(w), func(t *testing.T) {
			t.Parallel()
			s.Run(t, s.cases(k, w, s.Workers, testing.Short()))
		})
	}
}

func (s *Split[C]) cases(k, w, workers int, short bool) iter.Seq[C] {
	return func(yield func(C) bool) {
		for _, c := range s.share(k, w, workers, short) {
			if !yield(c) {
				return
			}
		}
	}
}

// share yields the cases of part k (under short its fixed subset) whose
// position among them is w modulo workers, with their domain indexes.
func (s *Split[C]) share(k, w, workers int, short bool) iter.Seq2[int, C] {
	return func(yield func(int, C) bool) {
		// i counts the domain, j the part, m the kept cases of the part.
		i, j, m := 0, 0, 0
		for c := range s.Cases {
			index, mine := i, i%s.Parts == k
			i++
			if !mine {
				continue
			}
			kept := !short || j%s.Short == 0
			j++
			if !kept {
				continue
			}
			dealt := m%workers == w
			m++
			if dealt && !yield(index, c) {
				return
			}
		}
	}
}

// partIndexes is the domain indexes that part k runs in full mode, over
// all its workers.
func (s *Split[C]) partIndexes(k int) []int {
	var out []int
	for w := range max(s.Workers, 1) {
		for i := range s.share(k, w, max(s.Workers, 1), false) {
			out = append(out, i)
		}
	}
	return out
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

// TestSplitsCoverTheirDomain (gate-fast T1, legs 1 and 3): for every split,
// the parts that exist run every case of the pinned full domain once; the
// pin is the domain the unsplit test ran (count and digest measured at
// 22b2e906, before the split).
func TestSplitsCoverTheirDomain(t *testing.T) {
	declared := declaredParts(t)
	names := map[string]bool{}
	for _, s := range splits {
		name := s.name()
		if names[name] {
			t.Errorf("two splits are named %s", name)
		}
		names[name] = true
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			coversDomain(t, s, declared[name])
		})
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

// coversDomain checks that the declared parts of s run its pinned domain,
// each case once.
func coversDomain(t *testing.T, s splitEntry, declared []int) {
	name := s.name()
	count, digest, checked := s.pin()
	if !checked {
		t.Skipf("%s: the pin is not checked in this mode", name)
	}
	keys := s.keys()
	ks := slices.Clone(declared)
	slices.Sort(ks)
	var idx []int
	for _, k := range ks {
		if k >= s.parts() {
			t.Errorf("%sPart%d: %s has %d parts", name, k, name, s.parts())
			continue
		}
		idx = append(idx, s.partIndexes(k)...)
	}
	slices.Sort(idx)
	var union []string
	for n, i := range idx {
		if n > 0 && idx[n-1] == i {
			t.Errorf("%s: case %d runs in two parts", name, i)
			continue
		}
		union = append(union, keys[i])
	}
	for k := range s.parts() {
		if !slices.Contains(ks, k) {
			t.Errorf("%s: no test %sPart%d", name, name, k)
		}
	}
	if len(keys) != count {
		t.Errorf("%s: the domain has %d cases, pinned %d", name, len(keys), count)
	}
	if got := splitDigest(union); len(union) != count || got != digest {
		t.Errorf("%s: the parts run %d cases with digest %s; the pinned domain is %d cases with digest %s", name, len(union), got, count, digest)
	}
}
