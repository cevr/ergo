package fix

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

// raceBody is a race of a block that works against a block that sleeps
// and gives a fallback, at indent (the race's line), with the sleeping
// block first or second and its fallback a tail or a return; inner, when
// set, is a race the working block runs.
func raceBody(r *rand.Rand, indent, inner string) string {
	work := "work()"
	if inner != "" {
		work = inner
	}
	body := indent + "\tfunc() int needs Scheduler {\n" + indent + "\t\t" + work + "\n" + indent + "\t},\n"
	tail := "0"
	if r.IntN(2) == 0 {
		tail = "return -1"
	}
	sleep := indent + "\tfunc() int needs Scheduler {\n" + indent + "\t\tScheduler.Sleep(2s)\n" + indent + "\t\t" + tail + "\n" + indent + "\t},\n"
	blocks := body + sleep
	if r.IntN(2) == 0 {
		blocks = sleep + body
	}
	return "tasks.Race([]func() int needs Scheduler{\n" + blocks + indent + "})"
}

// TestFixLintProperty generates programs with races that race-for-timeout
// fixes (the sleep first or second, a tail or a return fallback, a race
// nested in another's working block, so a later round fixes it), with
// timeout imported or not, with tasks still used by a race the rule does
// not report (two working blocks) or reports without a fix (one that
// already fails timeout.Expired) or used by nothing after the fix, and
// with a local named timeout, whose fixed text does not check, so the run
// is refused. Otherwise, after ergo fix --lint: the program checks; each
// fixable race was fixed once; lint finds no fixable finding and no
// finding of another rule; a second fix changes nothing; and the
// formatter keeps the text (EX1's properties, with --lint).
func TestFixLintProperty(t *testing.T) {
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const key = "/p/main.ergo"
	seeds := []uint64{}
	if *fixSeed != 0 {
		seeds = append(seeds, uint64(*fixSeed))
	} else {
		for s := uint64(1); s <= 40; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0xf1))
		var b strings.Builder
		imported := r.IntN(2) == 0
		b.WriteString("import tasks\n")
		if imported {
			b.WriteString("import timeout\n")
		}
		b.WriteString("\nfunc work() int needs Scheduler {\n\tScheduler.Sleep(5s)\n\t7\n}\n")
		if imported {
			b.WriteString("\nfunc bounded() int needs Scheduler {\n\ttimeout.After(1s, func() int needs Scheduler { work() }) catch {\n\tcase timeout.Expired:\n\t\treturn 0\n\t}\n}\n")
			b.WriteString("\nfunc late() int fails timeout.Expired {\n\ttimeout.Expired\n}\n")
		}
		want, refused := 0, false
		for i := 0; i < 1+r.IntN(3); i++ {
			fmt.Fprintf(&b, "\nfunc r%d() int needs Scheduler {\n\t", i)
			kind := r.IntN(4)
			switch {
			case !imported && r.IntN(8) == 0:
				kind = 4
			case imported && r.IntN(4) == 0:
				kind = 5
			}
			switch kind {
			case 5:
				// a race that already fails timeout.Expired: reported with
				// no fix, so the run leaves it
				b.WriteString("tasks.Race([]func() int fails timeout.Expired needs Scheduler{\n\t\tfunc() int fails timeout.Expired needs Scheduler {\n\t\t\ttry late()\n\t\t},\n\t\tfunc() int fails timeout.Expired needs Scheduler {\n\t\t\tScheduler.Sleep(2s)\n\t\t\t0\n\t\t},\n\t}) catch {\n\tcase timeout.Expired:\n\t\treturn 0\n\t}")
			case 4:
				// a local named timeout: the fixed text does not check,
				// so the run is refused
				b.WriteString("let timeout = 3\n\tlet v = " + raceBody(r, "\t", "") + "\n\tv + timeout")
				refused = true
			case 0, 1:
				b.WriteString(raceBody(r, "\t", ""))
				want++
			case 2:
				b.WriteString(raceBody(r, "\t", raceBody(r, "\t\t\t", "")))
				want += 2
			case 3:
				// a race of two working blocks: no finding; tasks stays used
				b.WriteString("tasks.Race([]func() int needs Scheduler{\n\t\tfunc() int needs Scheduler {\n\t\t\twork()\n\t\t},\n\t\tfunc() int needs Scheduler {\n\t\t\twork() + 1\n\t\t},\n\t})")
			}
			b.WriteString("\n}\n")
		}
		b.WriteString("\nfunc main() {\n}\n")
		src := b.String()
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nsource:\n%s\nreplay: go test ./internal/sketch/fix -run TestFixLintProperty -fix.seed=%d", seed, fmt.Sprintf(format, args...), src, seed)
		}
		if out, err := format.Source(src); err != nil || out != src {
			fail("the generator's source is not formatted (%v):\n%s", err, out)
		}
		chk, lintr := Source(key), LintSource(key, conf)
		res, err := RunLint(map[string]string{key: src}, chk, lintr)
		if refused {
			if err == nil {
				fail("fix --lint applied a fix whose program does not check:\n%s", res.Texts[key])
			}
			continue
		}
		if err != nil {
			fail("fix --lint: %v", err)
		}
		fixed := res.Texts[key]
		if len(res.Remaining) > 0 || len(res.Linted) != want {
			fail("fix --lint fixed %d races, want %d; remaining %v\nfixed:\n%s", len(res.Linted), want, res.Remaining, fixed)
		}
		// 1. the fixed program checks.
		if out, err := chk(res.Texts, check.Options{Explicit: true}); err != nil || len(out.Diagnostics) > 0 {
			fail("the fixed program does not check (%v): %v\n%s", err, out.Diagnostics, fixed)
		}
		// 2. fix then lint: no fixable finding is left, and the fix adds no
		// finding of another rule (its catch arm says why it is silent).
		if left, err := lintr(res.Texts); err != nil || len(left) > 0 {
			fail("lint after fix --lint still has fixable findings (%v): %v\n%s", err, left, fixed)
		}
		for _, f := range conf.Run(lint.InputOf(sketchfront.Check(fixed))).Findings {
			if f.Rule != "race-for-timeout" {
				fail("lint after fix --lint reports %s at line %d: %s\n%s", f.Rule, f.Line, f.Message, fixed)
			}
		}
		// 3. fix twice is fix once.
		again, err := RunLint(res.Texts, chk, lintr)
		if err != nil || again.Texts[key] != fixed || len(again.Linted) > 0 {
			fail("a second fix --lint changed the program (%v):\n%s\nthen:\n%s", err, fixed, again.Texts[key])
		}
		// 4. the formatter keeps the fixed text.
		if out, err := format.Source(fixed); err != nil || out != fixed {
			fail("fmt(fix(x)) != fix(x) (%v):\n%s\nformatted:\n%s", err, fixed, out)
		}
	}
}
