package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// evalScriptStop is the evaluator's one admitted stop in
// TestIRInstValuesMatchSource, word for word.
const evalScriptStop = "check-time evaluation does not run a literal of testing.Script[main.Store] yet"

// TestIRInstValuesMatchSource is SMALL-REASONS' seeded property
// (GenerateInstValue): a generic function named at its type arguments is
// a value of its instance on the IR, bound, passed, stored and named at a
// generic function's own type parameters, and a testing.Script's field is
// its operation's. The source path, the IR path and the evaluator print
// the same, and each of instValueFuncs has an IR body, so the property
// never passes on the source form alone. A failing seed prints the Ergo
// source, the outputs and its replay command. Mutants:
// sr-inst-value-refused (an instance as a value refused again),
// sr-script-field-refused (a Script's field refused again).
//
// The evaluator leg admits one stop, evalScriptStop, until MX's evaluator
// item (the evaluator runs a testing.Script literal) lands; then the
// exception and evalScriptStop are deleted.
//
// It builds Go because the other sides of the agreement are the built
// programs, and the patched compile checks the instance's call.
func TestIRInstValuesMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateInstValue(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRInstValuesMatchSource$' ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		ev, err := eval.RunMain(u.Package, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatalf("seed %d: the evaluator: %v\n%s\n%s", seed, err, replay, text)
		}
		// The evaluator does not run a testing.Script literal yet (MX's
		// evaluator item: delete this exception when it lands): it may stop
		// with that message only, at scripted, main's last lines, with
		// every instance value's line printed as the built program prints
		// it. The two built paths still compare every line, scripted's too.
		scriptStop := ev.Skipped == evalScriptStop && strings.HasPrefix(a.stdout, ev.Stdout) &&
			strings.Count(ev.Stdout, "\n") == 4*len(instValueLines)
		if !scriptStop && (ev.Skipped != "" || ev.Stdout != a.stdout || ev.Status != a.exit) || ev.Incomplete != "" {
			t.Fatalf("seed %d: the evaluator and the built program differ\nbuilt: %+v\neval:  %q (status %d, skipped %q, incomplete %q)\n%s\n%s", seed, a, ev.Stdout, ev.Status, ev.Skipped, ev.Incomplete, replay, text)
		}
		if scriptStop && !strings.Contains(a.stdout, "scripted(-3) ") {
			t.Fatalf("seed %d: the built programs printed no scripted line\n%s\n%s", seed, replay, text)
		}
		has := map[string]bool{}
		for _, f := range rep.IR {
			has[f] = true
		}
		for _, f := range instValueFuncs {
			if !has[f] {
				t.Fatalf("seed %d: %s has no IR body (IR: %v; fallback reasons: %v)\n%s\n%s", seed, f, rep.IR, rep.Fallback, replay, text)
			}
		}
	}
}
