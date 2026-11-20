package main

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// traceProgram is a ui test package whose two tests play the same scripts,
// drawn from base: ByPlayTrace through PlayTrace, whose lines it prints by
// a failed assert, and ByPlay through Play.
const traceProgram = `import actor
import time
import ui

func step(s int, e int) Decision[int, int] {
	Enter[int]{Next: s + e}
}

func timer(s int) Option[Timeout[int]] {
	None
}

func work(s int) Option[func(send func(e int))] {
	None
}

func view(f mut ui.Frame[int], s int) {
	ui.Column(f, ui.Plain()) {
		ui.Text(f, "at \(s)")
		ui.Pressable(f, "add", 1) {
			ui.Text(f, "+")
		}
		ui.Pressable(f, "sub", -1) {
			ui.Text(f, "-")
		}
	}
}

func script(seed int) []ui.Act {
	let mut x = seed
	let mut out = []ui.Act{}
	for _ in 0..(seed%4 + 1) {
		x = (x*1103515245 + 12345) % 2147483648
		match (x / 65536) % 4 {
		case 0:
			out = out.Append(ui.TapOn{Label: "add"})
		case 1:
			out = out.Append(ui.ClickOn{At: ui.WithLabel{Label: "sub"}})
		case 2:
			out = out.Append(ui.AdvanceBy{By: 16ms})
		case _:
			out = out.Append(ui.SettleAll)
		}
	}
	out
}

func lines(seed int, traced bool) []string fails ui.NoNode | ui.Ambiguous | ui.Mismatch | actor.Stopped | ui.BadPatch {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: step, After: timer, Work: work, Exit: func(s int) Option[func()] { None }}, 1, func(o int) {})
		let mut h = try ui.NewTestHost(a, view, m, 4)
		if traced {
			return try ui.PlayTrace(&h, script(seed))
		}
		try ui.Play(&h, script(seed))
		[]string{}
	}
}

func main() {}

test ByPlayTrace {
	let mut all = []string{}
	for seed in BASE..BASE+6 {
		for l in try lines(seed, true) {
			all = all.Append(l)
		}
	}
	assert all == []string{"-"}
}

test ByPlay {
	for seed in BASE..BASE+6 {
		let _ = try lines(seed, false)
	}
}
`

// TestTestTrace: with --trace, ergo test prints after the run each act its
// tests played, in order, as PlayTrace's line for it, and tracing changes
// nothing the tests see. Over scripts drawn from a seeded base, the run's
// verdicts and the returned lines (which ByPlayTrace's failed assert
// prints) are the untraced run's, and the printed steps are those lines
// twice: PlayTrace's, then Play's of the same scripts. A failure prints
// the base, the source, both outputs and the replay (ERGO_TRACE_BASE).
//
// It builds Go because the trace is the test binary's: its root records
// it when main ends, and `ergo test` reads it after the child exits. The
// child runs under ergo test's own memory limit (proclimit).
func TestTestTrace(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	root := physicalTempDir(t)
	xdg := filepath.Join(t.TempDir(), "xdg")
	tmp := t.TempDir()
	cwd := t.TempDir()
	base := rand.IntN(1 << 20)
	if s := os.Getenv("ERGO_TRACE_BASE"); s != "" {
		base, _ = strconv.Atoi(s)
	}
	source := strings.ReplaceAll(traceProgram, "BASE", strconv.Itoa(base))
	file := smokeWrite(t, root, "trace.ergo", source)
	plain, _, plainCode := testRunCLI(t, binary, cwd, xdg, tmp, "test", file)
	traced, stderr, tracedCode := testRunCLI(t, binary, cwd, xdg, tmp, "test", file, "--trace")
	fail := func(why string) {
		t.Helper()
		t.Fatalf("%s (base %d; replay: ERGO_TRACE_BASE=%d go test ./cmd/ergo -run '^TestTestTrace$')\nsource:\n%s\nuntraced (exit %d):\n%s\ntraced (exit %d):\n%s\nstderr: %s", why, base, base, source, plainCode, plain, tracedCode, traced, stderr)
	}
	verdicts, steps, ok := strings.Cut(traced, "trace: ")
	if !ok || tracedCode != plainCode || verdicts != plain {
		fail("the traced run's verdicts are not the untraced run's")
	}
	left := regexp.MustCompile(`left \[\]string\{(.*)\}, right`).FindStringSubmatch(plain)
	if plainCode != 1 || left == nil {
		fail("ByPlayTrace printed no lines")
	}
	var want []string
	for _, q := range regexp.MustCompile(`"(?:[^"\\]|\\.)*"`).FindAllString(left[1], -1) {
		s, err := strconv.Unquote(q)
		if err != nil {
			fail(err.Error())
		}
		want = append(want, s)
	}
	want = append(want, want...)
	got := strings.Split(strings.TrimSuffix(steps, "\n"), "\n")
	if got[0] != strconv.Itoa(len(want))+" steps" || strings.Join(got[1:], "\n") != strings.Join(want, "\n") {
		fail("the printed steps are not PlayTrace's lines, then Play's")
	}
	// --trace takes one package, played plainly.
	for _, args := range [][]string{{"test", root + "/...", "--trace"}, {"test", file, "--trace", "--explore"}} {
		if stdout, stderr, code := testRunCLI(t, binary, cwd, xdg, tmp, args...); code != 2 || stdout != "" || !strings.Contains(stderr, "--trace takes one package") {
			t.Fatalf("ergo %v: exit %d stdout %q stderr %q; want the usage error", args, code, stdout, stderr)
		}
	}
}
