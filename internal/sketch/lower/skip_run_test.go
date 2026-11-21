package lower

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// skipRun reaches skip "reason" on each path of the test binary's runner
// that runs a body (runtime.md, Tests 4): an example test, a drawn
// property past its first cases, a listed property, a law run for a
// provider its generator builds, and a law run under a named context; a
// test that passes and one that fails stand after the skips.
const skipRun = `

type Shape {
	Area() int,

	law Positive() {
		if Area() > 5 {
			skip "too large for this host"
		}
		Area() > 0
	},
}

type Sq { n int } implements Shape

func Area(s Sq) int { 1 + (s.n % 3 + 3) % 3 }

type Big { n int } implements Shape

func Area(_ Big) int { 9 }

type Fn { f func(int) int } implements Shape

func Area(f Fn) int { f.f(9) }

let Wired = context { Shape: Fn{f: func(n int) int { n }} }

test Host {
	skip "git is not on this host"
}

test Drawn(n int) {
	if n > 3 || n < -3 {
		skip "drawn past three"
	}
	assert n + n == 2 * n
}

test Flags(b bool) {
	if b {
		skip "true is not this host's"
	}
	assert !b
}

test Holds {
	assert 1 + 1 == 2
}

test Fails {
	assert 1 + 1 == 3
}

func main() {}
`

// TestSkipEndsTheRun runs skipRun's test binary: each skip prints its
// line with the test's reason, runs no more of its cases, prints no ok or
// FAIL line and counts as a skip, not a test; a law run's receipt entry is
// skipped with the reason; the other tests run. It builds Go because the
// skip is a panic the built case runner recovers ($Try, $Skippable),
// which no in-process check runs. It catches tskip-not-recovered,
// tskip-no-unwind, tskip-counted-as-test, tskip-receipt-dropped,
// tskip-target-unwrapped and tskip-context-unwrapped.
func TestSkipEndsTheRun(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "skip_run", skipRun)}, true)
	if exes == nil {
		return
	}
	receipt := filepath.Join(t.TempDir(), "receipt.json")
	stdout, stderr, code := runExeArgs(t, exes["skip_run"], "--receipt", receipt)
	want := []string{
		"skip Host: git is not on this host",
		"skip Drawn: drawn past three",
		"skip Flags: true is not this host's",
		"ok Holds",
		"FAIL Fails: assert failed at main.ergo:51:2: 1 + 1 == 3",
		"ok law Shape.Positive for Sq (100 cases)",
		"skip law Shape.Positive for Big: too large for this host",
		"skip law Shape.Positive under Wired: too large for this host",
		"FAIL (1 of 3 tests, 5 skipped)",
	}
	if code != 1 || !slices.Equal(verdicts(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8))
	}
	data, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{
		`{"run": "law Shape.Positive for Big", "status": "skipped", "why": "too large for this host"}`,
		`{"run": "law Shape.Positive under Wired", "status": "skipped", "why": "too large for this host"}`,
	} {
		if !strings.Contains(string(data), entry) {
			t.Errorf("the receipt has no %s:\n%s", entry, data)
		}
	}
}
