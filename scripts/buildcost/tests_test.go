package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// probeSeed is the first seed of the drawn tables (TPROBE_SEED, default 1).
func probeSeed(t *testing.T) uint64 {
	t.Helper()
	s := os.Getenv("TPROBE_SEED")
	if s == "" {
		return 1
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatalf("TPROBE_SEED=%q: %v", s, err)
	}
	return n
}

// probeDraws is the number of tables each test draws.
const probeDraws = 3000

// drawCounts is five counts around base: each within jitter of it.
func drawCounts(r *rand.Rand, base, jitter float64) []uint64 {
	out := make([]uint64, 5)
	for i := range out {
		out[i] = uint64(base * (1 + jitter*(2*r.Float64()-1)))
	}
	return out
}

// verdictTable is one drawn table: per side and scenario five counts and
// whether the row stayed within spread, and each Ergo form's kills.
type verdictTable struct {
	Counts map[string]map[string][]uint64
	Within map[string]map[string]bool
	Kills  map[string]int
	N      int
}

func drawTable(r *rand.Rand) verdictTable {
	tab := verdictTable{Counts: map[string]map[string][]uint64{}, Within: map[string]map[string]bool{}, Kills: map[string]int{}, N: 1 + r.IntN(17)}
	// Factors and jitters around the 2 % line on both sides of it, so
	// every verdict and both sides of each rule are drawn.
	factors := []float64{0.5, 0.9, 0.975, 0.985, 1, 1.015, 1.025, 1.1, 2}
	jitters := []float64{0, 0.001, 0.005, 0.02, 0.1}
	for _, side := range []string{"go", "ergo-test", "ergo-check"} {
		tab.Counts[side], tab.Within[side] = map[string][]uint64{}, map[string]bool{}
	}
	for _, s := range probeScenarios {
		base := 1e9 * (1 + 100*r.Float64())
		tab.Counts["go"][s] = drawCounts(r, base, jitters[r.IntN(len(jitters))])
		tab.Within["go"][s] = r.IntN(10) == 0
		for _, side := range probeErgoForms {
			tab.Counts[side][s] = drawCounts(r, base*factors[r.IntN(len(factors))], jitters[r.IntN(len(jitters))])
			tab.Within[side][s] = r.IntN(10) == 0
		}
	}
	for _, side := range probeErgoForms {
		tab.Kills[side] = tab.N
		if r.IntN(3) == 0 {
			tab.Kills[side] = r.IntN(tab.N)
		}
	}
	return tab
}

func (tab verdictTable) side(name string) probeSide {
	s := probeSide{Mutants: tab.N, Kills: tab.N, Counted: true, Rows: map[string]probeStats{}}
	if name != "go" {
		s.Kills = tab.Kills[name]
	}
	for _, sc := range probeScenarios {
		st := probeStatsOf(tab.Counts[name][sc])
		st.WithinSpread = tab.Within[name][sc]
		s.Rows[sc] = st
	}
	return s
}

func (tab verdictTable) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mutants %d, kills ergo-test %d, ergo-check %d\n", tab.N, tab.Kills["ergo-test"], tab.Kills["ergo-check"])
	for _, side := range []string{"go", "ergo-test", "ergo-check"} {
		for _, s := range probeScenarios {
			fmt.Fprintf(&b, "  %-10s %-4s %v within spread %v\n", side, s, tab.Counts[side][s], tab.Within[side][s])
		}
	}
	return b.String()
}

// refDiffer is the reference reading of Measure's "differs", from the raw
// counts: -1 when a is lower, 1 when higher, 0 when the two rows'
// least-to-most ranges meet, their medians are within 2 % of the lower
// one, or either row stayed within spread.
func refDiffer(a, b []uint64, withinA, withinB bool) int {
	if withinA || withinB {
		return 0
	}
	sa, sb := slices.Clone(a), slices.Clone(b)
	slices.Sort(sa)
	slices.Sort(sb)
	if sa[len(sa)-1] >= sb[0] && sb[len(sb)-1] >= sa[0] {
		return 0
	}
	ma, mb := float64(sa[len(sa)/2]), float64(sb[len(sb)/2])
	if ma < mb && mb-ma > 0.02*ma {
		return -1
	}
	if mb < ma && ma-mb > 0.02*mb {
		return 1
	}
	return 0
}

// refVerdict is Measure's verdict from refDiffer ("Any gain ports"), as
// the verdict, the faster scenarios and the slower ones, in that order.
func refVerdict(tab verdictTable, ergo string) (string, []string, []string) {
	var faster, slower []string
	for _, s := range probeScenarios {
		switch refDiffer(tab.Counts[ergo][s], tab.Counts["go"][s], tab.Within[ergo][s], tab.Within["go"][s]) {
		case -1:
			faster = append(faster, s)
		case 1:
			slower = append(slower, s)
		}
	}
	switch {
	case len(faster) > 0:
		return "win", faster, slower
	case len(slower) == 0:
		return "tie", faster, slower
	}
	return "loss", faster, slower
}

// TestProbeVerdictRule holds the meter's verdict to the rule of the TPROBE
// spec (Measure, "Verdict per kind"; owner, 2026-11-18: "Any gain ports")
// on drawn result tables: (1) no speed verdict comes from an Ergo form
// that misses a kill, and the counts of such a form change nothing; (2)
// each form's verdict is the reference reading of the rule from the raw
// counts (win exactly when some scenario is faster, on disjoint ranges and
// a median lower by more than 2 %, whatever the others show, with every
// slower scenario listed; tie exactly when no scenario differs), and the
// kind is a win when a covered form wins; (3) each scenario's comparison
// is antisymmetric, and exchanging the two sides turns a win with no
// slower scenario into a loss and back, keeps a tie, and keeps a win that
// has a slower scenario.
//
// Mutants it alone catches: scripts/buildcost/testdata/mutants.json,
// killer scripts/buildcost:TestProbeVerdictRule.
func TestProbeVerdictRule(t *testing.T) {
	seed := probeSeed(t)
	r := rand.New(rand.NewPCG(seed, 0x7072))
	seen := map[string]int{}
	for i := range probeDraws {
		tab := drawTable(r)
		goSide := tab.side("go")
		ergo := map[string]probeSide{"ergo-test": tab.side("ergo-test"), "ergo-check": tab.side("ergo-check")}
		v, from := probeKindVerdict(goSide, ergo)
		fail := func(format string, a ...any) {
			t.Fatalf("seed %d, table %d: %s\nverdict %q from %q\n%s\nreplay: TPROBE_SEED=%d go test -count=1 -run '^TestProbeVerdictRule$' ./scripts/buildcost", seed, i, fmt.Sprintf(format, a...), v, from, tab, seed)
		}
		seen[v.Verdict]++
		if v.Verdict == "win" && len(v.Slower) > 0 {
			seen["win, slower"]++
		}
		// (1) Coverage decides whether a form has a verdict at all.
		covered, anyWin := 0, false
		for _, f := range probeErgoForms {
			if tab.Kills[f] == tab.N {
				covered++
				w, _, _ := refVerdict(tab, f)
				anyWin = anyWin || w == "win"
			}
		}
		if anyWin != (v.Verdict == "win") {
			fail("a covered form wins: %v, yet the kind reads %q", anyWin, v.Verdict)
		}
		if covered == 0 && v.Verdict != "no result" {
			fail("no Ergo form kills every mutant, yet a speed verdict")
		}
		if v.Verdict != "no result" && tab.Kills[from] != tab.N {
			fail("the verdict comes from a form that misses a kill")
		}
		for _, f := range probeErgoForms {
			if tab.Kills[f] == tab.N {
				continue
			}
			changed := map[string]probeSide{}
			for k, s := range ergo {
				changed[k] = s
			}
			other := tab
			other.Counts = map[string]map[string][]uint64{"go": tab.Counts["go"], "ergo-test": tab.Counts["ergo-test"], "ergo-check": tab.Counts["ergo-check"]}
			other.Counts[f] = map[string][]uint64{}
			for _, s := range probeScenarios {
				other.Counts[f][s] = drawCounts(r, 1e7, 0)
			}
			changed[f] = other.side(f)
			if v2, _ := probeKindVerdict(goSide, changed); !reflect.DeepEqual(v2, v) {
				fail("the counts of %s, which misses a kill, changed the verdict to %+v", f, v2)
			}
		}
		// (2) Each form's verdict, and the scenarios it lists, are the rule's.
		for _, f := range probeErgoForms {
			got := probeVerdict(goSide.Rows, ergo[f].Rows)
			want, faster, slower := refVerdict(tab, f)
			if got.Verdict != want || !slices.Equal(got.Faster, faster) || !slices.Equal(got.Slower, slower) {
				fail("%s: the meter reads %+v, the rule %s, faster %v, slower %v", f, got, want, faster, slower)
			}
		}
		// (3) The two sides exchange.
		for _, f := range probeErgoForms {
			e := ergo[f]
			for _, s := range probeScenarios {
				if a, b := probeCompare(e.Rows[s], goSide.Rows[s]), probeCompare(goSide.Rows[s], e.Rows[s]); a != -b {
					fail("%s %s: the comparison is %d one way and %d the other", f, s, a, b)
				}
			}
			there, back := probeVerdict(goSide.Rows, e.Rows), probeVerdict(e.Rows, goSide.Rows)
			pure := func(x probeResult) bool { return x.Verdict == "win" && len(x.Slower) == 0 }
			switch {
			case pure(there) && back.Verdict != "loss", there.Verdict == "loss" && !pure(back):
				fail("%s: %+v reads as %+v from the other side, not a loss and a win with no slower scenario", f, there, back)
			case (there.Verdict == "tie") != (back.Verdict == "tie"):
				fail("%s: %+v reads as %+v from the other side", f, there, back)
			case there.Verdict == "win" && len(there.Slower) > 0 && back.Verdict != "win":
				fail("%s: a win with a slower scenario, %+v, reads as %+v from the other side", f, there, back)
			}
		}
	}
	for _, v := range []string{"win", "win, slower", "tie", "loss", "no result"} {
		if seen[v] == 0 {
			t.Errorf("seed %d: no table of %d gave %q; the draw misses a verdict", seed, probeDraws, v)
		}
	}
}

// evidenceTemplate is an output a form prints, with what it shows: the
// class the rule gives it and whether it is an answer from a cache.
type evidenceTemplate struct {
	form, output string
	exit         int
	failed, of   int
	hung         bool
	class        string
	cached       bool
}

var evidenceTemplates = []evidenceTemplate{
	{form: "go", output: "ok  \tergo.local/prototype/internal/selfhostcheck\t21.4s\n", of: 1, class: "pass"},
	{form: "go", output: "ok  \tergo.local/prototype/internal/selfhostcheck\t(cached)\n", of: 1, class: "pass", cached: true},
	{form: "go", output: "ok  \tergo.local/prototype/internal/selfhostcheck\t0.2s [no tests to run]\n", of: 1, class: "broken"},
	{form: "go", output: "testing: warning: no tests to run\nPASS\nok  \tergo.local/prototype/cmd/ergo\t0.3s\n", of: 1, class: "broken"},
	{form: "go", output: "?   \tergo.local/prototype/x\t[no test files]\n", of: 1, class: "broken"},
	{form: "go", output: "--- FAIL: TestLexicalErrorInLookahead (0.01s)\n    lexerror_test.go:98: got 21\nFAIL\nFAIL\tergo.local/prototype/internal/sketch/syntax\t1.2s\nFAIL\n", exit: 1, of: 2, failed: 1, class: "fail"},
	{form: "go", output: "# ergo.local/prototype/internal/linked/compiler\ncompiler.go:9:2: undefined: x\nFAIL\tergo.local/prototype/internal/selfhostcheck [build failed]\nFAIL\n", exit: 1, of: 1, class: "broken"},
	{form: "go", output: "linked: compiler/format/nodes.ergo:3:1: error S100\n", exit: 1, of: 2, failed: 0, class: "broken"},
	{form: "go", output: "panic: test timed out after 10m0s\n\trunning tests:\n--- FAIL: TestJSONParseAgreesWithControl (600.0s)\nFAIL\tergo.local/prototype/cmd/ergo\t600.1s\n", exit: 1, of: 1, class: "broken"},
	{form: "control", output: "ok 19 cases\n", of: 2, failed: 1, class: "pass"},
	{form: "control", output: "FAIL match header, open interpolation: got 21, want 17\n", exit: 1, of: 2, failed: 1, class: "fail"},
	{form: "control", output: "control: no testdata\n", exit: 2, of: 2, failed: 1, class: "broken"},
	{form: "control", output: "main.go:3:1: syntax error\n", exit: 1, of: 2, failed: 0, class: "broken"},
	{form: "ergo-test", output: "ok A\nok B (100 cases)\nPASS (2 tests)\n", of: 1, class: "pass"},
	{form: "ergo-test", output: "ok A\nPASS (1 tests)\nergo: cached pass replayed; --fresh runs it again\n", of: 1, class: "pass", cached: true},
	{form: "ergo-test", output: "ok A\nskip B: a Go function (Open), even a pure one\nPASS (2 tests, 1 skipped)\n", of: 1, class: "broken"},
	{form: "ergo-test", output: "PASS (0 tests)\n", of: 1, class: "broken"},
	{form: "ergo-test", output: "ok A\nFAIL B (case 3 of 100, seed 1, size 0: n = 2): assert failed at b.ergo:4:2\nFAIL (1 of 2 tests)\n", exit: 1, of: 1, class: "fail"},
	{form: "ergo-test", output: "FAIL Divides: a defect, which stops the run\npanic: index out of range\n", exit: 2, of: 1, class: "fail"},
	{form: "ergo-test", output: "FAIL B (case 1 of 100, seed 1, size 0: seed = 10): assert failed at agree.ergo:155:2\nFAIL (1 of 1 tests)\n", exit: 1, of: 2, failed: 0, class: "fail"},
	{form: "ergo-test", output: "agree.ergo:3:9: error S100: undefined: json.Parsee\n", exit: 1, of: 2, failed: 0, class: "broken"},
	{form: "ergo-test", output: "probe.ergo:3:9: error S100: undefined: syntax.Parsee\n", exit: 1, of: 1, class: "broken"},
	{form: "ergo-test", output: "ok A\n", hung: true, exit: -1, of: 1, class: "broken"},
	{form: "ergo-check", output: `{"schemaVersion":11,"checked":true,"evaluation":{"status":"passed","obligations":[{"name":"A","status":"passed"}]}}`, of: 1, class: "pass"},
	{form: "ergo-check", output: `{"schemaVersion":11,"evaluation":{"status":"passed","obligations":[{"name":"law L","status":"passed","cached":true}]}}`, of: 1, class: "pass", cached: true},
	{form: "ergo-check", output: `{"schemaVersion":11,"evaluation":{"status":"skipped","obligations":[{"name":"A","status":"passed"},{"name":"B","status":"skipped"}]}}`, of: 1, class: "broken"},
	{form: "ergo-check", output: `{"schemaVersion":11,"evaluation":{"status":"incomplete","obligations":[{"name":"A","status":"incomplete"}]}}`, of: 1, class: "broken"},
	{form: "ergo-check", output: `{"schemaVersion":11,"evaluation":{"status":"none","obligations":[]}}`, of: 1, class: "broken"},
	{form: "ergo-check", output: `{"schemaVersion":11,"evaluation":{"status":"refuted","obligations":[{"name":"A","status":"refuted"}]}}`, exit: 1, of: 1, class: "fail"},
	{form: "ergo-check", output: `{"schemaVersion":11,"checked":false,"diagnostics":[{"code":"S100"}]}`, exit: 1, of: 1, class: "broken"},
}

func (e evidenceTemplate) run() probeRun {
	return probeRun{Form: e.form, Exit: e.exit, Failed: e.failed, Of: e.of, Output: e.output, Hung: e.hung}
}

// TestProbeEvidenceRule holds the meter's guards (TPROBE spec, Measure,
// "Guards" and "Equal coverage") on drawn rows of known outputs: a row
// counts only when the family ran and passed (no "no tests to run", no
// skip, no incomplete or empty evaluation, no build error, no hang), a
// cold row only with both caches new and no cached answer, an edit row
// only when the edited file's digest changed and, for a form that relinks,
// the linked Go's too; a mutant is killed only when the form passes clean
// and fails with a test verdict on the mutant at every seed.
//
// Mutants it alone catches: scripts/buildcost/testdata/mutants.json,
// killer scripts/buildcost:TestProbeEvidenceRule.
func TestProbeEvidenceRule(t *testing.T) {
	seed := probeSeed(t)
	r := rand.New(rand.NewPCG(seed, 0x6576))
	replay := fmt.Sprintf("replay: TPROBE_SEED=%d go test -count=1 -run '^TestProbeEvidenceRule$' ./scripts/buildcost", seed)
	digests := []string{"", "a1", "b2"}
	for i := range probeDraws {
		e := evidenceTemplates[r.IntN(len(evidenceTemplates))]
		row := probeRow{
			Scenario:   probeScenarios[r.IntN(len(probeScenarios))],
			Run:        e.run(),
			XDGNew:     r.IntN(4) != 0,
			GoCacheNew: r.IntN(4) != 0,
			EditBefore: digests[r.IntN(3)], EditNow: digests[1+r.IntN(2)],
			Relink:       (e.form == "go" || e.form == "control") && r.IntN(2) == 0,
			LinkedBefore: digests[r.IntN(3)], Linked: digests[1+r.IntN(2)],
		}
		want := e.class == "pass"
		switch row.Scenario {
		case "cold":
			want = want && row.XDGNew && row.GoCacheNew && !e.cached
		case "edit":
			want = want && row.EditBefore != "" && row.EditBefore != row.EditNow && (!row.Relink || (row.LinkedBefore != "" && row.LinkedBefore != row.Linked))
		}
		if why := probeRefuse(row); (why == "") != want {
			t.Fatalf("seed %d, row %d: the guard says %q, the rule says counts=%v\nrow: %+v\noutput: %q\n%s", seed, i, why, want, row, e.output, replay)
		}
		// The kill rule, at one to three seeds of one form.
		var clean, mutated []probeOutcome
		killed := true
		for range 1 + r.IntN(3) {
			c, m := pickTemplate(r, e.form), pickTemplate(r, e.form)
			clean, mutated = append(clean, probeClassify(c.run())), append(mutated, probeClassify(m.run()))
			killed = killed && c.class == "pass" && m.class == "fail"
		}
		if got := probeKilled(clean, mutated); got != killed {
			t.Fatalf("seed %d, row %d: %s: the kill rule says %v, the meter %v\nclean %v\nmutated %v\n%s", seed, i, e.form, killed, got, clean, mutated, replay)
		}
	}
}

// pickTemplate draws a template of form, a pass or a fail half the time
// so that kills are drawn as often as misses.
func pickTemplate(r *rand.Rand, form string) evidenceTemplate {
	var of []evidenceTemplate
	for _, e := range evidenceTemplates {
		if e.form == form {
			of = append(of, e)
		}
	}
	if r.IntN(2) == 0 {
		want := []string{"pass", "fail"}[r.IntN(2)]
		for _, k := range r.Perm(len(of)) {
			if of[k].class == want {
				return of[k]
			}
		}
	}
	return of[r.IntN(len(of))]
}
