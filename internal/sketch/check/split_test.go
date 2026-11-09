package check

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"iter"
	"math/rand"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
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
// TestSplitsCoverTheirDomain checks that the declared parts together run
// every case of the domain that Cases yields exactly once. So a dropped
// part or a part that runs another part's cases fails that test. The
// domain has no pinned count or digest (AGENTS.md, Tests): a change of the
// generator is a change of the test, reviewed as one.
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
	keys() []string
	ran(t *testing.T, k int, short bool) []string
}

// NewSplit registers s for TestSplitsCoverTheirDomain.
func NewSplit[C any](s Split[C]) *Split[C] {
	p := &s
	splits = append(splits, p)
	return p
}

func (s *Split[C]) name() string { return s.Name }
func (s *Split[C]) parts() int   { return s.Parts }

func (s *Split[C]) keys() []string {
	var out []string
	for c := range s.Cases {
		out = append(out, s.Key(c))
	}
	return out
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
	if !s.dispatch(t, k, testing.Short(), s.Run) {
		t.Skipf("%s runs in full mode only (the gate's full tier)", s.Name)
	}
}

// dispatch hands the cases of part k (under short its fixed subset) to
// run: all of them at once, or dealt to Workers parallel subtests. It is
// the one path from the domain to a part's checks: Part passes the
// split's Run, and TestSplitsCoverTheirDomain and TestSplitHarness pass a
// recorder, so they see the cases that Run is given. It returns false, and
// runs nothing, for a part that skips under short.
func (s *Split[C]) dispatch(t *testing.T, k int, short bool, run func(*testing.T, iter.Seq[C])) bool {
	if short && s.Short == 0 {
		return false
	}
	if s.Workers <= 1 {
		run(t, s.cases(k, 0, 1, short))
		return true
	}
	for w := range s.Workers {
		t.Run(strconv.Itoa(w), func(t *testing.T) {
			t.Parallel()
			run(t, s.cases(k, w, s.Workers, short))
		})
	}
	return true
}

// cases yields the cases of part k (under short its fixed subset) whose
// position among them is w modulo workers.
func (s *Split[C]) cases(k, w, workers int, short bool) iter.Seq[C] {
	return func(yield func(C) bool) {
		// i counts the domain, j the part, m the kept cases of the part.
		i, j, m := 0, 0, 0
		for c := range s.Cases {
			mine := i%s.Parts == k
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
			if dealt && !yield(c) {
				return
			}
		}
	}
}

// ran is the keys of the cases that part k hands to Run, over all its
// workers, through dispatch.
func (s *Split[C]) ran(t *testing.T, k int, short bool) []string {
	var mu sync.Mutex
	var out []string
	// The subtest returns once its parallel workers are done.
	t.Run(fmt.Sprintf("part%d", k), func(t *testing.T) {
		s.dispatch(t, k, short, func(_ *testing.T, cases iter.Seq[C]) {
			for c := range cases {
				key := s.Key(c)
				mu.Lock()
				out = append(out, key)
				mu.Unlock()
			}
		})
	})
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
// the parts that exist run every case of the full domain once. It reads the cases each part hands to Run
// through dispatch, the path Part takes.
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

// TestSplitHarness (property): over generated domain sizes, part counts,
// worker counts and short strides, the cases that dispatch hands to Run
// are, in full mode, a partition of the domain with part k holding the
// indexes i % Parts == k, each worker's in domain order; and under short,
// exactly the cases at positions 0, Short, 2*Short, … of each part, or
// none when Short is 0. Part itself, run with a recording Run, hands over
// the same cases in the test's own mode. The model below is written from
// the Split doc, not from cases.
func TestSplitHarness(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	type config struct{ n, parts, workers, short int }
	var configs []config
	for _, n := range []int{0, 1, 2, 7, 13} {
		for _, parts := range []int{1, 2, 3, 5} {
			configs = append(configs, config{n, parts, 0, 1}, config{n, parts, 3, 2})
		}
	}
	for range 60 {
		configs = append(configs, config{r.Intn(60), 1 + r.Intn(9), r.Intn(6), r.Intn(5)})
	}
	for _, c := range configs {
		s := &Split[int]{
			Name: "TestToy", Parts: c.parts, Short: c.short, Workers: c.workers,
			Cases: func(yield func(int) bool) {
				for i := range c.n {
					if !yield(i) {
						return
					}
				}
			},
			Key: strconv.Itoa,
		}
		t.Run(fmt.Sprintf("n=%d,parts=%d,workers=%d,short=%d", c.n, c.parts, c.workers, c.short), func(t *testing.T) {
			seen := make([]int, c.n)
			for k := range c.parts {
				// The model: the part's indexes, then the short subset.
				var part, short []string
				for i := k; i < c.n; i += c.parts {
					part = append(part, strconv.Itoa(i))
				}
				for j := 0; c.short > 0 && j < len(part); j += c.short {
					short = append(short, part[j])
				}
				for _, mode := range []struct {
					short bool
					want  []string
				}{{false, part}, {true, short}} {
					got := s.ran(t, k, mode.short)
					if c.workers > 1 {
						// Workers run in parallel: compare as sets, and
						// check each worker's order on its own below.
						slices.SortFunc(got, func(a, b string) int { return atoi(a) - atoi(b) })
					}
					if !slices.Equal(got, mode.want) && !(len(got) == 0 && len(mode.want) == 0) {
						t.Errorf("part %d short=%v: ran %v, want %v", k, mode.short, got, mode.want)
					}
					if !mode.short {
						for _, key := range got {
							seen[atoi(key)]++
						}
					}
				}
				// Part itself, with a Run that records, in this test's
				// mode: the same cases as the model's.
				var mu sync.Mutex
				var got []string
				s.Name = t.Name() + "/TestToy"
				s.Run = func(_ *testing.T, cases iter.Seq[int]) {
					for i := range cases {
						mu.Lock()
						got = append(got, strconv.Itoa(i))
						mu.Unlock()
					}
				}
				t.Run(fmt.Sprintf("TestToyPart%d", k), func(t *testing.T) { s.Part(t, k) })
				slices.SortFunc(got, func(a, b string) int { return atoi(a) - atoi(b) })
				want := part
				if testing.Short() {
					want = short
				}
				if !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
					t.Errorf("Part(t, %d) ran %v, want %v", k, got, want)
				}
				for w := range max(c.workers, 1) {
					var order []int
					for i := range s.cases(k, w, max(c.workers, 1), false) {
						order = append(order, i)
					}
					if !slices.IsSorted(order) {
						t.Errorf("part %d worker %d: %v out of domain order", k, w, order)
					}
				}
			}
			for i, n := range seen {
				if n != 1 {
					t.Errorf("case %d runs %d times in full mode", i, n)
				}
			}
		})
	}
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}

// coversDomain checks that the declared parts of s run its domain, each
// case once.
func coversDomain(t *testing.T, s splitEntry, declared []int) {
	name := s.name()
	keys := s.keys()
	index := map[string]int{}
	for i, key := range keys {
		if _, dup := index[key]; dup {
			t.Fatalf("%s: cases %d and %d have one key", name, index[key], i)
		}
		index[key] = i
	}
	ks := slices.Clone(declared)
	slices.Sort(ks)
	var idx []int
	for _, k := range ks {
		if k >= s.parts() {
			t.Errorf("%sPart%d: %s has %d parts", name, k, name, s.parts())
			continue
		}
		for _, key := range s.ran(t, k, false) {
			i, ok := index[key]
			if !ok {
				t.Errorf("%sPart%d runs a case outside the domain", name, k)
				continue
			}
			idx = append(idx, i)
		}
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
	if len(keys) == 0 {
		t.Errorf("%s: the domain is empty", name)
	}
	if len(union) != len(keys) {
		t.Errorf("%s: the parts run %d of the domain's %d cases", name, len(union), len(keys))
	}
}
