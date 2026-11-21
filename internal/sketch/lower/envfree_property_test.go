package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The seeded property of env-free functions (pure.go, isEnvFree;
// ENV-FREE-FALLIBLE). Each seed writes a program of functions f0..fn-1
// over int, each of which may fail (fails Bad), may read the environment
// itself (it calls apply, which calls a function value: an opaque call),
// and calls up to three others, itself included, at x-1 under a base
// case, so the call graph has cycles. A failing callee is called with try
// from a failing caller and caught from the others.
//
// The model reads the call graph: a function reads the environment when
// it does or a callee does (the least fixed point). Under the rule, a
// function takes the environment parameter if and only if it reads it,
// whether or not it fails; under the reference (envParamLowering "pure"),
// it takes none only when it is pure: it does not fail, does not read,
// and calls only pure functions. TestEnvParamFollowsReads checks both on
// the Go text lowering, type-checked in process, and on the IR lowering
// (LowerIR), and TestEnvLoweringsAgree runs both builds against the
// model's stdout. A failing seed prints the Ergo source and its replay
// command. The fixed seeds reach every path in envFreePaths.
//
// Mutants it kills (testdata/mutants.json): env-free-callee-read-dropped
// (a caller of a reader loses its environment), env-free-fallible-keeps-env
// (a failing body takes the environment again), env-free-machine-step-env
// (a machine plan's Step passes the environment to a failing step that
// takes none: each seed's Counter, whose step advance calls one fi).

var envFreeSeed = flag.Uint64("envfree.seed", 0, "run the env-free properties on this seed alone")

// envFreePaths are the generator's branches; the fixed seeds reach each.
var envFreePaths = []string{"fails-free", "fails-reads", "pure", "reads", "through-callee", "cycle-free", "try", "catch", "machine-step-free", "machine-step-reads"}

// envFreeFn is one generated function.
type envFreeFn struct {
	fails, direct bool
	failAt        int   // the x it fails at, when fails
	calls         []int // its callees, in call order
}

// envFreeProgram is one seed's program and its model.
type envFreeProgram struct {
	seed  uint64
	fns   []envFreeFn
	step  int // the function the machine's step calls
	src   string
	reads []bool // the model: fi reads the environment
	pure  []bool // the reference's model: fi is pure
	want  string // stdout
}

func envFreeGen(seed uint64) envFreeProgram {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	n := 3 + r.IntN(4)
	p := envFreeProgram{seed: seed, fns: make([]envFreeFn, n)}
	for i := range p.fns {
		f := &p.fns[i]
		f.fails = r.IntN(2) == 0
		f.direct = r.IntN(4) == 0
		f.failAt = 1 + r.IntN(3)
		for range r.IntN(4) {
			f.calls = append(f.calls, r.IntN(n))
		}
	}
	p.step = int(seed) % n // not r: each seed's functions stay what they were
	p.model()
	p.write()
	return p
}

// model computes reads (least fixed point) and pure (greatest fixed point).
func (p *envFreeProgram) model() {
	n := len(p.fns)
	p.reads, p.pure = make([]bool, n), make([]bool, n)
	for i, f := range p.fns {
		p.reads[i] = f.direct
		p.pure[i] = !f.fails && !f.direct
	}
	for changed := true; changed; {
		changed = false
		for i, f := range p.fns {
			for _, j := range f.calls {
				if p.reads[j] && !p.reads[i] {
					p.reads[i], changed = true, true
				}
				if !p.pure[j] && p.pure[i] {
					p.pure[i], changed = false, true
				}
			}
		}
	}
	var out strings.Builder
	for i := range p.fns {
		v, failed := p.eval(i, 4)
		if failed {
			v = -1
		}
		fmt.Fprintf(&out, "%d\n", v)
	}
	p.want = out.String()
}

// eval is fi(x) in Go: its value, or whether it failed.
func (p *envFreeProgram) eval(i, x int) (int, bool) {
	f := p.fns[i]
	if x <= 0 {
		return 1, false
	}
	if f.fails && x == f.failAt {
		return 0, true
	}
	s := x
	if f.direct {
		s += x + 1 // apply(step, x)
	}
	for _, j := range f.calls {
		v, failed := p.eval(j, x-1)
		switch {
		case failed && f.fails:
			return 0, true // try passes it on
		case failed:
			v = 7 // catch
		}
		s += v
	}
	return s, false
}

func (p *envFreeProgram) write() {
	var b strings.Builder
	b.WriteString("type Bad { N int }\n\nfunc step(x int) int { x + 1 }\n\nfunc apply(h func(int) int, x int) int { h(x) }\n")
	for i, f := range p.fns {
		fails := ""
		if f.fails {
			fails = " fails Bad"
		}
		fmt.Fprintf(&b, "\nfunc f%d(x int) int%s {\n\tif x <= 0 {\n\t\treturn 1\n\t}\n", i, fails)
		if f.fails {
			fmt.Fprintf(&b, "\tif x == %d {\n\t\treturn Bad{N: %d}\n\t}\n", f.failAt, i)
		}
		if f.direct || len(f.calls) > 0 {
			b.WriteString("\tlet mut s = x\n")
		} else {
			b.WriteString("\tlet s = x\n")
		}
		if f.direct {
			b.WriteString("\ts = s + apply(step, x)\n")
		}
		for k, j := range f.calls {
			switch {
			case p.fns[j].fails && f.fails:
				fmt.Fprintf(&b, "\ts = s + try f%d(x - 1)\n", j)
			case p.fns[j].fails:
				fmt.Fprintf(&b, "\tlet c%d = f%d(x - 1) catch {\n\tcase Bad:\n\t\t7\n\t}\n\ts = s + c%d\n", k, j, k)
			default:
				fmt.Fprintf(&b, "\ts = s + f%d(x - 1)\n", j)
			}
		}
		b.WriteString("\ts\n}\n")
	}
	// A machine whose step fails and is a method of its state record,
	// calling f(step): it takes the environment iff f(step) reads it, and
	// the plan's Step must pass it iff it does (std actor's exitStep;
	// mutant env-free-machine-step-env).
	j := p.step
	try := ""
	if p.fns[j].fails {
		try = "try "
	}
	fmt.Fprintf(&b, "\ntype St { N int }\n\ntype Tick { K int }\n\nfunc first(i int) St { St{N: i} }\n\nfunc advance(c St, t Tick) Decision[St, int] fails Bad {\n\tif t.K < 0 {\n\t\treturn Bad{N: t.K}\n\t}\n\tlet v = %sf%d(t.K)\n\tStay[St]{Next: St{N: c.N + v}}\n}\n\ntype Counter machine[St, Tick] { initial: first, step: advance }\n", try, j)
	b.WriteString("\nfunc main() needs Console {\n")
	for i, f := range p.fns {
		if f.fails {
			fmt.Fprintf(&b, "\tlet r%d = f%d(4) catch {\n\tcase Bad:\n\t\t-1\n\t}\n", i, i)
		} else {
			fmt.Fprintf(&b, "\tlet r%d = f%d(4)\n", i, i)
		}
		fmt.Fprintf(&b, "\tConsole.Println(\"\\(r%d)\")\n", i)
	}
	b.WriteString("}\n")
	p.src = b.String()
}

// reach records the paths p reaches.
func (p *envFreeProgram) reach(reached map[string]bool) {
	onCycle := func(i int) bool {
		seen := map[int]bool{}
		var walk func(j int) bool
		walk = func(j int) bool {
			for _, k := range p.fns[j].calls {
				if k == i {
					return true
				}
				if !seen[k] {
					seen[k] = true
					if walk(k) {
						return true
					}
				}
			}
			return false
		}
		return walk(i)
	}
	if p.reads[p.step] {
		reached["machine-step-reads"] = true
	} else {
		reached["machine-step-free"] = true
	}
	for i, f := range p.fns {
		switch {
		case f.fails && !p.reads[i]:
			reached["fails-free"] = true
		case f.fails:
			reached["fails-reads"] = true
		case p.pure[i]:
			reached["pure"] = true
		case p.reads[i]:
			reached["reads"] = true
		}
		if p.reads[i] && !f.direct {
			reached["through-callee"] = true
		}
		if !p.reads[i] && onCycle(i) {
			reached["cycle-free"] = true
		}
		for _, j := range f.calls {
			if p.fns[j].fails {
				if f.fails {
					reached["try"] = true
				} else {
					reached["catch"] = true
				}
			}
		}
	}
}

// wantEnv is the model's environment parameter of each function under the
// rule (ref false) or the reference.
func (p *envFreeProgram) wantEnv(ref bool) (free, env []string) {
	for i := range p.fns {
		takes := p.reads[i]
		if ref {
			takes = !p.pure[i]
		}
		if takes {
			env = append(env, fmt.Sprintf("f%d", i))
		} else {
			free = append(free, fmt.Sprintf("f%d", i))
		}
	}
	return free, env
}

func envFreeSeeds() []uint64 {
	if *envFreeSeed != 0 {
		return []uint64{*envFreeSeed}
	}
	var seeds []uint64
	for s := uint64(1); s <= 24; s++ {
		seeds = append(seeds, s)
	}
	return seeds
}

// lowerEnvMode lowers src as text under the rule or the reference.
func lowerEnvMode(t *testing.T, name, src string, ref bool) *lowered {
	t.Helper()
	saved := envParamLowering
	defer func() { envParamLowering = saved }()
	envParamLowering = ""
	if ref {
		envParamLowering = "pure"
	}
	return mustLower(t, name, src)
}

// lowerEnvIR is the main.go of src's IR lowering under the rule or the
// reference.
func lowerEnvIR(t *testing.T, src string, ref bool) string {
	t.Helper()
	saved := envParamLowering
	defer func() { envParamLowering = saved }()
	envParamLowering = ""
	if ref {
		envParamLowering = "pure"
	}
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v", diags)
	}
	files, _, err := LowerIR(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
	if err != nil {
		t.Fatalf("LowerIR: %v\n%s", err, src)
	}
	for _, f := range files {
		if f.Path == "main.go" {
			return string(f.Data)
		}
	}
	t.Fatalf("LowerIR wrote no main.go")
	return ""
}

// TestEnvParamFollowsReads is the in-process half of the property (see
// above): the environment parameter of every generated function, on the
// text and the IR lowering, under the rule and the reference, and the
// text lowering type-checked.
func TestEnvParamFollowsReads(t *testing.T) {
	reached := map[string]bool{}
	var programs []*lowered
	for _, seed := range envFreeSeeds() {
		p := envFreeGen(seed)
		p.reach(reached)
		replay := fmt.Sprintf("Replay: go test -run '^TestEnvParamFollowsReads$' ./internal/sketch/lower -args -envfree.seed=%d", seed)
		for _, ref := range []bool{false, true} {
			name := fmt.Sprintf("envfree%d", seed)
			if ref {
				name += "ref"
			}
			free, env := p.wantEnv(ref)
			o := lowerEnvMode(t, name, p.src, ref)
			programs = append(programs, o)
			before := t.Failed()
			m := pureModel{name: name + " (text)", pure: append(free, "step"), impure: append(env, "apply")}
			checkEnvParams(t, m, o.mainGo())
			m.name = name + " (IR)"
			checkEnvParams(t, m, lowerEnvIR(t, p.src, ref))
			if !before && t.Failed() {
				t.Logf("%s\n%s", replay, p.src)
			}
		}
	}
	typecheckGo(t, programs)
	if *envFreeSeed != 0 {
		return
	}
	for _, path := range envFreePaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q: %v", path, reached)
		}
	}
}

// TestEnvLoweringsAgree is the run half of the property: each seed's
// program, built under the rule and under the reference, prints the
// model's stdout.
//
// It builds Go because the question is what the env-free functions
// compute when they run: go/types says only that the calls that pass no
// environment match the signatures that take none, not that a failure
// still reaches its catch or a try passes it on.
func TestEnvLoweringsAgree(t *testing.T) {
	needsBuild(t)
	var runs []envFreeProgram
	var programs []*lowered
	for _, seed := range envFreeSeeds() {
		p := envFreeGen(seed)
		runs = append(runs, p)
		programs = append(programs,
			lowerEnvMode(t, fmt.Sprintf("envfree%d", seed), p.src, false),
			lowerEnvMode(t, fmt.Sprintf("envfree%dref", seed), p.src, true))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the env-free programs do not build")
	}
	for _, p := range runs {
		name := fmt.Sprintf("envfree%d", p.seed)
		out, stderr, code := runExe(t, exes[name])
		refOut, refErr, refCode := runExe(t, exes[name+"ref"])
		if out != p.want || refOut != p.want || code != 0 || refCode != 0 {
			t.Errorf("seed %d: the lowerings and the model differ\nReplay: go test -run '^TestEnvLoweringsAgree$' ./internal/sketch/lower -args -envfree.seed=%d\n%s\n--- env-free (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s\n--- model\n%s", p.seed, p.seed, p.src, code, out, stderr, refCode, refOut, refErr, p.want)
		}
	}
}
