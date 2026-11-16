package gofacts

import (
	"bytes"
	"cmp"
	"expvar"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"maps"
	mrand "math/rand"
	rand2 "math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "ergo.local/prototype/runtime/v1"
)

// The guard of the reviewed table v1.GoSyncCallbacks (LAWFFI LF2c;
// plan-r1 §1, sync f: "a run that records the goroutine and the call's
// end at each callback"). The table stays in runtime/v1, where the checker
// and ergo bind go read it; this file holds one call per row, whose
// callbacks report to a probe.

// probe records a guarded call's callbacks: each must run on the caller's
// goroutine before the call returns.
type probe struct {
	mu       sync.Mutex
	gid      uint64
	calls    int
	returned bool
	bad      string
}

func newProbe() *probe { return &probe{gid: goid()} }

// hit is a callback's report.
func (p *probe) hit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	switch {
	case p.returned:
		p.bad = "a callback ran after the call returned"
	case goid() != p.gid:
		p.bad = "a callback ran on another goroutine"
	}
}

// goid is the running goroutine's id, from its stack header.
func goid() uint64 {
	var buf [64]byte
	s := strings.TrimPrefix(string(buf[:runtime.Stack(buf[:], false)]), "goroutine ")
	id, _ := strconv.ParseUint(s[:strings.IndexByte(s, ' ')], 10, 64)
	return id
}

// syncGuard runs call twice, each with a new probe: every callback of
// each call ran, on the caller's goroutine, before that call returned. A
// callback the function keeps and a later call runs is seen at the second
// call.
func syncGuard(target string, call func(p *probe)) error {
	var probes []*probe
	for range 2 {
		p := newProbe()
		call(p)
		p.mu.Lock()
		p.returned = true
		p.mu.Unlock()
		probes = append(probes, p)
	}
	for i, p := range probes {
		switch {
		case p.bad != "":
			return fmt.Errorf("%s: %s (call %d)", target, p.bad, i+1)
		case p.calls == 0:
			return fmt.Errorf("%s: the guard's call ran no callback (call %d): the guard checks nothing", target, i+1)
		}
	}
	return nil
}

var probeFlag sync.Once

func syncCalls() map[string]func(p *probe) {
	rf := func(p *probe) func(rune) bool { return func(rune) bool { p.hit(); return false } }
	cmpf := func(p *probe) func(a, b int) int { return func(a, b int) int { p.hit(); return cmp.Compare(a, b) } }
	eqf := func(p *probe) func(a, b int) bool { return func(a, b int) bool { p.hit(); return a == b } }
	pred := func(p *probe) func(int) bool { return func(int) bool { p.hit(); return false } }
	swap := func(p *probe) func(i, j int) { return func(i, j int) { p.hit() } }
	visit := func(p *probe) func(*flag.Flag) { return func(*flag.Flag) { p.hit() } }
	flagSet := func() *flag.FlagSet {
		fs := flag.NewFlagSet("probe", flag.ContinueOnError)
		fs.Bool("b", false, "")
		_ = fs.Set("b", "true")
		return fs
	}
	return map[string]func(p *probe){
		"bytes.ContainsFunc":      func(p *probe) { bytes.ContainsFunc([]byte("ab"), rf(p)) },
		"bytes.FieldsFunc":        func(p *probe) { bytes.FieldsFunc([]byte("a b"), rf(p)) },
		"bytes.IndexFunc":         func(p *probe) { bytes.IndexFunc([]byte("ab"), rf(p)) },
		"bytes.LastIndexFunc":     func(p *probe) { bytes.LastIndexFunc([]byte("ab"), rf(p)) },
		"bytes.Map":               func(p *probe) { bytes.Map(func(r rune) rune { p.hit(); return r }, []byte("ab")) },
		"bytes.TrimFunc":          func(p *probe) { bytes.TrimFunc([]byte("ab"), rf(p)) },
		"bytes.TrimLeftFunc":      func(p *probe) { bytes.TrimLeftFunc([]byte("ab"), rf(p)) },
		"bytes.TrimRightFunc":     func(p *probe) { bytes.TrimRightFunc([]byte("ab"), rf(p)) },
		"strings.ContainsFunc":    func(p *probe) { strings.ContainsFunc("ab", rf(p)) },
		"strings.FieldsFunc":      func(p *probe) { strings.FieldsFunc("a b", rf(p)) },
		"strings.IndexFunc":       func(p *probe) { strings.IndexFunc("ab", rf(p)) },
		"strings.LastIndexFunc":   func(p *probe) { strings.LastIndexFunc("ab", rf(p)) },
		"strings.Map":             func(p *probe) { strings.Map(func(r rune) rune { p.hit(); return r }, "ab") },
		"strings.TrimFunc":        func(p *probe) { strings.TrimFunc("ab", rf(p)) },
		"strings.TrimLeftFunc":    func(p *probe) { strings.TrimLeftFunc("ab", rf(p)) },
		"strings.TrimRightFunc":   func(p *probe) { strings.TrimRightFunc("ab", rf(p)) },
		"slices.BinarySearchFunc": func(p *probe) { _, _ = slices.BinarySearchFunc([]int{1, 2, 3}, 2, cmpf(p)) },
		"slices.CompactFunc":      func(p *probe) { _ = slices.CompactFunc([]int{1, 1, 2}, eqf(p)) },
		"slices.CompareFunc":      func(p *probe) { _ = slices.CompareFunc([]int{1}, []int{1}, cmpf(p)) },
		"slices.ContainsFunc":     func(p *probe) { _ = slices.ContainsFunc([]int{1}, pred(p)) },
		"slices.DeleteFunc":       func(p *probe) { _ = slices.DeleteFunc([]int{1, 2}, pred(p)) },
		"slices.EqualFunc":        func(p *probe) { _ = slices.EqualFunc([]int{1}, []int{1}, eqf(p)) },
		"slices.IndexFunc":        func(p *probe) { _ = slices.IndexFunc([]int{1}, pred(p)) },
		"slices.IsSortedFunc":     func(p *probe) { _ = slices.IsSortedFunc([]int{1, 2}, cmpf(p)) },
		"slices.MaxFunc":          func(p *probe) { _ = slices.MaxFunc([]int{1, 2}, cmpf(p)) },
		"slices.MinFunc":          func(p *probe) { _ = slices.MinFunc([]int{1, 2}, cmpf(p)) },
		"slices.SortFunc":         func(p *probe) { slices.SortFunc([]int{2, 1}, cmpf(p)) },
		"slices.SortStableFunc":   func(p *probe) { slices.SortStableFunc([]int{2, 1}, cmpf(p)) },
		"slices.SortedFunc":       func(p *probe) { _ = slices.SortedFunc(slices.Values([]int{2, 1}), cmpf(p)) },
		"slices.SortedStableFunc": func(p *probe) { _ = slices.SortedStableFunc(slices.Values([]int{2, 1}), cmpf(p)) },
		"maps.DeleteFunc":         func(p *probe) { maps.DeleteFunc(map[int]int{1: 1}, eqf(p)) },
		"maps.EqualFunc":          func(p *probe) { _ = maps.EqualFunc(map[int]int{1: 1}, map[int]int{1: 1}, eqf(p)) },
		"sort.Find":               func(p *probe) { sort.Find(3, func(int) int { p.hit(); return 0 }) },
		"sort.Search":             func(p *probe) { sort.Search(3, func(int) bool { p.hit(); return true }) },
		"math/rand.Shuffle":       func(p *probe) { mrand.Shuffle(3, swap(p)) },
		"math/rand.Rand.Shuffle":  func(p *probe) { mrand.New(mrand.NewSource(1)).Shuffle(3, swap(p)) },
		"math/rand/v2.Shuffle":    func(p *probe) { rand2.Shuffle(3, swap(p)) },
		"math/rand/v2.Rand.Shuffle": func(p *probe) {
			rand2.New(rand2.NewPCG(1, 2)).Shuffle(3, swap(p))
		},
		"os.Expand": func(p *probe) { os.Expand("$a", func(string) string { p.hit(); return "" }) },
		"path/filepath.Walk": func(p *probe) {
			_ = filepath.Walk("testdata", func(string, fs.FileInfo, error) error { p.hit(); return nil })
		},
		"path/filepath.WalkDir": func(p *probe) {
			_ = filepath.WalkDir("testdata", func(string, fs.DirEntry, error) error { p.hit(); return nil })
		},
		"io/fs.WalkDir": func(p *probe) {
			_ = fs.WalkDir(os.DirFS("testdata"), ".", func(string, fs.DirEntry, error) error { p.hit(); return nil })
		},
		"regexp.Regexp.ReplaceAllFunc": func(p *probe) {
			regexp.MustCompile("a").ReplaceAllFunc([]byte("aa"), func(b []byte) []byte { p.hit(); return b })
		},
		"regexp.Regexp.ReplaceAllStringFunc": func(p *probe) {
			regexp.MustCompile("a").ReplaceAllStringFunc("aa", func(s string) string { p.hit(); return s })
		},
		"flag.Visit": func(p *probe) {
			probeFlag.Do(func() {
				flag.Bool("gofacts.probe", false, "set by the sync guard of flag.Visit")
				_ = flag.Set("gofacts.probe", "true")
			})
			flag.Visit(visit(p))
		},
		"flag.VisitAll":         func(p *probe) { flag.VisitAll(visit(p)) },
		"flag.FlagSet.Visit":    func(p *probe) { flagSet().Visit(visit(p)) },
		"flag.FlagSet.VisitAll": func(p *probe) { flagSet().VisitAll(visit(p)) },
		"go/ast.Inspect": func(p *probe) {
			f, _ := parser.ParseFile(token.NewFileSet(), "p.go", "package p", 0)
			ast.Inspect(f, func(ast.Node) bool { p.hit(); return true })
		},
		"expvar.Do": func(p *probe) { expvar.Do(func(expvar.KeyValue) { p.hit() }) },
		"log/slog.Record.Attrs": func(p *probe) {
			r := slog.NewRecord(time.Time{}, slog.LevelInfo, "m", 0)
			r.AddAttrs(slog.Int("a", 1))
			r.Attrs(func(slog.Attr) bool { p.hit(); return true })
		},
		"sync.Once.Do": func(p *probe) {
			var o sync.Once
			o.Do(p.hit)
		},
	}
}

// TestSyncCallbacksHold is the guard of v1.GoSyncCallbacks: every row has
// a guarded call and passes it, and every guarded call is a row; two
// planted false rows, a function that runs its callback on another
// goroutine and one that keeps it for its next call, are refuted.
//
// Mutants it alone catches: ffi-sync-row-unguarded (a row with no guarded
// call), ffi-sync-guard-goroutine-blind, ffi-sync-guard-one-call.
func TestSyncCallbacksHold(t *testing.T) {
	calls := syncCalls()
	for target, reason := range v1.GoSyncCallbacks {
		call, ok := calls[target]
		switch {
		case !ok:
			t.Errorf("%s: a row of v1.GoSyncCallbacks (%q) with no guarded call in syncCalls", target, reason)
		case reason == "":
			t.Errorf("%s: a row with no reason", target)
		default:
			if err := syncGuard(target, call); err != nil {
				t.Error(err)
			}
		}
	}
	for target := range calls {
		if v1.GoSyncCallbacks[target] == "" {
			t.Errorf("%s: a guarded call with no row in v1.GoSyncCallbacks", target)
		}
	}
	var kept func()
	for name, c := range map[string]struct {
		call func(p *probe)
		want string
	}{
		"fixture.Async": {func(p *probe) {
			done := make(chan struct{})
			go func() { p.hit(); close(done) }()
			<-done
		}, "a callback ran on another goroutine"},
		"fixture.Keep": {func(p *probe) {
			p.hit()
			if kept != nil {
				kept()
			}
			kept = p.hit
		}, "a callback ran after the call returned"},
	} {
		if err := syncGuard(name, c.call); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("a planted false row %s passes the sync guard: %v, want %q", name, err, c.want)
		}
	}
}
