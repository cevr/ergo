package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/runtime/explore/sched"
)

// The search modes of `ergo test --explore MODE` (runtime.md, Explored
// schedules; stdlib.md, Testing): the test binary is built once with the
// ergoexplore tag, and its schedules run in worker processes of it
// (runtime/explore/sched, wire.go).
var exploreModes = []string{"dfs", "pct", "random"}

// exploreOptions are --explore's mode and its bounds.
type exploreOptions struct {
	mode      string // "", dfs, pct, random, or replay (--schedule)
	preempt   int    // dfs: the most preemptions a schedule has
	schedules int    // the most schedules a search runs
	depth     int    // pct: the bug depth
	schedule  string // replay: the schedule's token
}

// exploreDefaults are the bounds when no flag gives one: dfs takes at most
// 2 preemptions (Loom suggests 2 or 3) and 10,000 schedules; pct and
// random take 100 schedules, pct for depth 3. A schedules of 0 is the
// mode's default (budget).
func exploreDefaults(mode string) exploreOptions {
	return exploreOptions{mode: mode, preempt: 2, depth: 3}
}

// budget is the most schedules o's search runs.
func (o exploreOptions) budget() int {
	switch {
	case o.schedules > 0:
		return o.schedules
	case o.mode == "dfs":
		return 10000
	}
	return 100
}

// parseExploreFlag reads one of the search's flags at args[i] into o, and
// reports how many words it took (0: not one of them).
func parseExploreFlag(o *exploreOptions, args []string, i int) (int, error) {
	name, value, inline := strings.Cut(args[i], "=")
	switch name {
	case "--preempt", "--schedules", "--depth", "--schedule":
	default:
		return 0, nil
	}
	took := 1
	if !inline {
		if i+1 == len(args) {
			return 0, fmt.Errorf("%s requires a value", name)
		}
		value, took = args[i+1], 2
	}
	if name == "--schedule" {
		if _, err := sched.ParseToken(value); err != nil {
			return 0, err
		}
		o.schedule, o.mode = value, "replay"
		return took, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || (n == 0 && name != "--preempt") {
		return 0, fmt.Errorf("%s takes a number above 0 (0 too for --preempt), not %q", name, value)
	}
	switch name {
	case "--preempt":
		o.preempt = n
	case "--schedules":
		o.schedules = n
	default:
		o.depth = n
	}
	return took, nil
}

// exploreSearch runs the search of opts over the explored test binary and
// writes its report on w: the output of the first schedule, then one
// verdict line; on a failure, the schedule in words, the failing test as
// an Ergo test that replays it, and the command that replays it.
func exploreSearch(w io.Writer, l loaded, binary string, childArgs []string, opts exploreOptions, seed uint64, limit uint64) error {
	dir := filepath.Dir(binary)
	out, err := os.OpenFile(filepath.Join(dir, "explore.out"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	pool := &sched.Pool{Start: func() (sched.Conn, error) { return startWorker(binary, childArgs, out, limit) }}
	x := &outputExec{pool: pool, out: out}
	defer pool.Close()
	var rep sched.Report
	var verdict string
	switch opts.mode {
	case "replay":
		schedule, _ := sched.ParseToken(opts.schedule)
		r := sched.Once(x, sched.Replay{Schedule: schedule})
		rep = sched.Report{Schedules: 1, Points: len(r.Picks), Depth: len(r.Picks)}
		if r.Failed() {
			rep.Failure = &r
		}
		fmt.Fprint(w, r.Outcome.Value)
		verdict = "the schedule replays with no failure"
	case "dfs":
		first := true
		rep = sched.DFS(x, sched.Options{Preempt: opts.preempt, Schedules: opts.budget(), Visit: func(r sched.Run) {
			if first {
				fmt.Fprint(w, r.Outcome.Value)
				first = false
			}
		}})
		switch {
		case rep.Proved:
			verdict = fmt.Sprintf("proved for at most %d preemptions", opts.preempt)
		default:
			verdict = fmt.Sprintf("no failure in %d schedules; not proved", rep.Schedules)
		}
	default:
		first := sched.Once(x, sched.Cooperative{})
		fmt.Fprint(w, first.Outcome.Value)
		if first.Failed() {
			rep = sched.Report{Schedules: 1, Points: len(first.Picks), Depth: len(first.Picks), Failure: &first}
			break
		}
		tasks := map[string]bool{}
		for _, c := range first.Choices {
			for _, s := range c.Enabled {
				tasks[s.Task] = true
			}
		}
		n, k := len(tasks), len(first.Picks)
		rep = sched.Sample(x, opts.budget(), func(i int) sched.Picker {
			if opts.mode == "pct" {
				return sched.NewPCT(mix(seed, i), n, k, opts.depth)
			}
			return sched.NewRandom(mix(seed, i))
		})
		verdict = fmt.Sprintf("no failure in %d schedules (%s", rep.Schedules, opts.mode)
		if opts.mode == "pct" {
			verdict += fmt.Sprintf(", depth %d", opts.depth)
		}
		verdict += ")"
	}
	counters := fmt.Sprintf("%d schedules, %d points, depth %d, %d sleep-set blocked, %d workers", rep.Schedules, rep.Points, rep.Depth, rep.SleepBlocked, pool.Started)
	if rep.Failure == nil {
		fmt.Fprintf(w, "ok explore %s: %s (%s)\n", opts.mode, verdict, counters)
		return nil
	}
	exploreFailure(w, l, *rep.Failure, opts.mode, counters)
	return exitStatusError{code: 1, handled: true}
}

// mix is the seed of the i-th schedule of a run seeded with seed.
func mix(seed uint64, i int) uint64 { return seed*0x9e3779b97f4a7c15 + uint64(i) + 1 }

// outputExec runs a schedule in the pool and reads what the run wrote to
// standard output into its outcome's value.
type outputExec struct {
	pool *sched.Pool
	out  *os.File
}

func (x *outputExec) Run(p sched.Picker) sched.Outcome {
	_ = x.out.Truncate(0)
	o := x.pool.Run(p)
	if data, err := os.ReadFile(x.out.Name()); err == nil {
		o.Value = string(data)
	}
	return o
}

// exploreFailure writes a failing run: its verdict, its steps in words,
// the failing test as an Ergo test that replays it when one can be found,
// and the command that replays it.
func exploreFailure(w io.Writer, l loaded, r sched.Run, mode, counters string) {
	why := r.Outcome.Failure
	if r.Diverged != "" {
		why = "the test is not deterministic under the scheduler: " + r.Diverged
	} else if r.Outcome.Stopped != "" && why == "" {
		why = r.Outcome.Stopped
	}
	fmt.Fprintf(w, "FAIL explore %s: %s (%s)\n", mode, why, counters)
	for _, line := range sched.Words(r) {
		fmt.Fprintln(w, "\t"+line)
	}
	if mode != "replay" && r.Outcome.Value != "" {
		fmt.Fprintln(w, "\tthe run wrote:")
		for _, line := range strings.Split(strings.TrimSuffix(r.Outcome.Value, "\n"), "\n") {
			fmt.Fprintln(w, "\t\t"+line)
		}
	}
	if text, ok := replayTest(l, r); ok {
		fmt.Fprintln(w)
		fmt.Fprint(w, text)
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "replay: ergo test %s --schedule %s\n", l.source.String(), sched.Token(r.Schedule()))
}

// replayTest is the failing test of r as an Ergo test that starts with
// explore.Replay and its window of the schedule. The failing test is the
// one whose verdict line is the run's first FAIL line, or the one that was
// running when the run stopped; its window is the picks after the verdict
// line before it. A law, a property or a test of a package folder has no
// such text.
func replayTest(l loaded, r sched.Run) (string, bool) {
	text := l.snapshot.Text
	if text == "" {
		return "", false
	}
	file, err := syntax.Parse(text)
	if err != nil {
		return "", false
	}
	var tests []*syntax.TestDecl
	for _, d := range file.Decls {
		if t, ok := d.(*syntax.TestDecl); ok {
			tests = append(tests, t)
		}
	}
	out := r.Outcome.Value
	start, end, index := int64(0), int64(len(out)), 0
	failing := ""
	for at := 0; at < len(out); {
		line, _, _ := strings.Cut(out[at:], "\n")
		next := at + len(line) + 1
		verdict, law := verdictLine(line)
		if verdict != "" && !law {
			if strings.HasPrefix(line, "FAIL ") {
				failing, end = verdict, int64(at)
				break
			}
			index++
			start = int64(min(next, len(out)))
		} else if verdict != "" {
			start = int64(min(next, len(out)))
		}
		at = next
	}
	var decl *syntax.TestDecl
	if failing != "" {
		for _, t := range tests {
			if t.Name.Name == failing {
				decl = t
			}
		}
	} else if index < len(tests) {
		decl = tests[index]
	}
	if decl == nil || (decl.Params != nil && len(decl.Params.List) > 0) {
		return "", false
	}
	var seen, window []string
	for i, c := range r.Choices {
		switch {
		case c.Out < start:
			for _, s := range c.Enabled {
				seen = append(seen, s.Task)
			}
		case c.Out <= end:
			window = append(window, c.Enabled[r.Picks[i]].Task)
		}
	}
	off := sched.Offset(seen, "0")
	var rel []string
	for _, p := range window {
		q, ok := sched.Relative(p, "0", off)
		if !ok {
			return "", false
		}
		rel = append(rel, strconv.Quote(q))
	}
	body := strings.Trim(text[decl.Body.Lbrace+1:decl.Body.Rbrace], "\n")
	var b strings.Builder
	b.WriteString("// The schedule of the failure; the file needs `import explore`.\n")
	fmt.Fprintf(&b, "test %sSchedule {\n", decl.Name.Name)
	fmt.Fprintf(&b, "\texplore.Replay([]string{%s})\n", strings.Join(rel, ", "))
	b.WriteString(body + "\n}\n")
	return b.String(), true
}

// verdictLine reads a line of the test binary's report: the test's or the
// law's name when it is a verdict line ("ok NAME", "FAIL NAME…", "skip
// …"), and whether it is a law's.
func verdictLine(line string) (string, bool) {
	for _, p := range []string{"ok ", "FAIL ", "skip "} {
		rest, ok := strings.CutPrefix(line, p)
		if !ok {
			continue
		}
		if strings.HasPrefix(rest, "law ") {
			return rest, true
		}
		if strings.HasPrefix(rest, "(") {
			return "", false // the summary: FAIL (K of N tests)
		}
		name, _, _ := strings.Cut(rest, " ")
		name, _, _ = strings.Cut(name, ":")
		return name, false
	}
	return "", false
}
