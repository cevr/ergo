package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// lawCacheSource has a rewrite law with a site (the ledger runs it), two
// pure laws that hold and one that is false.
const lawCacheSource = `import sync

type Counter {
	Add(n int),
	Read() int,
	law Joins(a int, b int) { Add(a); Add(b) } == { Add(a + b) },
}

type Tally { s sync.Ref[int] }

func NewTally() Tally { Tally{s: sync.NewRef[int](0)} }

func Add(t Tally, n int) { t.s.Set(t.s.Get() + n) }

func Read(t Tally) int { t.s.Get() }

law Doubles(x int) { x + x == 2 * x }

law Halves(x int) { x - x == 0 }

law Wrong(x int) { x + 1 == x }

func main() {
	let t = NewTally()
	t.Add(3)
	t.Add(4)
	let _ = t.Read()
}
`

// TestLawVerdictsCLI drives ergo test and ergo check as processes over
// one store (lawcache; v1 audit 3, F7): a second ergo test prints each
// law run that held "(cached)" and does not run it, a refuted law runs
// and fails every time, ergo check reports the stored verdicts cached,
// and ERGO_LAWCACHE=off runs every law.
//
// It builds Go because the test build's runner, which reads --cached and
// skips the runs it lists, is generated Go that only a built test binary
// runs; TestLawRunsOnce replaces it in process.
//
// Mutants it kills: lc-runner-ignores-cached (the runner runs a cached
// law), lc-test-no-record (ergo test stores no verdict).
func TestLawVerdictsCLI(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	root := physicalTempDir(t)
	xdg := filepath.Join(t.TempDir(), "xdg")
	tmp := t.TempDir()
	source := smokeWrite(t, root, "main.ergo", lawCacheSource)
	held := []string{"ok law Counter.Joins for Tally (", "ok law Doubles (100 cases)", "ok law Halves (100 cases)"}
	test := func(env ...string) string {
		t.Helper()
		t.Setenv("ERGO_LAWCACHE", "")
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			t.Setenv(k, v)
		}
		stdout, stderr, code := testRunCLI(t, binary, root, xdg, tmp, "test", source)
		if code != 1 || !strings.Contains(stdout, "FAIL law Wrong (") || strings.Contains(stdout, "law Wrong (100 cases)") {
			t.Fatalf("ergo test: exit %d, want 1 with law Wrong refuted\n%s\n%s", code, stdout, stderr)
		}
		return stdout
	}
	for _, out := range []string{test(), test("ERGO_LAWCACHE=off")} {
		for _, line := range held {
			if !strings.Contains(out, line) || strings.Contains(out, "(cached)") {
				t.Fatalf("a first run, or a run with the store off, prints %q uncached:\n%s", line, out)
			}
		}
	}
	out := test()
	for _, line := range held {
		i := strings.Index(out, line)
		if i < 0 || !strings.HasPrefix(out[i+strings.Index(out[i:], ")"):], ") (cached)\n") {
			t.Fatalf("a second ergo test does not print %q cached:\n%s", line, out)
		}
	}
	stdout, stderr, _ := testRunCLI(t, binary, root, xdg, tmp, "check", source)
	var envelope struct {
		Evaluation struct {
			Obligations []struct {
				Name   string `json:"name"`
				Cached bool   `json:"cached"`
			} `json:"obligations"`
		} `json:"evaluation"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("ergo check: %v\n%s\n%s", err, stdout, stderr)
	}
	cached := map[string]bool{}
	for _, o := range envelope.Evaluation.Obligations {
		cached[o.Name] = o.Cached
	}
	if !cached["law Doubles"] || !cached["law Halves"] || cached["law Wrong"] {
		t.Fatalf("ergo check after ergo test: cached %v, want the laws that held and not Wrong\n%s", cached, stdout)
	}
}
