package hostguard

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The determinism guard of the reviewed table v1.PureImports (LAWFFI
// LF2c, LF2d; plan-r1 §1, pure: "call twice under two ambient states
// (working directory, environment, time zone, GOMAXPROCS) and compare").
// members_gen.go, generated from the pinned toolchain's source
// (TestMembersCurrent), holds every exported function of the packages with
// no type parameter, minus v1.PureImportsExcept; the guard calls each
// whose parameters it can draw. It finds a member that reads host state
// the two states differ in (filepath.Abs reads the working directory); it
// cannot find every read of host state, so the fact stays review-first.

// pureCases is the number of drawn calls per member.
const pureCases = 8

// ambient is one host state of the guard.
type ambient struct {
	dir, home, tz string
	local         *time.Location
	procs         int
}

// enter makes s the process's host state and returns what restores the
// state before it.
func (s ambient) enter() (restore func(), err error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, hadHome := os.LookupEnv("HOME")
	tz, hadTZ := os.LookupEnv("TZ")
	local := time.Local
	if err := os.Chdir(s.dir); err != nil {
		return nil, err
	}
	os.Setenv("HOME", s.home)
	os.Setenv("TZ", s.tz)
	time.Local = s.local
	procs := runtime.GOMAXPROCS(s.procs)
	return func() {
		runtime.GOMAXPROCS(procs)
		time.Local = local
		reset := func(k, v string, had bool) {
			if had {
				os.Setenv(k, v)
			} else {
				os.Unsetenv(k)
			}
		}
		reset("HOME", home, hadHome)
		reset("TZ", tz, hadTZ)
		os.Chdir(wd)
	}, nil
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

// run calls every member pureCases times under the current host state;
// the arguments are drawn from a seed of the member's name and the case,
// so both states call with the same ones.
func run(members map[string]any) map[string][]outcome {
	out := map[string][]outcome{}
	for name, fn := range members {
		v := reflect.ValueOf(fn)
		for c := range pureCases {
			h := fnv.New64a()
			h.Write([]byte(name))
			r := rand.New(rand.NewPCG(h.Sum64()+Seed, uint64(c)))
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

// Diff is a member whose outcome differs between the two host states.
type Diff struct {
	Pkg, Member string
	Case        int
	A, B        string
}

func (d Diff) Error() string {
	return fmt.Sprintf("%s.%s case %d: %s under one host state, %s under the other", d.Pkg, d.Member, d.Case, d.A, d.B)
}

// pureStates are the guard's two host states, in two new directories.
func pureStates() (a, b ambient, done func(), err error) {
	da, err := os.MkdirTemp("", "ergo-hostguard-a-")
	if err != nil {
		return a, b, nil, err
	}
	db, err := os.MkdirTemp("", "ergo-hostguard-b-")
	if err != nil {
		os.Remove(da)
		return a, b, nil, err
	}
	a = ambient{dir: da, home: "/home/a", tz: "UTC", local: time.UTC, procs: 1}
	b = ambient{dir: db, home: "/home/b", tz: "Asia/Kolkata", local: time.FixedZone("IST", 19800), procs: 3}
	return a, b, func() { os.RemoveAll(da); os.RemoveAll(db) }, nil
}

// Pure is the members of the map (keyed "path.Name") whose outcome
// differs between the guard's two host states, each with its first
// differing case, sorted; a state the guard cannot enter is one Diff.
func Pure(members map[string]any) []Diff {
	a, b, done, err := pureStates()
	if err != nil {
		return []Diff{{Pkg: "?", Member: "?", A: err.Error()}}
	}
	defer done()
	outs := [2]map[string][]outcome{}
	for i, s := range []ambient{a, b} {
		restore, err := s.enter()
		if err != nil {
			return []Diff{{Pkg: "?", Member: "?", A: err.Error()}}
		}
		outs[i] = run(members)
		restore()
	}
	var diffs []Diff
	for name, got := range outs[0] {
		for c, o := range got {
			if p := outs[1][name][c]; !o.same(p) {
				dot := strings.LastIndex(name, ".")
				diffs = append(diffs, Diff{Pkg: name[:dot], Member: name[dot+1:], Case: c, A: fmt.Sprintf("%s%v", o.text, o.vals), B: fmt.Sprintf("%s%v", p.text, p.vals)})
				break
			}
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Error() < diffs[j].Error() })
	return diffs
}

// Callable is the number of members whose parameters the guard draws.
func Callable(members map[string]any) int {
	n := 0
	for _, fn := range members {
		if _, ok := drawArgs(rand.New(rand.NewPCG(1, 1)), reflect.TypeOf(fn)); ok {
			n++
		}
	}
	return n
}
