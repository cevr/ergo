package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/scripts/mutants/anchor"
)

// A package's mutants are judged in bounded work (GT-BOUND: the runner ran
// out of its 90 minutes on internal/sketch/check in the coverage pass, which
// measured every test of five suites before it judged one row).
//
// Phase A needs no coverage. Each test a selected row names (its killers,
// its anchor's tests) runs once on the unmutated tree, its baseline: one
// that fails there leaves its rows unjudged, and its time is the base of its
// deadline. Then each row runs its named tests on its mutated copy. A row
// they kill is judged.
//
// Phase B measures coverage only for the rows still open (a row with no
// named test, or whose named tests did not kill it; with -all every row),
// over the run's go test suites (each suite's segment comes from the
// coverage store where its key holds, GT6), and runs the tests that cover
// each row that it has not run yet, known killers first, then cheapest
// first.
//
// In both phases a row's tests run Config.Tests at a time, and the first
// kill stops the rest (Result.Stopped). A test whose only failure is its
// timeout (go test's "panic: test timed out", or the runner's deadline)
// kills nothing: a deadline only stops a hung test. A row that only such
// failures met is hung, and a hung row fails the run, as an invalid one
// does. The receipt is the tests each row ran, never a wall-clock bound.

// item is one selected row.
type item struct {
	at     int
	ledger string
	m      *Mutant
}

// selected lists the rows of jobs that their Select picks.
func selected(jobs []Job) []item {
	var items []item
	for _, j := range jobs {
		for _, m := range j.Ledger.Mutants {
			if j.Select == nil || j.Select.MatchString(m.Name) {
				items = append(items, item{len(items), j.Path, m})
			}
		}
	}
	return items
}

// judgeAll judges the selected rows of jobs: those the journal holds are
// taken from it, the rest in phase A and, where still open, phase B.
func (e *engine) judgeAll(ctx context.Context, jobs []Job) ([]Result, error) {
	items := selected(jobs)
	results := make([]Result, len(items))
	jr, err := e.openJournal()
	if err != nil {
		return nil, err
	}
	e.base = jr.baselines
	var pending []int
	for i, it := range items {
		if r, ok := jr.results[resultKey(it.ledger, it.m.Name)]; ok {
			results[i] = r
			continue
		}
		pending = append(pending, i)
	}
	if n := len(items) - len(pending); n > 0 {
		e.logf("resume: %d of %d rows were judged by an earlier run of tree %s (%s): not judged again", n, len(items), e.key, jr.path)
	}
	var mu sync.Mutex
	var journalErr error
	finish := func(i int, r Result) {
		r.Ledger = items[i].ledger
		if ctx.Err() != nil {
			// A verdict that lands as the run stops may rest on a child the
			// stop cut (a build "failed" by the interrupt): the next run
			// judges the row again.
			r.Status, r.Detail = "", "interrupted"
		}
		mu.Lock()
		defer mu.Unlock()
		results[i] = r
		e.logf("%s: %s %s %s (%.0f s, %d run, %d stopped, %d covering)", r.Name, r.Status, r.Killer, r.Reason, r.Seconds, len(r.Ran), len(r.Stopped), r.Covering)
		if r.Status == "" {
			return // interrupted: judged again by the next run
		}
		if err := jr.add(journalEntry{Result: &r}); err != nil && journalErr == nil {
			journalErr = err
		}
		if e.cfg.Progress != nil {
			e.cfg.Progress(slices.Clone(results))
		}
	}
	run := func() error {
		if e.compileOnly {
			return e.each(ctx, pending, func(w *worker, i int) { finish(i, w.compileRow(ctx, items[i].m)) })
		}
		if err := e.baselines(ctx, items, pending, jr); err != nil {
			return err
		}
		open := map[int]*Result{}
		err := e.each(ctx, pending, func(w *worker, i int) {
			r, more := w.judgeNamed(ctx, items[i].m)
			if !more {
				finish(i, r)
				return
			}
			mu.Lock()
			open[i] = &r
			mu.Unlock()
		})
		if err != nil || ctx.Err() != nil {
			return err
		}
		var rest []int
		for _, i := range pending {
			if open[i] != nil {
				rest = append(rest, i)
			}
		}
		if len(rest) == 0 {
			e.logf("coverage: not measured: the named tests judged every row (%d)", len(pending))
			return nil
		}
		e.logf("coverage: %d of %d rows are still open after their named tests", len(rest), len(pending))
		if err := e.prepareCoverage(ctx); err != nil {
			return err
		}
		return e.each(ctx, rest, func(w *worker, i int) { finish(i, w.judgeCovered(ctx, items[i].m, *open[i])) })
	}
	err = run()
	// Anything a child left that its own end did not stop (one whose
	// environment lost the token) ends with the run.
	reapAfter("")
	for _, w := range e.workers {
		err = errors.Join(err, w.err, e.verifyCopy(w.root, w.touched))
	}
	err = errors.Join(err, journalErr, ctx.Err())
	complete := err == nil && !slices.ContainsFunc(results, func(r Result) bool { return r.Status == "" })
	return results, errors.Join(err, jr.close(complete))
}

// workerPool makes the Workers copies of the checkout, once.
func (e *engine) workerPool() ([]*worker, error) {
	if e.workers != nil {
		return e.workers, nil
	}
	for w := 0; w < e.cfg.Workers; w++ {
		dir := filepath.Join(e.cfg.Work, "w"+strconv.Itoa(w))
		wk := &worker{e: e, root: filepath.Join(dir, "src"), bin: filepath.Join(dir, "bin"), touched: map[string]bool{}}
		if err := e.copyTree(wk.root); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(wk.bin, 0o755); err != nil {
			return nil, err
		}
		e.workers = append(e.workers, wk)
	}
	return e.workers, nil
}

// each runs fn on the rows at, one row per worker at a time.
func (e *engine) each(ctx context.Context, at []int, fn func(w *worker, i int)) error {
	if len(at) == 0 {
		return nil
	}
	workers, err := e.workerPool()
	if err != nil {
		return err
	}
	next := make(chan int)
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(w, i)
			}
		}()
	}
	for _, i := range at {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	return nil
}

// baselines runs, on the unmutated tree, each test that a pending row
// names and the journal does not hold yet, in worker 0's copy before any
// mutant touches it, with the runner's build and coverage deadline.
func (e *engine) baselines(ctx context.Context, items []item, pending []int, jr *journal) error {
	var ids []string
	for _, i := range pending {
		for _, id := range killerIDs(items[i].m) {
			if suite, _ := splitID(id); e.suite(suite) != nil && e.base[id] == nil && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	workers, err := e.workerPool()
	if err != nil {
		return err
	}
	w := workers[0]
	built := map[string]bool{}
	for _, id := range ids {
		suite, _ := splitID(id)
		if detail := w.buildWith(ctx, "baseline", e.suite(suite), built, e.cfg.CoverDeadline); detail != "" {
			return fmt.Errorf("the unmutated tree does not build what %s needs: %s", id, detail)
		}
	}
	slots := make(chan struct{}, max(1, e.cfg.Workers*e.cfg.Tests))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var addErr error
	ended := 0
	e.logf("baseline: %d named tests to run on the unmutated tree", len(ids))
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			suite, name := splitID(id)
			s := e.suite(suite)
			argv, cwd, env := e.argv(s, w.root, w.bin, name, e.cfg.CoverDeadline)
			o := e.execStarted(ctx, cwd, argv, e.env(env...), e.cfg.CoverDeadline, startLine(s, name))
			if o.reason == "interrupted" {
				return
			}
			o = neverRan(o)
			t := &covTest{ID: id, Seconds: o.seconds}
			if o.reason != "" {
				t.Failed, t.Detail = true, baseDetail(id, o)
				e.logf("baseline: %s fails on the unmutated tree (%s): its rows are unjudged\n%s", id, o.reason, lastLines(o.tail, 8))
			}
			mu.Lock()
			e.base[id] = t
			ended++
			e.logf("baseline: %d/%d %s %.1fs", ended, len(ids), id, t.Seconds)
			mu.Unlock()
			if err := jr.add(journalEntry{Baseline: t}); err != nil {
				mu.Lock()
				addErr = errors.Join(addErr, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	e.logf("baseline receipt: %d named tests run on the unmutated tree", len(ids))
	return errors.Join(addErr, ctx.Err())
}

// info is what the runner knows of a test on the unmutated tree: its
// coverage pass run, or its baseline.
func (e *engine) info(id string) *covTest {
	if e.cov != nil {
		if t := e.cov.Lookup(id); t != nil {
			return t
		}
	}
	return e.base[id]
}

// order puts the known killers first, in the ledger's order, then the
// other tests cheapest first.
func (e *engine) order(covering map[string]bool, known []string) []string {
	var out []string
	for _, id := range known {
		if covering[id] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	var rest []string
	for id := range covering {
		if !slices.Contains(out, id) {
			rest = append(rest, id)
		}
	}
	slices.SortFunc(rest, func(a, b string) int {
		if sa, sb := e.info(a).Seconds, e.info(b).Seconds; sa != sb {
			if sa < sb {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return append(out, rest...)
}

// known is the coverage the run used, with the baselines of the tests it
// does not hold: the report lists every test that fails on the unmutated
// tree, and -write keeps the killers that still exist.
func (e *engine) known() *coverage {
	c := &coverage{Key: e.key, Files: map[string][]covBlock{}, Builds: map[string]float64{}}
	if e.cov != nil {
		*c = *e.cov
		c.Tests = slices.Clone(e.cov.Tests)
	}
	var ids []string
	for id := range e.base {
		if !c.Indexed() || c.Lookup(id) == nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		c.Tests = append(c.Tests, e.base[id])
	}
	c.Reindex()
	return c
}

type worker struct {
	e       *engine
	root    string
	bin     string
	touched map[string]bool
	err     error
	// unmutated holds each source file's state before any mutant (they
	// are restored after each one), and sourceTools the source checks'
	// tools built from the unmutated copy.
	unmutated   map[string]sourceState
	sourceTools map[string]bool
}

// target is where a row's edit lands and the lines that stand for it.
type target struct {
	path     string
	original []byte
	file     string
	lo, hi   int
	header   bool
}

// locate finds the row's edit in the worker's copy; a row whose edit no
// longer applies is stale.
func (w *worker) locate(m *Mutant, r *Result) (*target, bool) {
	t := &target{path: filepath.Join(w.root, filepath.FromSlash(m.File)), file: m.File}
	var err error
	if t.original, err = os.ReadFile(t.path); err != nil {
		r.Status, r.Detail = Stale, err.Error()
		return nil, false
	}
	original := string(t.original)
	at, err := anchor.Locate(m.File, t.original, m.Decl, m.From)
	if err != nil {
		r.Status, r.Detail = Stale, err.Error()
		return nil, false
	}
	r.Line = 1 + strings.Count(original[:at], "\n")
	t.lo, t.hi = r.Line, r.Line+strings.Count(m.From, "\n")
	t.header = headerLine(original, at) && m.Anchor == nil
	if a := m.Anchor; a != nil && a.Text != "" {
		if a.File != "" {
			t.file = a.File
		}
		text := t.original
		if t.file != m.File {
			if text, err = os.ReadFile(filepath.Join(w.root, filepath.FromSlash(t.file))); err != nil {
				r.Status, r.Detail = Stale, "anchor: "+err.Error()
				return nil, false
			}
		}
		if n := strings.Count(string(text), a.Text); n != 1 {
			r.Status, r.Detail = Stale, fmt.Sprintf("its anchor text occurs %d times in %s", n, t.file)
			return nil, false
		}
		at := strings.Index(string(text), a.Text)
		t.lo = 1 + strings.Count(string(text[:at]), "\n")
		t.hi = t.lo + strings.Count(a.Text, "\n")
	}
	return t, true
}

// compileRow is -compiles' verdict: the row applied and compiled.
func (w *worker) compileRow(ctx context.Context, m *Mutant) (r Result) {
	r.Name = m.Name
	t, ok := w.locate(m, &r)
	if !ok {
		return r
	}
	restore, detail := w.applyChecked(ctx, m, t.path, t.original)
	if restore != nil {
		defer restore()
	}
	r.Status = Compiles
	if detail != "" {
		r.Status, r.Detail = Invalid, detail
	}
	return r
}

// judgeNamed is phase A for one row: it runs the tests the row names on
// its mutated copy. more is true when the row is still open.
func (w *worker) judgeNamed(ctx context.Context, m *Mutant) (r Result, more bool) {
	start := time.Now()
	r.Name = m.Name
	defer func() { r.Seconds += time.Since(start).Seconds() }()
	t, ok := w.locate(m, &r)
	if !ok {
		return r, false
	}
	var named []string
	for _, id := range killerIDs(m) {
		if w.e.info(id) != nil {
			named = append(named, id)
		}
	}
	if len(named) == 0 {
		return r, true
	}
	r.Covering = len(named)
	// A test that would judge the mutant and fails on the unmutated tree
	// kills nothing, so a verdict would rest on a red baseline (FT11b).
	if red := w.e.red(named); len(red) > 0 {
		r.Status, r.Detail = Unjudged, w.e.redDetail(red)
		return r, false
	}
	// A test that fails because the mutated file does not compile kills
	// nothing the ledger can trust (compiles.go, GT-COMPILE; FK7a, FK12a).
	restore, detail := w.applyChecked(ctx, m, t.path, t.original)
	if restore != nil {
		defer restore()
	}
	if detail != "" {
		r.Status, r.Detail = Invalid, detail
		return r, false
	}
	r.checked = true
	if w.runTests(ctx, m, named, &r) {
		return r, false
	}
	if r.Killer != "" && !w.e.cfg.All {
		r.Status = Killed
		return r, false
	}
	return r, true
}

// red are the tests of ids that fail on the unmutated tree.
func (e *engine) red(ids []string) []string {
	var out []string
	for _, id := range ids {
		if t := e.info(id); t != nil && t.Failed {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// judgeCovered is phase B for a row still open after phase A (its state
// in r): it runs the tests that cover the row and have not run on it.
func (w *worker) judgeCovered(ctx context.Context, m *Mutant, r Result) Result {
	start := time.Now()
	defer func() { r.Seconds += time.Since(start).Seconds() }()
	r.Name = m.Name
	t, ok := w.locate(m, &r)
	if !ok {
		return r
	}
	named := killerIDs(m)
	// A .ergo file's lines have blocks too (the cover marks of std bodies,
	// ergolines.go), and every file a test opens covers it (reads.go).
	covering := w.e.cov.Covering(t.file, t.lo, t.hi, t.header, named)
	for _, id := range named {
		if w.e.info(id) != nil {
			covering[id] = true
		}
	}
	r.Covering = len(covering)
	if len(covering) == 0 {
		r.Status = Uncovered
		return r
	}
	done := map[string]bool{}
	for _, id := range slices.Concat(r.Ran, r.Stopped) {
		done[id] = true
	}
	var ids []string
	for _, id := range w.e.order(covering, named) {
		if !done[id] {
			ids = append(ids, id)
		}
	}
	if red := w.e.red(ids); len(red) > 0 {
		if r.Killer == "" {
			r.Status, r.Detail = Unjudged, w.e.redDetail(red)
			return r
		}
		// -all after a kill: a red test only names no further killer.
		ids = slices.DeleteFunc(ids, func(id string) bool { return slices.Contains(red, id) })
	}
	apply := w.applyChecked
	if r.checked {
		apply = func(ctx context.Context, m *Mutant, path string, original []byte) (func(), string) {
			restore, detail := w.apply(m, path, original)
			if restore == nil {
				return nil, detail
			}
			return w.regenerated(ctx, m, restore)
		}
	}
	restore, detail := apply(ctx, m, t.path, t.original)
	if restore != nil {
		defer restore()
	}
	if detail != "" {
		r.Status, r.Detail, r.Killer, r.Killers = Invalid, detail, "", nil
		return r
	}
	if w.runTests(ctx, m, ids, &r) {
		return r
	}
	r.Status = verdict(r)
	return r
}

// verdict is the status of a row whose tests all ran.
func verdict(r Result) string {
	switch {
	case r.Killer != "":
		return Killed
	case len(r.NotStarted) > 0:
		return Invalid
	case len(r.Hung) > 0:
		return Hung
	}
	return Survived
}

// runTests builds what the tests ids need in the mutated copy, then runs
// them Config.Tests at a time, in order, until the first kill (with All,
// every one). It records each test in r and returns true when r is final:
// the mutated tree does not build (invalid), or the run was interrupted
// (no status).
func (w *worker) runTests(ctx context.Context, m *Mutant, ids []string, r *Result) bool {
	built := map[string]bool{}
	var runs []string
	for _, id := range ids {
		suite, _ := splitID(id)
		s := w.e.suite(suite)
		if s == nil {
			continue
		}
		if detail := w.build(ctx, m.Name, s, built); detail != "" {
			if killedByBuild(m, id, s, detail) {
				r.Status, r.Killer, r.Killers, r.Reason = Killed, id, []string{id}, "build"
				r.Detail = "killed by build failure after regenerate: " + firstCompileError(detail)
				return true
			}
			r.Status, r.Detail, r.Killers, r.Killer = Invalid, detail, nil, ""
			return true
		}
		runs = append(runs, id)
	}
	stop, cancel := context.WithCancel(ctx)
	defer cancel()
	outcomes := make([]*outcome, len(runs))
	slots := make(chan struct{}, max(1, w.e.cfg.Tests))
	var wg sync.WaitGroup
	for i, id := range runs {
		slots <- struct{}{}
		if stop.Err() != nil {
			<-slots
			break
		}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			o := w.runOne(stop, m, id)
			outcomes[i] = &o
			if killedBy(o) && !w.e.cfg.All {
				cancel()
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		r.Status, r.Detail = "", "interrupted"
		return true
	}
	for i, id := range runs {
		o := outcomes[i]
		switch {
		case o == nil:
		case o.reason == "interrupted":
			r.Stopped = append(r.Stopped, id) // stopped by an earlier kill
		case o.timeout:
			r.Ran, r.Hung = append(r.Ran, id), append(r.Hung, id)
			if r.Killer == "" && r.Detail == "" {
				r.Detail = id + " " + o.reason + " (a timeout, no kill): " + lastLines(o.tail, 30)
			}
		case !o.started:
			// The binary ended before the test started (a usage error, a
			// panic in an init, no test of that name): no kill, and no
			// pass either (GT-NOTESTS: Lane A's S2e run counted every row
			// killed when its test command errored before any test ran).
			r.NotStarted = append(r.NotStarted, id)
			if r.Killer == "" && r.Detail == "" {
				r.Detail = id + " ended before the test started (" + cmp.Or(o.reason, "exit 0") + ", no kill): " + lastLines(o.tail, 30)
			}
		case o.reason == "":
			r.Ran = append(r.Ran, id)
		default:
			r.Ran, r.Killers = append(r.Ran, id), append(r.Killers, id)
			if r.Killer == "" {
				r.Killer, r.Reason, r.Detail = id, o.reason, lastLines(o.tail, 30)
			}
		}
	}
	return false
}

// killedBy says whether o is a kill: a failure of a test that started
// (startLine), and not by its timeout.
func killedBy(o outcome) bool {
	return o.reason != "" && o.reason != "interrupted" && !o.timeout && o.started
}

// killedByBuild says whether a build failure of suite s, met before the
// test id ran, kills the row m rather than making it invalid. A failed
// build is a kill only for a generator's row: the row has a regenerate
// step, the mutated source compiled and the step ran (applyChecked, or
// regenerated after the named pass checked it; either failure makes the
// row invalid before any build), id is a killer the row names, and the
// failure is the compiler refusing that suite's own build, not a tool's
// build or its deadline. Then the generator's output no longer gives the
// suite what it uses (H3f-layer-dropped: drawgen without raster, and the
// host that draws with it does not compile), the strongest kill there is.
// Any other build failure stays invalid, so a careless row cannot pass
// for a kill (batch 23 fallout).
func killedByBuild(m *Mutant, id string, s *Suite, detail string) bool {
	return m.Regenerate != nil && s.Package != "" && slices.Contains(killerIDs(m), id) &&
		strings.HasPrefix(detail, "build "+s.Name+": fail\n")
}

// compileError matches a compiler error line: FILE.go:LINE:COL: text.
var compileError = regexp.MustCompile(`(?m)^\S+\.go:\d+:\d+: .*$`)

// firstCompileError is the first compiler error of a failed build's
// detail, or its first line when none is there.
func firstCompileError(detail string) string {
	if line := compileError.FindString(detail); line != "" {
		return line
	}
	return firstLine(detail)
}

// testError is a line a failing Go test prints: a t.Error or t.Fatal
// line (file_test.go:12: …), a panic, or a build error; or the FAIL line
// of a command killer (scripts/examples: "FAIL modules/work: test: …").
var testError = regexp.MustCompile(`(?m)^\s*(\S+\.go:\d+(:\d+)?: .*|panic: .*|FAIL \S+: .*)$`)

// firstError is the first error line of a failed test's output, else its
// first line that is neither go test's bookkeeping nor a passing or
// skipped case of a command killer ("ok …", "skip …": I22's
// clone-follows-links named the examples runner's full-tier skip line as
// the failure); "" when it has none.
func firstError(out string) string {
	if line := testError.FindString(out); line != "" {
		return strings.TrimSpace(line)
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "FAIL" || strings.HasPrefix(line, "=== ") || strings.HasPrefix(line, "--- FAIL") || strings.HasPrefix(line, "ok ") || strings.HasPrefix(line, "skip ") {
			continue
		}
		return line
	}
	return ""
}

// baseDetail is the Detail of id's red run on the unmutated tree: its
// reason, its first error line and the file that keeps its whole output
// (keepRed), then its output's last lines.
func baseDetail(id string, o outcome) string {
	first := firstError(o.tail)
	if path := keepRed(id, o); path != "" {
		first += " (whole output: " + path + ")"
	}
	return o.reason + ": " + first + "\n" + lastLines(o.tail, 20)
}

// redDetail is the Detail of a row whose covering tests red fail on the
// unmutated tree: each with its first error line.
func (e *engine) redDetail(red []string) string {
	var parts []string
	for _, id := range red {
		if t := e.info(id); t != nil {
			if _, rest, _ := strings.Cut(t.Detail, ": "); firstError(rest) != "" {
				id += " (" + firstError(rest) + ")"
			}
		}
		parts = append(parts, id)
	}
	return strings.Join(parts, " ") + " fails on the unmutated tree"
}

// notRun is the reason of a test that passed without starting: no test of
// its name ran ("testing: warning: no tests to run").
const notRun = "not run"

// neverRan makes a run on the unmutated tree that never started its test
// a failure there: a named test that runs nothing judges nothing.
func neverRan(o outcome) outcome {
	if o.reason == "" && !o.started {
		o.reason = notRun
	}
	return o
}

// runOne runs one test on the mutated copy, with a deadline derived from
// its run on the unmutated tree.
func (w *worker) runOne(ctx context.Context, m *Mutant, id string) outcome {
	suite, name := splitID(id)
	s := w.e.suite(suite)
	deadline := max(w.e.testFloor(), time.Duration(deadlineFactor*w.e.info(id).Seconds*float64(time.Second)))
	argv, cwd, env := w.e.argv(s, w.root, w.bin, name, deadline)
	w.e.logf("%s: run %s (deadline %s)", m.Name, id, deadline.Round(time.Second))
	return w.e.execStarted(ctx, cwd, argv, w.e.env(append(env, MutantVariable+"="+m.Name)...), deadline, startLine(s, name))
}

func (e *engine) testFloor() time.Duration {
	if e.cfg.TestFloor > 0 {
		return e.cfg.TestFloor
	}
	return testFloor
}

// build builds what a suite needs in the mutated copy of the mutant named
// who, once per mutant. It returns why the mutated tree does not build,
// or "".
func (w *worker) build(ctx context.Context, who string, s *Suite, built map[string]bool) string {
	return w.buildWith(ctx, who, s, built, 0)
}

// buildWith is build with a fixed deadline; 0 derives each one from the
// build's time on the unmutated tree. Each build logs its start with its
// deadline and its end with its time, so a run whose builds take minutes
// (lower's test binary) shows where it is and what bounds it (GT-SINGLE:
// a one-row run printed nothing for minutes between its first line and
// its verdict).
func (w *worker) buildWith(ctx context.Context, who string, s *Suite, built map[string]bool, deadline time.Duration) string {
	for _, t := range toolsOf(s) {
		if built["tool:"+t] {
			continue
		}
		w.e.logf("%s: build tool %s (deadline %s)", who, t, w.buildDeadline(t, deadline).Round(time.Second))
		o, err := w.e.buildTool(ctx, w.root, w.bin, t, false, w.buildDeadline(t, deadline))
		if err != nil {
			return err.Error()
		}
		if o.reason != "" {
			return "build " + t + ": " + o.reason + "\n" + lastLines(o.tail, 20)
		}
		w.e.logf("%s: built tool %s in %.0fs", who, t, o.seconds)
		w.e.noteBuild(t, o.seconds)
		built["tool:"+t] = true
	}
	if s.Package != "" && !built["suite:"+s.Name] {
		w.e.logf("%s: build %s (deadline %s)", who, s.Name, w.buildDeadline(s.Name, deadline).Round(time.Second))
		o := w.e.buildSuite(ctx, w.root, w.bin, s, false, w.buildDeadline(s.Name, deadline))
		if o.reason != "" {
			return "build " + s.Name + ": " + o.reason + "\n" + lastLines(o.tail, 20)
		}
		w.e.logf("%s: built %s in %.0fs", who, s.Name, o.seconds)
		w.e.noteBuild(s.Name, o.seconds)
		built["suite:"+s.Name] = true
	}
	return ""
}

// noteBuild keeps the first time a build took on this run, the base of its
// later deadlines when no coverage pass timed it.
func (e *engine) noteBuild(name string, seconds float64) {
	e.buildMu.Lock()
	defer e.buildMu.Unlock()
	if e.builds == nil {
		e.builds = map[string]float64{}
	}
	if _, ok := e.builds[name]; !ok {
		e.builds[name] = seconds
	}
}

func (w *worker) buildDeadline(name string, fixed time.Duration) time.Duration {
	if fixed > 0 {
		return fixed
	}
	seconds := 0.0
	if w.e.cov != nil {
		seconds = w.e.cov.Builds[name]
	}
	w.e.buildMu.Lock()
	seconds = max(seconds, w.e.builds[name])
	w.e.buildMu.Unlock()
	return max(buildFloor, time.Duration(deadlineFactor*seconds*float64(time.Second)))
}
