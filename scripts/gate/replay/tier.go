package replay

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// TryReplay replays W(K) for the facts' key K when the facts can key a
// replay and every object W(K) names verifies (Lookup). Otherwise it says
// why not, and the gate runs.
func (s Store) TryReplay(facts *Facts, spool func() (*os.File, error)) (*Replay, string) {
	key := facts.Key()
	if problem := facts.Problem(); problem != "" {
		return nil, "no whole-gate replay: " + problem
	}
	replay, err := s.Lookup(key, spool)
	if err != nil {
		return nil, fmt.Sprintf("no whole-gate replay (key %s): %v", key[:12], err)
	}
	return replay, ""
}

// Run is one gate run (the inner tier or --no-cache) from its start facts.
// It records W(K) at the start key K when every step passed and the key at
// the end of the run equals K, and it withdraws W(K) when a step failed.
type Run struct {
	store Store
	start *Facts
	key   string
	// passed are the passed steps in declaration order, each with the
	// object that holds its output.
	passed []passedStep
	// objects are every object the run started, for abort.
	objects []*Object
	problem string
}

type passedStep struct {
	step   Step
	object *Object
}

// Begin starts a run at the start facts.
func (s Store) Begin(start *Facts) *Run {
	return &Run{store: s, start: start, key: start.Key()}
}

// Key is the run's start key.
func (r *Run) Key() string { return r.key }

// Output starts the object that holds a step's printed output. On an error
// the run records nothing, and the gate prints the step as usual.
func (r *Run) Output() (*Object, error) {
	object, err := r.store.NewObject()
	if err != nil {
		r.Refuse(fmt.Sprintf("store output: %v", err))
		return nil, err
	}
	r.objects = append(r.objects, object)
	return object, nil
}

// Refuse keeps the run from recording, for reason (the first one given).
func (r *Run) Refuse(reason string) {
	if r.problem == "" {
		r.problem = reason
	}
}

// Passed notes a passed step, in declaration order, with the object that
// holds its printed output.
func (r *Run) Passed(name string, object *Object, outcome string, seconds float64, passed time.Time) {
	if object == nil {
		r.Refuse(fmt.Sprintf("step %q has no stored output", name))
		return
	}
	r.passed = append(r.passed, passedStep{Step{Name: name, Outcome: outcome, Seconds: seconds, Passed: passed}, object})
}

// Meta is what a record says about the checkout besides its key.
type Meta struct {
	Head, Tree string
	Clean      bool
	Now        time.Time
}

// Finish ends the run and returns a line for the log, or "".
//   - A run with a failed step records nothing and withdraws W(K).
//   - A passed run collects the facts again (all of them). It records W(K)
//     only when the end key equals K, neither collection names a recent
//     stat-keyed path, and the facts can key a replay. It writes every
//     object first, then the record by one atomic rename.
func (r *Run) Finish(passed bool, collect func() (*Facts, error), meta Meta) string {
	defer r.abort()
	if !passed {
		if existed, err := r.store.withdraw(r.key); err != nil {
			return fmt.Sprintf("gate: withdraw whole-gate record %s: %v", r.key[:12], err)
		} else if existed {
			return fmt.Sprintf("gate: withdrew whole-gate record %s", r.key[:12])
		}
		return ""
	}
	refuse := func(reason string) string { return "gate: no whole-gate record: " + reason }
	if problem := r.start.Problem(); problem != "" {
		return refuse(problem)
	}
	if r.problem != "" {
		return refuse(r.problem)
	}
	if len(r.passed) == 0 {
		return refuse("no step passed")
	}
	// The objects first: content-addressed and written once, they are
	// harmless when no record follows (gatestore.qnt storeObject).
	record := Record{Version: Version, Key: r.key, Head: meta.Head, Tree: meta.Tree, Clean: meta.Clean, Recorded: meta.Now.UTC()}
	for _, p := range r.passed {
		sum, size, err := p.object.commit()
		if err != nil {
			return refuse(err.Error())
		}
		step := p.step
		step.Object, step.Bytes = sum, size
		record.Steps = append(record.Steps, step)
	}
	// Then the key again, all of it, and the record only at the start key
	// (gatestore.qnt record).
	end, err := collect()
	if err != nil {
		return refuse(fmt.Sprintf("collect the end key: %v", err))
	}
	if end.Key() != r.key {
		return refuse("the checkout or the host changed during the run: " + strings.Join(Diff(r.start, end, 5), ", "))
	}
	if problem := end.Problem(); problem != "" {
		return refuse(problem)
	}
	for _, facts := range []*Facts{r.start, end} {
		if len(facts.Recent) > 0 {
			return refuse(fmt.Sprintf("%s changed within %v of the key", facts.Recent[0], RecentWindow))
		}
	}
	if err := r.store.write(record); err != nil {
		return refuse(err.Error())
	}
	return fmt.Sprintf("gate: recorded whole-gate key %s", r.key[:12])
}

// abort drops the objects not committed.
func (r *Run) abort() {
	for _, object := range r.objects {
		object.Abort()
	}
	r.objects = nil
}

// withdraw removes W(key) and reports whether one was there.
func (s Store) withdraw(key string) (bool, error) {
	if _, err := os.Lstat(s.RecordPath(key)); err != nil {
		return false, nil
	}
	return true, s.Withdraw(key)
}

// Verdict is a step's verdict in the merge tier's table.
type Verdict string

const (
	Pass   Verdict = "pass"
	Failed Verdict = "FAILED"
	NotRun Verdict = "not run"
	// None is the inner verdict of a step the record does not hold.
	None Verdict = "none"
)

// Comparison is the merge tier's table.
type Comparison struct {
	Rows []string
	// Differ names the steps whose row is not "inner pass, uncached pass".
	Differ []string
	Steps  int
}

// Compare tabulates, for every declared step in order and then every step
// of the record that is not declared, the inner verdict (the record's: pass,
// or none) and the uncached one. Rows are equal exactly when both are pass.
func Compare(declared []string, uncached map[string]Verdict, record *Record) Comparison {
	inner := map[string]bool{}
	if record != nil {
		for _, step := range record.Steps {
			inner[step.Name] = true
		}
	}
	var c Comparison
	row := func(name string, in, un Verdict) {
		c.Rows = append(c.Rows, fmt.Sprintf("merge: %s: inner %s, uncached %s", name, in, un))
		c.Steps++
		if in != Pass || un != Pass {
			c.Differ = append(c.Differ, name)
		}
	}
	seen := map[string]bool{}
	for _, name := range declared {
		seen[name] = true
		in := None
		if inner[name] {
			in = Pass
		}
		un, ok := uncached[name]
		if !ok {
			un = NotRun
		}
		row(name, in, un)
	}
	if record != nil {
		for _, step := range record.Steps {
			if !seen[step.Name] {
				seen[step.Name] = true
				row(step.Name, Pass, NotRun)
			}
		}
	}
	return c
}

// Summary is the table's last line.
func (c Comparison) Summary() string {
	if len(c.Differ) == 0 {
		return fmt.Sprintf("merge: verdicts equal (%d steps)", c.Steps)
	}
	return "merge: FAILED: verdicts differ: " + strings.Join(c.Differ, ", ")
}

// MergeTier is the clean-against-incremental check: it runs every step
// uncached and compares each verdict with W(K).
type MergeTier struct {
	Store Store
	// Collect collects the facts of the checkout and host.
	Collect func() (*Facts, error)
	// Inner runs the inner tier, which records W(K) on a pass, and reports
	// whether it passed.
	Inner func() bool
	// Uncached runs every step uncached and returns the declared steps and
	// each one's verdict. It records no whole-gate record.
	Uncached func() ([]string, map[string]Verdict)
}

// Run runs the merge tier and returns the exit code.
//   - With no W(K), the inner tier runs first; if it fails, the merge tier
//     fails without the uncached run.
//   - The uncached run's verdicts are compared with W(K). A difference, or
//     step sets that differ, withdraws W(K) and fails.
//   - A source, HEAD, the index, the environment, a setting or the host
//     that changed during the uncached run fails it; ignored files the run
//     wrote are named and admitted (sources).
//   - The merge tier writes no whole-gate record.
func (m MergeTier) Run(out io.Writer) int {
	start, err := m.Collect()
	if err != nil {
		fmt.Fprintf(out, "merge: FAILED: collect the key: %v\n", err)
		return 2
	}
	key := start.Key()
	record, err := m.Store.Load(key)
	if err != nil {
		fmt.Fprintf(out, "merge: %v; the inner tier runs first\n", err)
		if !m.Inner() {
			fmt.Fprintln(out, "merge: FAILED: the inner tier failed")
			return 1
		}
		if record, err = m.Store.Load(key); err != nil {
			fmt.Fprintf(out, "merge: the inner tier left no record: %v\n", err)
			record = nil
		}
	}
	declared, verdicts := m.Uncached()
	comparison := Compare(declared, verdicts, record)
	for _, row := range comparison.Rows {
		fmt.Fprintln(out, row)
	}
	end, err := m.Collect()
	if err != nil {
		fmt.Fprintf(out, "merge: FAILED: collect the end key: %v\n", err)
		return 1
	}
	// The uncached run starts at K, so its verdicts answer for W(K) unless
	// a source, HEAD, the index, the environment, a setting or the host
	// changed under it. Its steps write ignored files into the checkout
	// (bin/ergo, test leftovers such as dist/main), which changes the whole
	// key but no source: the merge tier says so and goes on.
	if end.Key() != key {
		if start.sources().Key() != end.sources().Key() {
			fmt.Fprintf(out, "merge: FAILED: the checkout or the host changed during the merge run: %s\n", strings.Join(Diff(start, end, 5), ", "))
			return 1
		}
		fmt.Fprintf(out, "merge: the run wrote into the checkout: %s\n", strings.Join(Diff(start, end, 5), ", "))
	}
	if len(comparison.Differ) > 0 {
		if err := m.Store.Withdraw(key); err != nil {
			fmt.Fprintf(out, "merge: withdraw whole-gate record %s: %v\n", key[:12], err)
		}
		fmt.Fprintln(out, comparison.Summary())
		return 1
	}
	fmt.Fprintln(out, comparison.Summary())
	return 0
}
