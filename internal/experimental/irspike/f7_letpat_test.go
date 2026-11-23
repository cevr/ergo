package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRLetDefaultsMatchSource is the seeded property over lets with no
// value (F7, GenerateLetDefault): each program prints the same under both
// lowerings, and fold has an IR body. A failing seed prints the Ergo
// source, both outcomes and its replay command.
//
// It builds Go because the decoder's zero of each type (a record's and an
// array's literal, a slice's nil) is checked only by the patched compile,
// and a default read wrong shows only in the compiled program.
func TestIRLetDefaultsMatchSource(t *testing.T) {
	f7Property(t, "TestIRLetDefaultsMatchSource", GenerateLetDefault, "fold")
}

// f7Property runs the programs gen draws at ERGO_IRSPIKE_SEEDS seeds from
// ERGO_IRSPIKE_SEED under both lowerings: each prints the same, and each
// function of funcs has an IR body. Its callers state why they build Go.
func f7Property(t *testing.T, name string, gen func(uint64) string, funcs ...string) {
	t.Helper()
	f7PropertyWith(t, name, gen, nil, funcs...)
}

// f7PropertyWith is f7Property that also checks each seed's IR report
// (check reports what it finds wrong; nil checks nothing more).
func f7PropertyWith(t *testing.T, name string, gen func(uint64) string, check func(lower.IRReport) string, funcs ...string) {
	t.Helper()
	f7Run(t, name, gen, check, false, funcs...)
}

// f7PropertyEval is f7Property with the evaluator as a third oracle.
func f7PropertyEval(t *testing.T, name string, gen func(uint64) string, funcs ...string) {
	t.Helper()
	f7Run(t, name, gen, nil, true, funcs...)
}

func f7Run(t *testing.T, name string, gen func(uint64) string, check func(lower.IRReport) string, evaluator bool, funcs ...string) {
	t.Helper()
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := gen(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run " + name + " ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("the program does not check: %v\n%s\n%s", err, replay, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", a, b, replay, text)
		}
		if evaluator {
			ev, err := eval.RunMain(u.Package, eval.DefaultBudget(), eval.Process{})
			if err != nil {
				t.Fatalf("the evaluator failed: %v\n%s\n%s", err, replay, text)
			}
			e := outcome{stdout: ev.Stdout, stderr: firstLine(ev.Stderr), exit: ev.Status}
			if ev.Skipped != "" || ev.Incomplete != "" || e != a {
				t.Fatalf("seed %d: the evaluator differs\nevaluator: %+v (skipped %q, incomplete %q)\nsource and IR: %+v\n%s\n%s", seed, e, ev.Skipped, ev.Incomplete, a, replay, text)
			}
		}
		if check != nil {
			if why := check(rep); why != "" {
				t.Errorf("seed %d: %s\n%s\n%s", seed, why, replay, text)
			}
		}
		got := map[string]bool{}
		for _, f := range rep.IR {
			got[f] = true
		}
		for _, f := range funcs {
			if !got[f] {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s\n%s", seed, f, rep.IR, replay, text)
			}
		}
	}
}
