package gofacts

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"hash/fnv"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	v1 "ergo.local/prototype/runtime/v1"
)

// The determinism guard of the reviewed table v1.PureImports (LAWFFI
// LF2c; plan-r1 §1, pure: "call twice under two ambient states (working
// directory, environment, time zone, GOMAXPROCS) and compare"). The table
// stays in runtime/v1, where the checker and a marked body's import check
// read it. pure_members_test.go, generated from the pinned toolchain's
// source (-update-pure), holds every exported function of the packages
// with no type parameter, minus v1.PureImportsExcept; the guard calls each
// whose parameters it can draw. It finds a member that reads host state
// the two states differ in (filepath.Abs reads the working directory); it
// cannot find every read of host state, so the fact stays review-first.

var updatePure = flag.Bool("update-pure", false, "rewrite pure_members_test.go from the toolchain's source")

// pureCases is the number of drawn calls per member.
const pureCases = 8

// ambient is one host state of the guard.
type ambient struct {
	dir, home, tz string
	local         *time.Location
	procs         int
}

// enter makes s the test's host state.
func (s ambient) enter(t *testing.T) {
	t.Chdir(s.dir)
	t.Setenv("HOME", s.home)
	t.Setenv("TZ", s.tz)
	saved := time.Local
	time.Local = s.local
	t.Cleanup(func() { time.Local = saved })
	old := runtime.GOMAXPROCS(s.procs)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
}

// pureStates are the guard's two host states.
func pureStates(t *testing.T) (a, b ambient) {
	a = ambient{dir: t.TempDir(), home: "/home/a", tz: "UTC", local: time.UTC, procs: 1}
	b = ambient{dir: t.TempDir(), home: "/home/b", tz: "Asia/Kolkata", local: time.FixedZone("IST", 19800), procs: 3}
	return a, b
}

// outcome is one call's results and its arguments after the call, or its
// panic, printed.
type outcome struct {
	vals []reflect.Value
	text string
}

func (o outcome) same(p outcome) bool {
	if o.text != p.text {
		return false
	}
	for i := range o.vals {
		x, y := o.vals[i].Interface(), p.vals[i].Interface()
		if !reflect.DeepEqual(x, y) && fmt.Sprintf("%v", x) != fmt.Sprintf("%v", y) {
			return false
		}
	}
	return true
}

// pureRun calls every member pureCases times under the current host
// state; the arguments are drawn from a seed of the member's name and the
// case, so both states call with the same ones.
func pureRun(members map[string]any) map[string][]outcome {
	out := map[string][]outcome{}
	for name, fn := range members {
		v := reflect.ValueOf(fn)
		for c := range pureCases {
			h := fnv.New64a()
			h.Write([]byte(name))
			r := rand.New(rand.NewPCG(h.Sum64(), uint64(c)))
			args, ok := drawArgs(r, v.Type())
			if !ok {
				break
			}
			out[name] = append(out[name], call(v, args))
		}
	}
	return out
}

// call calls v with args: its results and its arguments after the call,
// or the panic.
func call(v reflect.Value, args []reflect.Value) (o outcome) {
	defer func() {
		if p := recover(); p != nil {
			o = outcome{text: fmt.Sprint("panic: ", p)}
		}
	}()
	var res []reflect.Value
	if v.Type().IsVariadic() {
		res = v.CallSlice(args)
	} else {
		res = v.Call(args)
	}
	return outcome{vals: append(res, args...)}
}

// drawArgs draws the arguments of a function of type ft, or reports a
// parameter it cannot draw.
func drawArgs(r *rand.Rand, ft reflect.Type) ([]reflect.Value, bool) {
	var args []reflect.Value
	for i := range ft.NumIn() {
		a, ok := drawValue(r, ft.In(i))
		if !ok {
			return nil, false
		}
		args = append(args, a)
	}
	return args, true
}

// drawValue is a drawn value of t: small, so no member allocates much or
// loops long (strings.Repeat's count is at most 8).
func drawValue(r *rand.Rand, t reflect.Type) (reflect.Value, bool) {
	v := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.String:
		var b strings.Builder
		for range r.IntN(9) {
			b.WriteString([]string{"a", "b", ",", "1", " ", ".", "/", "é", "\t", "~", "$"}[r.IntN(11)])
		}
		v.SetString(b.String())
	case reflect.Bool:
		v.SetBool(r.IntN(2) == 1)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int64:
		v.SetInt(int64(r.IntN(11) - 2))
	case reflect.Int32:
		v.SetInt(int64([]rune{'a', 'é', ' ', '9', -1, 0x10ffff}[r.IntN(6)]))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v.SetUint(uint64(r.IntN(9)))
	case reflect.Float32, reflect.Float64:
		v.SetFloat([]float64{0, 1.5, -2, 1e300, math.NaN(), math.Inf(1)}[r.IntN(6)])
	case reflect.Slice:
		n := r.IntN(4)
		s := reflect.MakeSlice(t, n, n)
		for i := range n {
			e, ok := drawValue(r, t.Elem())
			if !ok {
				return v, false
			}
			s.Index(i).Set(e)
		}
		v.Set(s)
	default:
		return v, false
	}
	return v, true
}

// pureDiffs is the members whose outcome differs between the states a and
// b, each with its first differing case.
func pureDiffs(t *testing.T, members map[string]any) []string {
	a, b := pureStates(t)
	a.enter(t)
	inA := pureRun(members)
	b.enter(t)
	inB := pureRun(members)
	var diffs []string
	for name, outs := range inA {
		for c, o := range outs {
			if !o.same(inB[name][c]) {
				diffs = append(diffs, fmt.Sprintf("%s case %d: %s%v under one host state, %s%v under the other", name, c, o.text, o.vals, inB[name][c].text, inB[name][c].vals))
				break
			}
		}
	}
	sort.Strings(diffs)
	return diffs
}

// TestPureGoFactsHold is the determinism guard of v1.PureImports: every
// member it can call gives the same results under two host states; a
// planted member that reads the working directory (filepath.Abs, which
// v1.PureImportsExcept names) is refuted.
//
// Mutants it alone catches: ffi-pure-guard-one-state (the two states are
// one), ffi-pure-except-dropped (filepath.Abs joins the members).
func TestPureGoFactsHold(t *testing.T) {
	callable := 0
	for _, fn := range pureMembers {
		if _, ok := drawArgs(rand.New(rand.NewPCG(1, 1)), reflect.TypeOf(fn)); ok {
			callable++
		}
	}
	t.Logf("the guard calls %d of %d members", callable, len(pureMembers))
	if callable*2 < len(pureMembers) {
		t.Errorf("the guard draws the arguments of %d of %d members: most go unchecked", callable, len(pureMembers))
	}
	for _, d := range pureDiffs(t, pureMembers) {
		t.Errorf("a member of v1.PureImports reads host state: %s", d)
	}
	if d := pureDiffs(t, map[string]any{"path/filepath.Abs": filepath.Abs}); len(d) == 0 {
		t.Errorf("filepath.Abs, which reads the working directory, passes the determinism guard")
	}
}

// TestPureMembersCurrent: pure_members_test.go is the generator's output
// for the pinned toolchain and the current v1.PureImports and
// v1.PureImportsExcept (go test ./internal/gofacts -run TestPureMembersCurrent -args -update-pure rewrites
// it).
func TestPureMembersCurrent(t *testing.T) {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	src, err := pureMembersSource(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	if *updatePure {
		if err := os.WriteFile("pure_members_test.go", src, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	have, err := os.ReadFile("pure_members_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(have, src) {
		t.Errorf("pure_members_test.go is stale: run go test ./internal/gofacts -run TestPureMembersCurrent -args -update-pure")
	}
}

// pureMembersSource is the text of pure_members_test.go: every exported
// function with no type parameter of each package of v1.PureImports, in
// the files the build of this platform reads, minus v1.PureImportsExcept.
func pureMembersSource(goroot string) ([]byte, error) {
	var b, imports bytes.Buffer
	pkgs := slices.Sorted(slices.Values(v1.PureImports))
	ctx := build.Default
	for _, p := range pkgs {
		dir := filepath.Join(goroot, "src", filepath.FromSlash(p))
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		names := map[string]bool{}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
				continue
			}
			if ok, err := ctx.MatchFile(dir, n); err != nil || !ok {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, n), nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			if f.Name.Name != path.Base(p) {
				continue
			}
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if ok && fd.Recv == nil && fd.Type.TypeParams == nil && ast.IsExported(fd.Name.Name) && !slices.Contains(v1.PureImportsExcept[p], fd.Name.Name) {
					names[fd.Name.Name] = true
				}
			}
		}
		if len(names) > 0 {
			// A package of generic functions only (slices) has none.
			fmt.Fprintf(&imports, "\t%q\n", p)
		}
		for _, n := range slices.Sorted(maps.Keys(names)) {
			fmt.Fprintf(&b, "\t%q: %s.%s,\n", p+"."+n, path.Base(p), n)
		}
	}
	src := "// Code generated by TestPureMembersCurrent -update-pure. DO NOT EDIT.\n\npackage gofacts\n\nimport (\n" + imports.String() +
		")\n\n// pureMembers are the guarded members of v1.PureImports (pure_test.go).\nvar pureMembers = map[string]any{\n" + b.String() + "}\n"
	return format.Source([]byte(src))
}
