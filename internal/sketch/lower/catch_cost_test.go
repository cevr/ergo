package lower

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestCatchCostModule (M1b) writes one Go module that holds the emitted
// code of the shape cases total and partial (catch_tree_test.go) and a
// hand-written Go control: the type switch on the error, then the field
// tests, that a Go programmer writes after the same raise call. The
// module's contract test proves that the emitted code and the control give
// the same result and the same error for every input; the test also runs
// each benchmark once. With CATCH_COST_DIR set (an absolute path) the
// module stays there for the benchmark run:
//
//	go test -run '^$' -bench . -benchmem -count 10
//
// BenchmarkCatchEmitted and BenchmarkCatchControl each have the
// sub-benchmarks total and partial: the catch over 1,024 seeded inputs of
// raise (each of its six errors and its value), so a branch predictor
// cannot learn the order.

func catchCostSource() string {
	var b strings.Builder
	b.WriteString(catchShapeDecls)
	for _, c := range catchShapeCases() {
		b.WriteString(strings.TrimSuffix(strings.TrimPrefix(c.src, catchShapeDecls), "\nfunc main() {}\n"))
	}
	b.WriteString("\nfunc main() {}\n")
	return b.String()
}

// catchCostControl is the hand-written Go control.
const catchCostControl = `package main

func ctlTotal(env *Env, k int) string {
	v, err := raise(env, k)
	if err == nil {
		return v
	}
	switch e := err.(type) {
	case Declined:
		if e.Reason == "card" {
			return "arm0"
		}
		return "arm1" + e.Reason
	case Refused:
		return "arm1" + e.Reason
	case Expired:
		if e.Days == 0 {
			return "arm2"
		}
	case Frozen:
		return "arm3"
	}
	return "arm4"
}

func ctlPartial(env *Env, k int) (string, error) {
	v, err := raise(env, k)
	if err == nil {
		return v, nil
	}
	switch e := err.(type) {
	case Declined:
		if e.Code == 1 {
			return "arm0" + e.Reason, nil
		}
		return "arm1" + e.Reason, nil
	case Refused:
		return "arm1" + e.Reason, nil
	case Frozen:
		return "arm2", nil
	case Expired:
		if e.Days == 0 {
			return "arm3", nil
		}
	}
	return "", err
}
`

// catchCostBench is the module's contract test and benchmarks.
const catchCostBench = `package main

import (
	"math/rand/v2"
	"testing"
)

// catchInputs is 1,024 seeded inputs of raise: 0 to 5 fail, 6 does not.
func catchInputs() []int {
	r := rand.New(rand.NewPCG(5, 6))
	in := make([]int, 1024)
	for i := range in {
		in[i] = i % 7
	}
	r.Shuffle(len(in), func(i, j int) { in[i], in[j] = in[j], in[i] })
	return in
}

var (
	sinkS string
	sinkE error
)

func TestCatchContract(t *testing.T) {
	env := &Env{}
	for k := range 7 {
		if got, want := total(env, k), ctlTotal(env, k); got != want {
			t.Errorf("total(%d) = %q, control %q", k, got, want)
		}
		got, gerr := partial(env, k)
		want, werr := ctlPartial(env, k)
		if got != want || gerr != werr {
			t.Errorf("partial(%d) = %q, %v; control %q, %v", k, got, gerr, want, werr)
		}
	}
}

func benchTotal(b *testing.B, f func(*Env, int) string) {
	env, in := &Env{}, catchInputs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sinkS = f(env, in[i&1023])
	}
}

func benchPartial(b *testing.B, f func(*Env, int) (string, error)) {
	env, in := &Env{}, catchInputs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sinkS, sinkE = f(env, in[i&1023])
	}
}

func BenchmarkCatchEmitted(b *testing.B) {
	b.Run("total", func(b *testing.B) { benchTotal(b, total) })
	b.Run("partial", func(b *testing.B) { benchPartial(b, partial) })
}

func BenchmarkCatchControl(b *testing.B) {
	b.Run("total", func(b *testing.B) { benchTotal(b, ctlTotal) })
	b.Run("partial", func(b *testing.B) { benchPartial(b, ctlPartial) })
}
`

func TestCatchCostModule(t *testing.T) {
	dir := os.Getenv("CATCH_COST_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if !filepath.IsAbs(dir) {
		t.Fatalf("CATCH_COST_DIR %q must be absolute", dir)
	}
	emitted := mustLower(t, "catchcost", catchCostSource()).mainGo()
	files := map[string]string{
		"go.mod":              goMod,
		"main.go":             emitted,
		"control.go":          catchCostControl,
		"catch_bench_test.go": catchCostBench,
	}
	for _, f := range Runtime() {
		files[f.Path] = string(f.Data)
	}
	costWrite(t, dir, files)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout=3m", "-v",
		"-run", "^TestCatchContract$", "-bench", "^BenchmarkCatch(Emitted|Control)$", "-benchtime=1x", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOFLAGS=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("catch cost module: %v\n%s\nGo:\n%s", err, output, stripDirectives(emitted))
	}
	if !strings.Contains(string(output), "--- PASS: TestCatchContract") {
		t.Errorf("the contract test did not pass:\n%s", output)
	}
	for _, side := range []string{"Emitted", "Control"} {
		for _, work := range []string{"total", "partial"} {
			name := "BenchmarkCatch" + side + "/" + work
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `(?:-\d+)?\s+1\s+\S+ ns/op`).Match(output) {
				t.Errorf("benchmark %s did not run one op:\n%s", name, output)
			}
		}
	}
}
