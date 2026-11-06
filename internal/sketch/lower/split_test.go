package lower

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"iter"
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
// A generated domain is pinned: Count is its number of cases, and Digest
// is the SHA-256 of the case keys, each written as "<len>:<key>\n", in
// index order, measured from the unsplit test at f79be34b. A domain read
// from files that other units edit (the check package's tests, the
// fixtures and the examples) is not pinned, since a new fixture must join
// it with no pin to keep; its parts must still cover it, each case once.
// TestSplitsCoverTheirDomain checks both. So a dropped part, a part that
// runs another part's cases, or a generator that draws other cases fails
// that test.
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
	// count and digest pin the full domain; unpinned leaves them unchecked
	// (a domain read from files).
	count    int
	digest   string
	unpinned bool
	// cases yields the full domain, in index order.
	cases iter.Seq[C]
	// key is a stable text of one case: what it checks, with its inputs.
	key func(C) string
	// run checks the cases of a part.
	run func(t *testing.T, cases []C)
}

// splits are the package's splits, by newSplit.
var splits []splitEntry

type splitEntry interface {
	splitName() string
	splitParts() int
	pin() (count int, digest string, pinned bool)
	// domain is the key of each case of the full domain, and the domain
	// indexes each part runs in full mode.
	domain() (keys []string, parts [][]int)
}

// newSplit registers s for TestSplitsCoverTheirDomain.
func newSplit[C any](s split[C]) *split[C] {
	p := &s
	splits = append(splits, p)
	return p
}

func (s *split[C]) splitName() string { return s.name }
func (s *split[C]) splitParts() int   { return s.parts }

func (s *split[C]) pin() (int, string, bool) { return s.count, s.digest, !s.unpinned }

func (s *split[C]) domain() ([]string, [][]int) {
	cases := slices.Collect(s.cases)
	keys := make([]string, len(cases))
	for i, c := range cases {
		keys[i] = s.key(c)
	}
	parts := make([][]int, s.parts)
	for k := range s.parts {
		for i := range s.share(slices.Values(cases), k, false) {
			parts[k] = append(parts[k], i)
		}
	}
	return keys, parts
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
func (s *split[C]) Part(t *testing.T, k int) {
	t.Helper()
	if want := s.name + "Part" + strconv.Itoa(k); t.Name() != want {
		t.Fatalf("part %d of %s runs in %s, want %s", k, s.name, t.Name(), want)
	}
	if k < 0 || k >= s.parts {
		t.Fatalf("%s has parts 0 to %d, not %d", s.name, s.parts-1, k)
	}
	if testing.Short() && s.short == 0 {
		t.Skipf("%s runs in full mode only (the gate's full tier)", s.name)
	}
	var cases []C
	for _, c := range s.share(s.cases, k, testing.Short()) {
		cases = append(cases, c)
	}
	s.run(t, cases)
}

// share yields the cases of part k of the domain (under short its fixed
// subset), with their domain indexes.
func (s *split[C]) share(domain iter.Seq[C], k int, short bool) iter.Seq2[int, C] {
	return func(yield func(int, C) bool) {
		// i counts the domain, j the part.
		i, j := 0, 0
		for c := range domain {
			index, mine := i, i%s.parts == k
			i++
			if !mine {
				continue
			}
			kept := !short || j%s.short == 0
			j++
			if kept && !yield(index, c) {
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
// split, the parts that exist run every case of the full domain once; a
// pinned domain is the one the unsplit test ran (count and digest measured
// at f79be34b, before the split).
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

// coversDomain checks that the declared parts of s run its domain, each
// case once, and that a pinned domain is the pinned one.
func coversDomain(t *testing.T, s splitEntry, declared []int) {
	name := s.splitName()
	keys, parts := s.domain()
	ks := slices.Clone(declared)
	slices.Sort(ks)
	var idx []int
	for _, k := range ks {
		if k >= s.splitParts() {
			t.Errorf("%sPart%d: %s has %d parts", name, k, name, s.splitParts())
			continue
		}
		idx = append(idx, parts[k]...)
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
	count, digest, pinned := s.pin()
	if !pinned {
		t.Logf("%s: %d cases, read from files; not pinned", name, len(keys))
		return
	}
	if len(keys) != count {
		t.Errorf("%s: the domain has %d cases, pinned %d", name, len(keys), count)
	}
	if got := splitDigest(union); len(union) != count || got != digest {
		t.Errorf("%s: the parts run %d cases with digest %s; the pinned domain is %d cases with digest %s", name, len(union), got, count, digest)
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
