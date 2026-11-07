package lower

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestMatchCostModule (M1, check C) writes one Go module that holds the
// emitted code of the breaker, full36 and mixed matches
// (match_tree_shape_test.go) and a hand-written Go control: the nested type
// switches a Go programmer writes for the same sealed interfaces, over the
// emitted types themselves. The module's contract test proves that the
// emitted code and the control give the same result for every input; the
// test also runs each benchmark once. With MATCH_COST_DIR set (an absolute
// path) the module stays there for the benchmark run:
//
//	go test -run '^$' -bench . -benchmem -count 10
//
// BenchmarkMatchEmitted and BenchmarkMatchControl each have the
// sub-benchmarks breaker (Breaker{}.Step over 1,024 seeded (state, signal)
// pairs, all 12 combinations present), full36 and mixed (pick over 1,024
// seeded pairs). The control of breaker builds the same Decision values,
// so both allocate alike.

func treeCostSource() string {
	return treeBreakerDecls + treeCommonTypes + treeFull36Func() + treeMixedFunc + "\nfunc main() {}\n"
}

// treeCostControl is the hand-written Go control.
func treeCostControl() string {
	var b strings.Builder
	b.WriteString(`package main

func ctlStep(s State, sig Signal) Decision[State, string] {
	switch s := s.(type) {
	case Closed:
		switch e := sig.(type) {
		case Failure:
			if s.Failures >= 2 {
				return Enter[State]{Next: Open{Endpoint: e.Endpoint}}
			}
			return Stay[State]{Next: Closed{Failures: s.Failures + 1}}
		case Retry:
			return Done[string]{Output: "arm2"}
		}
	case Open:
		switch sig.(type) {
		case Retry:
			return Stay[State]{Next: s}
		case Failure:
			return Done[string]{Output: "arm3"}
		}
	}
	panic("control: no case")
}

func ctlMixed(a A, b B) string {
	switch a := a.(type) {
	case A0:
		return "arm0"
	case A1:
		switch b := b.(type) {
		case B3:
			return "arm1"
		case B2:
			return "arm2" + b.S
		}
		return "arm8"
	case A2:
		if _, ok := b.(B3); ok {
			return "arm1"
		}
		switch a.N {
		case 1:
			return "arm3"
		case 2:
			if _, ok := b.(B4); ok {
				return "arm6"
			}
		}
		return "arm9"
	case A3:
		switch b.(type) {
		case B3:
			return "arm1"
		case B0:
			return "arm4"
		}
		return "arm9"
	case A4:
		switch b.(type) {
		case B3:
			return "arm1"
		case B0:
			return "arm4"
		case B5:
			return "arm7"
		}
		return "arm9"
	case A5:
		switch b := b.(type) {
		case B3:
			return "arm1"
		case B2:
			if b.S == "x" {
				return "arm5"
			}
		}
		return "arm9"
	}
	if _, ok := b.(B3); ok {
		return "arm1"
	}
	return "arm9"
}

func ctlPick36(a A, b B) string {
	switch a.(type) {
`)
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "\tcase A%d:\n\t\tswitch b.(type) {\n", i)
		for j := 0; j < 6; j++ {
			fmt.Fprintf(&b, "\t\tcase B%d:\n\t\t\treturn \"%d.%d\"\n", j, i, j)
		}
		b.WriteString("\t\t}\n")
	}
	b.WriteString("\t}\n\tpanic(\"control: no case\")\n}\n")
	return b.String()
}

// treeCostBench is the module's contract test and benchmarks.
const treeCostBench = `package main

import (
	"math/rand/v2"
	"testing"
)

type stepIn struct {
	s State
	e Signal
}

type pickIn struct {
	a A
	b B
}

var (
	states  = []State{Closed{Failures: 0}, Closed{Failures: 1}, Closed{Failures: 2}, Open{Endpoint: "p"}, Open{Endpoint: "q"}, Open{Endpoint: "r"}}
	signals = []Signal{Failure{Endpoint: "e"}, Retry{}}
	as      = []A{A0{}, A1{}, A2{N: 0}, A2{N: 1}, A2{N: 2}, A2{N: 3}, A3{}, A4{}, A5{}}
	bs      = []B{B0{}, B1{}, B2{S: "x"}, B2{S: "y"}, B3{}, B4{}, B5{}}
)

// stepInputs is 1,024 seeded pairs, every combination among them.
func stepInputs() []stepIn {
	r := rand.New(rand.NewPCG(1, 2))
	var in []stepIn
	for _, s := range states {
		for _, e := range signals {
			in = append(in, stepIn{s, e})
		}
	}
	for len(in) < 1024 {
		in = append(in, stepIn{states[r.IntN(len(states))], signals[r.IntN(len(signals))]})
	}
	r.Shuffle(len(in), func(i, j int) { in[i], in[j] = in[j], in[i] })
	return in
}

func pickInputs() []pickIn {
	r := rand.New(rand.NewPCG(3, 4))
	var in []pickIn
	for _, a := range as {
		for _, b := range bs {
			in = append(in, pickIn{a, b})
		}
	}
	for len(in) < 1024 {
		in = append(in, pickIn{as[r.IntN(len(as))], bs[r.IntN(len(bs))]})
	}
	r.Shuffle(len(in), func(i, j int) { in[i], in[j] = in[j], in[i] })
	return in
}

var (
	sinkD Decision[State, string]
	sinkS string
)

func TestMatchContract(t *testing.T) {
	for _, s := range states {
		for _, e := range signals {
			if got, want := (Breaker{}).Step(s, e), ctlStep(s, e); got != want {
				t.Errorf("Step(%v, %v) = %v, control %v", s, e, got, want)
			}
		}
	}
	for _, a := range as {
		for _, b := range bs {
			if got, want := pick36(a, b), ctlPick36(a, b); got != want {
				t.Errorf("pick36(%v, %v) = %q, control %q", a, b, got, want)
			}
			if got, want := pickMixed(a, b), ctlMixed(a, b); got != want {
				t.Errorf("pickMixed(%v, %v) = %q, control %q", a, b, got, want)
			}
		}
	}
}

func benchStep(b *testing.B, f func(State, Signal) Decision[State, string]) {
	in := stepInputs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := in[i&1023]
		sinkD = f(p.s, p.e)
	}
}

func benchPick(b *testing.B, f func(A, B) string) {
	in := pickInputs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := in[i&1023]
		sinkS = f(p.a, p.b)
	}
}

func BenchmarkMatchEmitted(b *testing.B) {
	b.Run("breaker", func(b *testing.B) { benchStep(b, Breaker{}.Step) })
	b.Run("full36", func(b *testing.B) { benchPick(b, pick36) })
	b.Run("mixed", func(b *testing.B) { benchPick(b, pickMixed) })
}

func BenchmarkMatchControl(b *testing.B) {
	b.Run("breaker", func(b *testing.B) { benchStep(b, ctlStep) })
	b.Run("full36", func(b *testing.B) { benchPick(b, ctlPick36) })
	b.Run("mixed", func(b *testing.B) { benchPick(b, ctlMixed) })
}
`

func TestMatchCostModule(t *testing.T) {
	dir := os.Getenv("MATCH_COST_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if !filepath.IsAbs(dir) {
		t.Fatalf("MATCH_COST_DIR %q must be absolute", dir)
	}
	emitted := mustLower(t, "matchcost", treeCostSource()).mainGo()
	files := map[string]string{
		"go.mod":              goMod,
		"main.go":             emitted,
		"control.go":          treeCostControl(),
		"match_bench_test.go": treeCostBench,
	}
	for _, f := range Runtime() {
		files[f.Path] = string(f.Data)
	}
	costWrite(t, dir, files)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout=3m", "-v",
		"-run", "^TestMatchContract$", "-bench", "^BenchmarkMatch(Emitted|Control)$", "-benchtime=1x", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOFLAGS=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("match cost module: %v\n%s\nGo:\n%s", err, output, stripDirectives(emitted))
	}
	if !strings.Contains(string(output), "--- PASS: TestMatchContract") {
		t.Errorf("the contract test did not pass:\n%s", output)
	}
	for _, side := range []string{"Emitted", "Control"} {
		for _, work := range []string{"breaker", "full36", "mixed"} {
			name := "BenchmarkMatch" + side + "/" + work
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `(?:-\d+)?\s+1\s+\S+ ns/op`).Match(output) {
				t.Errorf("benchmark %s did not run one op:\n%s", name, output)
			}
		}
	}
}
