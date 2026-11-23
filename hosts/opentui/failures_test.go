package opentui

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var failSeed = flag.Uint64("fail.seed", 0, "run TestTuiFailuresAgree on this seed only")

// callNames are tui.CallName's names of screen's calls, by number
// (screen.CallOpen to screen.CallQuit).
var callNames = []string{"open", "scene", "frame", "output", "scroll", "edit", "notify", "resize", "input", "quit"}

// failStatuses are library status codes (ot.Status) a check fails with.
var failStatuses = []int{-1, -3, -5, -9, -11, -12, -15, -16, -26}

// failRun is one hostagree run with the screen's at-th check failing
// with status: the lines before the checks, the checks ("CALL NODE"),
// and the screens left unquit.
type failRun struct {
	lines  []string
	checks []string
	live   int
}

func playFailing(bin, script string, at, status int) (failRun, string, error) {
	text, err := play(bin, fmt.Sprintf("fail %d %d\n", at, status)+script)
	var r failRun
	r.live = -1
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "check "):
			r.checks = append(r.checks, strings.TrimPrefix(l, "check "))
		case strings.HasPrefix(l, "live "):
			r.live, _ = strconv.Atoi(strings.TrimPrefix(l, "live "))
		default:
			r.lines = append(r.lines, l)
		}
	}
	return r, text, err
}

// TestTuiFailuresAgree: a screen failure ends a terminal run the way a
// small reference model says, on every path, and the screen is quit on
// each. hostagree plays a seeded script of each app once with no check
// failing (the model's record: its checks, its lines), then with the
// screen's AT-th check failing with a seeded library status, for AT the
// first check, seeded ones, the last (Quit's) and one past the last (no
// failure). The model: a run whose AT-th check failed prints the lines
// of the full run before the failure, then "failed CALL NODE STATUS WHY"
// with the call and node of the AT-th check and that status; its checks
// are the full run's first AT, then Quit's, unless the AT-th was Quit's;
// a run past the last check is the full run; no run leaves a screen
// unquit ("live 0"). A failing seed prints the first divergent event and
// the replay command.
//
// It builds and runs a Go binary because the driver is Ergo source that
// calls Go (go.Foreign): no in-process check runs it.
//
// Mutants (testdata/mutants.json): OTF-status-dropped, OTF-node-zero,
// OTF-quit-skipped, OTF-quit-hides-body, OTF-quit-swallowed.
func TestTuiFailuresAgree(t *testing.T) {
	bin, err := build("hostagree", "./cmd/hostagree")
	if err != nil {
		t.Fatal(err)
	}
	seeds := []uint64{}
	if *failSeed != 0 {
		seeds = append(seeds, *failSeed)
	} else {
		for s := uint64(1); s <= uint64(len(agreeApps)); s++ {
			seeds = append(seeds, s)
		}
	}
	failed, runs := 0, 0
	for _, seed := range seeds {
		app := agreeApps[seed%uint64(len(agreeApps))]
		r := rand.New(rand.NewPCG(seed, 0x6661696c))
		script := genScript(r, app)
		full, text, err := playFailing(bin, script, 0, 0)
		if err != nil || full.live != 0 || len(full.checks) < 2 {
			t.Fatalf("seed %d (%s): the run with no failure: %v\n%s\nscript:\n%s", seed, app, err, text, script)
		}
		k := len(full.checks)
		ats := []int{1, k, k + 1}
		for range 3 {
			ats = append(ats, 2+r.IntN(k-1))
		}
		for _, at := range ats {
			runs++
			st := failStatuses[r.IntN(len(failStatuses))]
			got, text, err := playFailing(bin, script, at, st)
			why := diverged(full, got, at, st)
			if err != nil {
				why = fmt.Sprintf("hostagree: %v", err)
			}
			if why == "" {
				continue
			}
			failed++
			if failed <= 3 {
				t.Errorf("seed %d (%s), check %d of %d failing with %d: %s\noutput:\n%s\nscript:\n%s\nreplay: go test -run TestTuiFailuresAgree -fail.seed=%d", seed, app, at, k, st, why, text, script, seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more runs differ", failed-3)
	}
	t.Logf("%d seeds, %d failing runs agree with the model", len(seeds), runs-failed)
}

// diverged is the first event where got, the run with the at-th check
// failing with status, leaves the model built from full, the run with no
// failure; "" when it does not.
func diverged(full, got failRun, at, status int) string {
	if got.live != 0 {
		return fmt.Sprintf("live %d: the run left a screen unquit", got.live)
	}
	k := len(full.checks)
	if at > k {
		if !slices.Equal(got.lines, full.lines) || !slices.Equal(got.checks, full.checks) {
			return "with no check failing, the run is not the full run"
		}
		return ""
	}
	var call, node int
	fmt.Sscanf(full.checks[at-1], "%d %d", &call, &node)
	// After the failure the screen quits (its Quit's check), unless no
	// screen was made (open) or the failure was Quit's.
	want := slices.Clone(full.checks[:at])
	if callNames[call] != "open" && callNames[call] != "quit" {
		want = append(want, fmt.Sprintf("%d 0", slices.Index(callNames, "quit")))
	}
	for i := range max(len(want), len(got.checks)) {
		if i >= len(want) || i >= len(got.checks) || want[i] != got.checks[i] {
			return fmt.Sprintf("check %d: want %q, got %q (want %d checks, got %d)", i+1, at1(want, i), at1(got.checks, i), len(want), len(got.checks))
		}
	}
	head := fmt.Sprintf("failed %s %d %d ", callNames[call], node, status)
	last := len(got.lines) - 1
	if last < 0 || !strings.HasPrefix(got.lines[last], head) {
		return fmt.Sprintf("the run's last line is not %q…: %q", head, at1(got.lines, last))
	}
	// Before the failure, the full run's lines: each step's ok, and "end"
	// when Quit's check failed after the steps.
	for i, l := range got.lines[:last] {
		if i >= len(full.lines) || full.lines[i] != l {
			return fmt.Sprintf("line %d: %q, the full run has %q", i+1, l, at1(full.lines, i))
		}
	}
	return ""
}

func at1(s []string, i int) string {
	if i < 0 || i >= len(s) {
		return "(none)"
	}
	return s[i]
}
