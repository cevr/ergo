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
// law's replay line. The seeds must reach a pass, a proved pass, a
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
// dropped.
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
			native := runNativeTests(t, ergo, file, seed)
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
	for _, k := range []string{"passed", "proved", "shrunk", "law", "shown"} {
		if !reached[k] {
			t.Fatalf("the seeds never reached a %s verdict (reached %v)", k, reached)
		}
	}
}

// nativeHas reports whether the native output holds the verdict line
// whole: it starts a line, and what follows it starts the next verdict
// ("ok ", "FAIL ", "skip ") or ends the output, so a line that leaves out
// the native report's last lines (a diff, a replay) does not match.
func nativeHas(native, line string) bool {
	text := "\n" + native
	for from := 0; ; {
		i := strings.Index(text[from:], "\n"+line+"\n")
		if i < 0 {
			return false
		}
		rest := text[from+i+len(line)+2:]
		if rest == "" || strings.HasPrefix(rest, "ok ") || strings.HasPrefix(rest, "FAIL ") || strings.HasPrefix(rest, "skip ") {
			return true
		}
		from += i + 1
	}
}

// runNativeTests runs `ergo test file --seed seed` under the memory bound
// and returns its standard output.
func runNativeTests(t *testing.T, ergo, file string, seed uint64) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(ergo, "test", file, "--seed", strconv.FormatUint(seed, 10))
	cmd.Dir, cmd.Stdout, cmd.Stderr = filepath.Dir(file), &stdout, &stderr
	err := proclimit.Run(cmd, proclimit.DefaultLimit)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("ergo test %s: %v\n%s", file, err, stderr.String())
	}
	return stdout.String()
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
