package eval_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/eval/evalgen"
	"ergo.local/prototype/internal/sketchfront"
)

// TestEvalAgreesWithLowering holds a program's main at check time to the
// model and to the built program (ORACLE O4). Each seed's evalgen main
// program (scenarios of owned resources, tasks over a queue, sleeps on a
// virtual clock, a stopped task, standard input, arguments, standard error
// and an os.Exit) runs under eval.RunMain with the seed's arguments and
// input; its standard output, standard error and status must be the
// model's, and the seeds must reach every scenario. In the full tier the
// first seeds are also built with the ergo command and run: the built
// program's streams and status must be the evaluator's. A failing seed
// prints the source, both runs and the replay command.
//
// It builds Go because the other side of the agreement is the built
// program; the in-process part needs no build and runs in every tier. Each
// built program runs under proclimit's memory bound.
//
// Mutants only it kills (testdata/mutants.json): oracle-o4-exit-code-ignored,
// oracle-o4-stdin-line-feed-kept, oracle-o4-console-to-stderr.
func TestEvalAgreesWithLowering(t *testing.T) {
	n, built := 60, 6
	first := uint64(1)
	if s := os.Getenv("ERGO_EVAL_MAIN_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n, built = v, 1, 1
	}
	reached := map[string]bool{}
	runs := map[uint64]eval.Exit{}
	programs := map[uint64]evalgen.MainProgram{}
	for seed := first; seed < first+uint64(n); seed++ {
		p := evalgen.GenerateMain(seed)
		programs[seed] = p
		for k := range p.Kinds {
			reached[k] = true
		}
		a := sketchfront.Check(p.Source)
		if len(a.Diagnostics) > 0 {
			t.Fatalf("seed %d: %v\n%s", seed, a.Diagnostics, p.Source)
		}
		pkgs := a.Checked()
		got, err := eval.RunMain(pkgs[len(pkgs)-1], eval.DefaultBudget(), eval.Process{Args: p.Args, Stdin: p.Stdin})
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, p.Source)
		}
		want := eval.Exit{Stdout: p.Stdout, Stderr: p.Stderr, Status: p.Status}
		if d := runsDiffer(got, want); d != "" {
			t.Fatalf("seed %d: the evaluator and the model differ: %s\n%s\nreplay: ERGO_EVAL_MAIN_SEED=%d go test -run TestEvalAgreesWithLowering ./internal/sketch/eval", seed, d, p.Source, seed)
		}
		runs[seed] = got
	}
	if n > 1 {
		for _, k := range evalgen.MainKinds {
			if !reached[k] {
				t.Fatalf("the seeds never reached the scenario %s (reached %v)", k, reached)
			}
		}
	}
	if testing.Short() {
		return // the built side is the full tier's
	}
	ergo := buildErgo(t)
	for seed := first; seed < first+uint64(built); seed++ {
		p := programs[seed]
		native := runBuilt(t, ergo, p)
		if d := runsDiffer(runs[seed], native); d != "" {
			t.Fatalf("seed %d: the evaluator and the built program differ: %s\n%s\nreplay: ERGO_EVAL_MAIN_SEED=%d go test -run TestEvalAgreesWithLowering ./internal/sketch/eval", seed, d, p.Source, seed)
		}
	}
}

// runsDiffer says how two runs differ, "" when they do not.
func runsDiffer(got, want eval.Exit) string {
	if got.Skipped != "" || got.Incomplete != "" {
		return fmt.Sprintf("the run did not end: %s%s at %s:%d", got.Skipped, got.Incomplete, got.Site.File, got.Site.Line)
	}
	return disagreement(golden{stdout: want.Stdout, errs: want.Stderr, status: want.Status}, got)
}

// buildErgo builds the ergo command into the test's directory.
// It builds Go because the built program is the other side of the
// agreement, and the ergo command is what builds it (the evaluator may
// not import lower).
func buildErgo(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "ergo")
	cmd := exec.Command("go", "build", "-o", exe, "ergo.local/prototype/cmd/ergo")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ergo: %v\n%s", err, out)
	}
	return exe
}

// runBuilt builds p with ergo build and runs it on p's arguments and
// input, under the memory bound.
func runBuilt(t *testing.T, ergo string, p evalgen.MainProgram) eval.Exit {
	t.Helper()
	dir := filepath.Join(t.TempDir(), fmt.Sprintf("main%d", p.Seed))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.ergo"), []byte(p.Source), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "prog")
	if out, err := exec.Command(ergo, "build", dir, "-o", exe).CombinedOutput(); err != nil {
		t.Fatalf("seed %d: ergo build: %v\n%s\n%s", p.Seed, err, out, p.Source)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(exe, p.Args...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, strings.NewReader(p.Stdin), &stdout, &stderr
	cmd.Env = []string{"HOME=" + dir}
	err := proclimit.Run(cmd, proclimit.DefaultLimit)
	status := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		status = exit.ExitCode()
	case err != nil:
		t.Fatalf("seed %d: run: %v", p.Seed, err)
	}
	return eval.Exit{Stdout: stdout.String(), Stderr: stderr.String(), Status: status}
}
