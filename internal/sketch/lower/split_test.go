package lower

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"iter"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"testing"
)

// A split cuts the case domain of one large test into parts (gate-fast
// plan T2, after check's T1). Each part is a top-level test of its own, so
// the gate's duration shards spread the parts over processes, and no part
// runs long. A part that builds programs builds its own module.
//
// Part k of a split named N is the test function NPart<k>, and its body is
// the one statement `<split>.Part(t, k)`. Part k runs the cases whose index
// i in the full domain has i % Parts == k, in index order.
//
// No domain is pinned by a count or a digest (AGENTS.md, Tests): a change
// of a generator is a change of the test, reviewed as one. The parts must
// cover the domain, each case once: TestSplitsCoverTheirDomain checks it,
// over the cases each part hands to its run through dispatch, the path
// Part takes. So a dropped part or a part that runs another part's cases
// fails that test; TestSplitHarness checks dispatch and Part against
// a model of these rules. Part also fails when run does not consume each
// of its cases once.
//
// Under -short a part runs the cases at positions 0, Short, 2*Short, … of
// its share, or none when Short is 0. The seeds never change: the short
// cases are a fixed subset of the full domain, so a fast-tier result is a
// function of the source alone, and every short case is also a full case.
type split[C any] struct {
	// name is the name of the test that the parts replace.
	name  string
	parts int
	// short is the stride of the cases a part keeps under -short; 0 skips
	// the part under -short.
	short int
	// cases yields the full domain, in index order.
	cases iter.Seq[C]
	// key is a stable text of one case: what it checks, with its inputs.
	key func(C) string
	// run checks the cases of a part. It ranges over them once, to the
	// end: Part fails a part whose run consumes fewer or more cases than
	// the part holds.
	run func(t *testing.T, cases iter.Seq[C])
}

// splits are the package's splits, by newSplit.
var splits []splitEntry

type splitEntry interface {
	splitName() string
	splitParts() int
	// keys is the key of each case of the full domain, in index order.
	keys() []string
	// ran is the keys of the cases that part k hands to run, through
	// dispatch, the path Part takes.
	ran(t *testing.T, k int, short bool) []string
}

// newSplit registers s for TestSplitsCoverTheirDomain.
func newSplit[C any](s split[C]) *split[C] {
	p := &s
	splits = append(splits, p)
	return p
}

func (s *split[C]) splitName() string { return s.name }
func (s *split[C]) splitParts() int   { return s.parts }

func (s *split[C]) keys() []string {
	var out []string
	for c := range s.cases {
		out = append(out, s.key(c))
	}
	return out
}

// ran records the keys of the cases that dispatch hands to run for part
// k, in a subtest, so a consumption error of dispatch fails the caller.
func (s *split[C]) ran(t *testing.T, k int, short bool) []string {
	var out []string
	t.Run(fmt.Sprintf("part%d", k), func(t *testing.T) {
		s.dispatch(t, k, short, func(_ *testing.T, cases iter.Seq[C]) {
			for c := range cases {
				out = append(out, s.key(c))
			}
		})
	})
	return out
}

// Part runs part k of the split.
func (s *split[C]) Part(t *testing.T, k int) {
	t.Helper()
	if want := s.name + "Part" + strconv.Itoa(k); t.Name() != want {
		t.Fatalf("part %d of %s runs in %s, want %s", k, s.name, t.Name(), want)
	}
	if k < 0 || k >= s.parts {
		t.Fatalf("%s has parts 0 to %d, not %d", s.name, s.parts-1, k)
	}
	if !s.dispatch(t, k, testing.Short(), s.run) {
		t.Skipf("%s runs in full mode only (the gate's full tier)", s.name)
	}
}

// dispatch hands the cases of part k (under short its fixed subset) to
// run, and fails t when run does not consume each of them once. It is the
// one path from the domain to a part's checks: Part passes the split's
// run, and TestSplitsCoverTheirDomain and TestSplitHarness pass a
// recorder, so they see the cases that run is given. It returns false, and
// runs nothing, for a part that skips under short.
func (s *split[C]) dispatch(t *testing.T, k int, short bool, run func(*testing.T, iter.Seq[C])) bool {
	if short && s.short == 0 {
		return false
	}
	cases := slices.Collect(s.share(k, short))
	consumed := 0
	run(t, func(yield func(C) bool) {
		for _, c := range cases {
			consumed++
			if !yield(c) {
				return
			}
		}
	})
	if consumed != len(cases) {
		t.Errorf("%s part %d: run consumed %d of the part's %d cases", s.name, k, consumed, len(cases))
	}
	return true
}

// share yields the cases of part k of the domain (under short its fixed
// subset), in domain order.
func (s *split[C]) share(k int, short bool) iter.Seq[C] {
	return func(yield func(C) bool) {
		// i counts the domain, j the part.
		i, j := 0, 0
		for c := range s.cases {
			mine := i%s.parts == k
			i++
			if !mine {
				continue
			}
			kept := !short || j%s.short == 0
			j++
			if kept && !yield(c) {
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

// TestSplitsCoverTheirDomain (gate-fast T2, legs 1 and 3): for every
// split, the parts that exist run every case of the full domain once.
func TestSplitsCoverTheirDomain(t *testing.T) {
	declared := declaredParts(t)
	names := map[string]bool{}
	for _, s := range splits {
		name := s.splitName()
		if names[name] {
			t.Errorf("two splits are named %s", name)
		}
		names[name] = true
		t.Run(name, func(t *testing.T) {
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

// TestSplitHarness (property): over generated domain sizes, part counts
// and short strides, the cases that dispatch hands to run are, in full
// mode, part k = the indexes i with i % parts == k in domain order, a
// partition of the domain; and under short, the cases at positions 0,
// short, 2*short, … of each part, or none when short is 0. Part itself,
// run with a recording run, hands over the same cases in the test's own
// mode. The model below is written from the split doc, not from share.
func TestSplitHarness(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	type config struct{ n, parts, short int }
	var configs []config
	for _, n := range []int{0, 1, 2, 7, 13} {
		for _, parts := range []int{1, 2, 3, 5} {
			configs = append(configs, config{n, parts, 0}, config{n, parts, 1}, config{n, parts, 2})
		}
	}
	for range 60 {
		configs = append(configs, config{r.Intn(60), 1 + r.Intn(9), r.Intn(5)})
	}
	for _, c := range configs {
		s := &split[int]{
			name: "TestToy", parts: c.parts, short: c.short,
			cases: func(yield func(int) bool) {
				for i := range c.n {
					if !yield(i) {
						return
					}
				}
			},
			key: strconv.Itoa,
		}
		t.Run(fmt.Sprintf("n=%d,parts=%d,short=%d", c.n, c.parts, c.short), func(t *testing.T) {
			seen := make([]int, c.n)
			for k := range c.parts {
				// The model: the part's indexes, then its short subset.
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
					if !slices.Equal(got, mode.want) {
						t.Errorf("part %d short=%v: ran %v, want %v", k, mode.short, got, mode.want)
					}
					if !mode.short {
						for _, key := range got {
							i, _ := strconv.Atoi(key)
							seen[i]++
						}
					}
				}
				// Part itself, with a run that records, in this test's
				// mode: the same cases as the model's.
				var got []string
				s.name = t.Name() + "/TestToy"
				s.run = func(_ *testing.T, cases iter.Seq[int]) {
					for i := range cases {
						got = append(got, strconv.Itoa(i))
					}
				}
				t.Run(fmt.Sprintf("TestToyPart%d", k), func(t *testing.T) { s.Part(t, k) })
				want := part
				if testing.Short() {
					want = short
				}
				if !slices.Equal(got, want) {
					t.Errorf("Part(t, %d) ran %v, want %v", k, got, want)
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

// coversDomain checks that the declared parts of s run its domain, each
// case once, and that a pinned domain is the pinned one.
func coversDomain(t *testing.T, s splitEntry, declared []int) {
	name := s.splitName()
	keys := s.keys()
	index := map[string]int{}
	for i, key := range keys {
		if j, ok := index[key]; ok {
			t.Fatalf("%s: cases %d and %d have one key; the coverage check needs distinct keys", name, j, i)
		}
		index[key] = i
	}
	ks := slices.Clone(declared)
	slices.Sort(ks)
	var idx []int
	for _, k := range ks {
		if k >= s.splitParts() {
			t.Errorf("%sPart%d: %s has %d parts", name, k, name, s.splitParts())
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
	for k := range s.splitParts() {
		if !slices.Contains(ks, k) {
			t.Errorf("%s: no test %sPart%d", name, name, k)
		}
	}
	if len(union) != len(keys) {
		t.Errorf("%s: the parts run %d of the domain's %d cases", name, len(union), len(keys))
	}
	if len(keys) == 0 {
		t.Errorf("%s: the domain is empty", name)
	}
}

// The leg 1 probe (request.md, "Leg 1"): with ERGO_SPLIT_PROBE set to a
// file, every lowerSource call, every program buildAll builds and every
// built program that runs appends "<kind> <sha256>" to it. The base carried
// the same functions as a scratch file, so the sorted lines of the unsplit
// test at the base and of its parts at the head compare.

var probe struct {
	sync.Mutex
	exes map[string]string
}

func probeSum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func probeLog(kind, sum string) {
	path := os.Getenv("ERGO_SPLIT_PROBE")
	if path == "" {
		return
	}
	probe.Lock()
	defer probe.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", kind, sum)
}

// probeBuilt records a built program and the executable that holds it.
func probeBuilt(exe string, mainGo string) {
	sum := probeSum([]byte(mainGo))
	probeLog("build", sum)
	if exe != "" {
		probe.Lock()
		if probe.exes == nil {
			probe.exes = map[string]string{}
		}
		probe.exes[exe] = sum
		probe.Unlock()
	}
}

// probeRan records a run of a built executable.
func probeRan(exe string) {
	probe.Lock()
	sum, ok := probe.exes[exe]
	probe.Unlock()
	if !ok {
		sum = "unknown"
	}
	probeLog("run", sum)
}
