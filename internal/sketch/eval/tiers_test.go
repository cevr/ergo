package eval_test

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/eval/casegen"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestTiersAgree is FT1's tier agreement (TESTSPEED plan §4.1): for each
// seed's programs (casegen's case plans, and assertGen's asserts, laws
// and tests of seeded bounds), every obligation the evaluator decides
// (eval.VerdictLine reports a line) has, in the native runner's output of
// `ergo test FILE --seed N`, exactly that line: the verdict, the case
// number, the shrunk and drawn arguments, the report after "): " and a
// law's replay line. When the evaluator decides the whole package
// (eval.Summary), its standard output is the native run's, byte for byte,
// with the same exit status and error line; a program with a defect, which
// stops the native run, is not one it decides. The seeds must reach a pass, a proved pass, a
// shrunk refutation, a refuted law and a shown comparison. A failing seed
// prints the source, both outputs and the replay command.
//
// It builds Go because the other tier is the built test binary: only its
// run prints the native line, and the evaluator may not import lower to
// write it. It is the full tier's (-short skips it); each test binary runs
// under proclimit's memory bound.
//
// Mutants only it kills (testdata/mutants.json): oracle-ft1-law-replay-
// seedless, oracle-ft1-message-site-unnamed, oracle-ft1-shown-operands-
// dropped, oracle-ft1-string-diff-dropped, oracle-ft1-summary-failed-
// uncounted, oracle-ft1-defect-decided.
func TestTiersAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds the native test binaries")
	}
	first, last := uint64(1), uint64(3)
	if s := os.Getenv("ERGO_TIERS_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n
	}
	ergo := buildErgo(t)
	reached := map[string]bool{}
	for seed := first; seed <= last; seed++ {
		for _, prog := range []struct{ name, src string }{
			{"cases", casegen.Generate(seed).Source},
			{"asserts", assertGen(seed)},
			{"holds", holdsGen(seed)},
			{"defect", holdsGen(seed) + "\ntest Boom(n int) {\n\tassert 10 / (n - n) == 0\n}\n\ntest After() {\n\tassert true\n}\n"},
			{"budget", budgetGen(seed)},
		} {
			dir := filepath.Join(t.TempDir(), fmt.Sprintf("%s%d", prog.name, seed))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "main.ergo")
			if err := os.WriteFile(file, []byte(prog.src), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := syntax.Parse(prog.src)
			if err != nil {
				t.Fatalf("seed %d %s: %v\n%s", seed, prog.name, err, prog.src)
			}
			f.Name = "main.ergo" // as ergo test names the file it reads
			pkg, ds := check.Check("main", []*syntax.File{f}, nil)
			if len(ds) > 0 {
				t.Fatalf("seed %d %s: %v\n%s", seed, prog.name, ds, prog.src)
			}
			b := eval.DefaultBudget()
			b.Seed = seed
			rep, err := eval.Run(pkg, b)
			if err != nil {
				t.Fatal(err)
			}
			native, nativeErr, nativeExit := runNativeTests(t, ergo, file, seed)
			if runDefaultTests(t, ergo, file, seed, prog.src, native, nativeErr, nativeExit) {
				reached["evaluated"] = true
				if strings.Contains(native, "\nok law ") || strings.HasPrefix(native, "ok law ") {
					// Both tiers have now stored this package's law
					// verdicts; --native (the gate) must still run every
					// law, never print one from the store (FT2).
					again, _, _ := runErgoTest(t, ergo, file, seed, "--native", "--count", "1")
					if again != native {
						t.Fatalf("seed %d %s: a second ergo test --native read the verdict store\nfirst:\n%s\nsecond:\n%s\nreplay: ERGO_TIERS_SEED=%d go test -run '^TestTiersAgree$' ./internal/sketch/eval", seed, prog.name, native, again, seed)
					}
					reached["native rerun"] = true
				}
			}
			if lines, stderr, exit, ok := eval.Summary(rep.Results, file); ok {
				// The evaluator decides the package: the whole run is its.
				stdout := strings.Join(lines, "\n") + "\n"
				if stdout != native || exit != nativeExit || (stderr != "" && !strings.Contains("\n"+nativeErr, "\n"+stderr+"\n")) {
					t.Fatalf("seed %d %s: the evaluator's run is not the native runner's\nevaluator (exit %d, stderr %q):\n%s\nnative (exit %d, stderr %q):\n%s\n%s\nreplay: ERGO_TIERS_SEED=%d go test -run '^TestTiersAgree$' ./internal/sketch/eval", seed, prog.name, exit, stderr, stdout, nativeExit, nativeErr, native, prog.src, seed)
				}
				reached[fmt.Sprintf("summary exit %d", exit)] = true
			}
			for _, r := range rep.Results {
				line, ok := eval.VerdictLine(r, file)
				if !ok {
					continue
				}
				if !nativeHas(native, line) {
					t.Fatalf("seed %d %s: %s: the evaluator's line is not the native runner's\nevaluator: %s\nnative:\n%s\n%s\nreplay: ERGO_TIERS_SEED=%d go test -run '^TestTiersAgree$' ./internal/sketch/eval", seed, prog.name, r.Name, line, native, prog.src, seed)
				}
				switch {
				case r.Status == eval.Passed && r.Evidence == "proved":
					reached["proved"] = true
				case r.Status == eval.Passed:
					reached["passed"] = true
				case r.Drawn != nil:
					reached["shrunk"] = true
				}
				if r.Status == eval.Refuted && strings.HasPrefix(r.Name, "law ") {
					reached["law"] = true
				}
				if strings.Contains(r.Message, ": left ") {
					reached["shown"] = true
				}
			}
		}
	}
	if first == last {
		return
	}
	for _, k := range []string{"passed", "proved", "shrunk", "law", "shown", "summary exit 0", "summary exit 1", "evaluated", "native rerun"} {
		if !reached[k] {
			t.Fatalf("the seeds never reached a %s verdict (reached %v)", k, reached)
		}
	}
}

// nativeHas reports whether the native output holds the verdict line
// whole: it starts a line, and what follows it starts the next verdict
// ("ok ", "FAIL ", "skip "), the summary ("PASS (") or ends the output, so a line that leaves out
// the native report's last lines (a diff, a replay) does not match.
func nativeHas(native, line string) bool {
	text := "\n" + native
	for from := 0; ; {
		i := strings.Index(text[from:], "\n"+line+"\n")
		if i < 0 {
			return false
		}
		rest := text[from+i+len(line)+2:]
		if rest == "" || strings.HasPrefix(rest, "ok ") || strings.HasPrefix(rest, "FAIL ") || strings.HasPrefix(rest, "skip ") || strings.HasPrefix(rest, "PASS (") {
			return true
		}
		from += i + 1
	}
}

// runNativeTests runs `ergo test --native file --seed seed` under the
// memory bound and returns its standard output, standard error and exit
// status.
func runNativeTests(t *testing.T, ergo, file string, seed uint64) (string, string, int) {
	return runErgoTest(t, ergo, file, seed, "--native")
}

// runDefaultTests is the CLI half of the agreement (TESTSPEED FT1, CS's
// side): `ergo test file --seed seed`, which the evaluator answers when it
// decides the whole package, prints what the native run (`--native`,
// given as native, nativeErr and nativeCode) prints, each verdict line
// ending " (evaluated)", and exits with its status; the native run prints
// no such line. It reports whether the evaluator answered.
//
// Mutants only it kills: ft-incomplete-reads-as-pass, ft-undecided-dropped,
// ft-seed-not-shared, ft-evidence-says-native (internal/sketchfront) and
// ft1-native-flag-ignored (cmd/ergo).
func runDefaultTests(t *testing.T, ergo, file string, seed uint64, src, native, nativeErr string, nativeCode int) bool {
	t.Helper()
	out, errText, code := runErgoTest(t, ergo, file, seed)
	evaluated := strings.Contains(out, " (evaluated)\n")
	// The native run left its law verdicts in the store, so this run may
	// print one " (cached)" (lawcache).
	stripped := strings.ReplaceAll(strings.ReplaceAll(out, " (evaluated)\n", "\n"), " (cached)\n", "\n")
	if strings.Contains(native, " (evaluated)") || stripped != native || code != nativeCode || errorLines(errText) != errorLines(nativeErr) {
		t.Fatalf("seed %d: ergo test and ergo test --native disagree (evaluated: %v)\nsource:\n%s\nergo test (exit %d):\n%s%s\nergo test --native (exit %d):\n%s%s\nreplay: ERGO_TIERS_SEED=%d go test -run '^TestTiersAgree$' ./internal/sketch/eval", seed, evaluated, src, code, out, errText, nativeCode, native, nativeErr, seed)
	}
	return evaluated
}

// errorLines are the "error: " lines of a run's standard error, the test
// binary's, without the CLI's notes (a toolchain build).
func errorLines(stderr string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(stderr, "\n") {
		if strings.HasPrefix(line, "error: ") {
			b.WriteString(line)
		}
	}
	return b.String()
}

// budgetGen is a program whose one failing test the evaluator leaves
// incomplete (its loop outruns the default budget of steps) beside a test
// it decides: the native binary must answer the whole package, so the
// failure is reported, not dropped or read as a pass.
func budgetGen(seed uint64) string {
	return fmt.Sprintf(`func spin(k int) int {
	let mut n = 0
	for n < k {
		n = n + 1
	}
	n
}

test Quick() {
	assert spin(%d) == %d
}

test Spins() {
	assert spin(2000000) == 1
}
`, seed, seed)
}

// runErgoTest runs `ergo test file --seed seed` with extra flags under
// the memory bound and returns its standard output, standard error and
// exit status.
func runErgoTest(t *testing.T, ergo, file string, seed uint64, extra ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(ergo, append([]string{"test", file, "--seed", strconv.FormatUint(seed, 10)}, extra...)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = filepath.Dir(file), &stdout, &stderr
	err := proclimit.Run(cmd, proclimit.DefaultLimit)
	var exit *exec.ExitError
	code := 0
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatalf("ergo test %s: %v\n%s", file, err, stderr.String())
	}
	return stdout.String(), stderr.String(), code
}

// holdsGen is a program whose tests and laws hold, with seeded bounds: an
// example, a property, a drawn law and a listed one.
func holdsGen(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x401d))
	k := 1 + r.IntN(1000)
	return fmt.Sprintf(`func clamp(n int) int {
	if n > %d {
		return %d
	}
	n
}

test Example() {
	assert clamp(%d) == %d
}

test Clamped(n int) {
	assert clamp(n) <= %d
}

law Idempotent(n int) {
	clamp(clamp(n)) == clamp(n)
}

law Either(a bool, b bool) {
	a || !a || b
}
`, k, k, k+1, k, k)
}

// assertGen is a program of asserts, tests and laws with seeded bounds: a
// shown comparison of ints, of strings over more than one line (a diff),
// of a record; an assert the report does not show; a test with no
// parameters; a refuted drawn law and a refuted listed one; and passes.
func assertGen(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x71e5))
	k, l, m := 1+r.IntN(500), 1+r.IntN(4), 2+r.IntN(6)
	return fmt.Sprintf(`type Pair { A int, B bool }

func twice(n int) int { n + n }

test Bound(n int) {
	assert twice(n) < %d
}

test Lines(s string) {
	assert s + "\nend" == "start\nend"
}

test Record(a int) {
	assert Pair{A: a, B: true} == Pair{A: 0, B: true}
}

test Plain(xs []int) {
	assert len(xs) < %d && true
}

test Fixed() {
	assert twice(%d) == %d
}

test Holds(n int8) {
	assert twice(int(n)) == int(n) * 2
}

law Small(n int) {
	n < %d
}

law Flags(a bool, b bool) {
	a || b
}

law Commutes(a int, b int) {
	a + b == b + a
}
`, 2*k, l, m, 2*m+1, k)
}
