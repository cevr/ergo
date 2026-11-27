package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ergo.local/prototype/scripts/mutants/anchor"
	"ergo.local/prototype/scripts/mutants/schemata"
)

// Builds counts the suite builds of a run (the count guard of
// TestSchemataVerdictMatchesIsolated): Rows those of mutated copies, one
// per row and suite on the isolated path, and Tree those of the schemata
// tree, one per suite for every row it folds.
type Builds struct {
	Rows atomic.Int64
	Tree atomic.Int64
}

// schemaTree is the run's schemata tree (mutant schemata): one copy of
// the checkout holding every foldable Go row behind schemata's switch,
// its suites built once. A row it does not hold, or one it cannot judge
// beyond doubt, takes the isolated path (judgeNamed) with the reason.
type schemaTree struct {
	w        *worker
	ids      map[string]int
	isolated map[string]string
	base     map[string]*schemaBase
	traces   atomic.Int64
}

// schemaBase is a named test's run on the schemata tree with no mutant
// enabled: whether it passed, and the sites it reached.
type schemaBase struct {
	ok     bool
	detail string
	hits   map[int]bool
}

// schemaEligible says why the row m cannot fold, or "": it edits a Go
// file that is no test, regenerates nothing, and every test it names is
// a known, green test of a Go suite.
func (e *engine) schemaEligible(m *Mutant) string {
	switch {
	case !strings.HasSuffix(m.File, ".go"):
		return "the edit is in " + filepath.Ext(m.File) + " source, which reaches its tests through a build the tests run (no Go fold)"
	case strings.HasSuffix(m.File, "_test.go"):
		return "the edit is in a test file"
	case m.Regenerate != nil:
		return "the row regenerates files"
	}
	named := e.namedKnown(m)
	if len(named) == 0 {
		return "the row names no known test"
	}
	for _, id := range named {
		suite, _ := splitID(id)
		if s := e.suite(suite); s == nil || s.Package == "" {
			return "its test " + id + " is no Go test"
		}
	}
	if len(e.red(named)) > 0 {
		return "a test it names fails on the unmutated tree"
	}
	return ""
}

func (e *engine) namedKnown(m *Mutant) []string {
	var named []string
	for _, id := range killerIDs(m) {
		if e.info(id) != nil {
			named = append(named, id)
		}
	}
	return named
}

// prepareSchemata folds the pending rows that can fold into one tree,
// builds the suites they name there once and runs each named test on it
// with no mutant enabled, recording the sites the test reaches. Any
// failure leaves the rows to the isolated path, with the reason.
func (e *engine) prepareSchemata(ctx context.Context, items []item, pending []int) error {
	if !e.cfg.Schemata || e.cfg.All || e.compileOnly {
		return nil
	}
	tree := &schemaTree{ids: map[string]int{}, isolated: map[string]string{}, base: map[string]*schemaBase{}}
	e.schema = tree
	originals := map[string][]byte{}
	var variants []schemata.Variant
	names := map[int]string{}
	for _, i := range pending {
		m := items[i].m
		if why := e.schemaEligible(m); why != "" {
			tree.isolated[m.Name] = why
			continue
		}
		if _, ok := originals[m.File]; !ok {
			data, err := os.ReadFile(filepath.Join(e.cfg.Root, filepath.FromSlash(m.File)))
			if err != nil {
				tree.isolated[m.Name] = err.Error()
				continue
			}
			originals[m.File] = data
		}
		mutated, err := anchor.Apply(m.File, originals[m.File], m.Decl, m.From, m.To)
		if err != nil {
			tree.isolated[m.Name] = "the edit does not apply: " + err.Error()
			continue
		}
		id := len(variants) + 1
		names[id] = m.Name
		variants = append(variants, schemata.Variant{ID: id, Name: m.Name, File: schemata.File{Path: m.File, Data: mutated}})
	}
	if len(variants) == 0 {
		return nil
	}
	var base []schemata.File
	for p, data := range originals {
		base = append(base, schemata.File{Path: p, Data: data})
	}
	dir := filepath.Join(e.cfg.Work, "schemata")
	w := &worker{e: e, root: filepath.Join(dir, "src"), bin: filepath.Join(dir, "bin"), touched: map[string]bool{}}
	if err := e.copyTree(w.root); err != nil {
		return err
	}
	if err := os.MkdirAll(w.bin, 0o755); err != nil {
		return err
	}
	tree.w = w
	folded, err := e.foldCompiled(ctx, w, base, variants, names, tree)
	if err != nil || len(folded.Folded) == 0 {
		return err
	}
	var suites []string
	var tests []string
	for id := range folded.Folded {
		for _, t := range e.namedKnown(items[e.itemOf(items, names[id])].m) {
			suite, _ := splitID(t)
			if !slices.Contains(suites, suite) {
				suites = append(suites, suite)
			}
			if !slices.Contains(tests, t) {
				tests = append(tests, t)
			}
		}
		tree.ids[names[id]] = id
	}
	slices.Sort(suites)
	slices.Sort(tests)
	e.logf("schemata: %d rows folded into one tree (%d isolated), %d suites to build once", len(folded.Folded), len(tree.isolated), len(suites))
	built := map[string]bool{}
	for _, suite := range suites {
		if detail := w.buildWith(ctx, "schemata", e.suite(suite), built, e.cfg.CoverDeadline); detail != "" {
			for id := range folded.Folded {
				tree.isolated[names[id]] = "the schemata tree does not build " + suite + ": " + firstCompileError(detail)
			}
			tree.ids = map[string]int{}
			e.logf("schemata: the tree does not build %s: every folded row takes the isolated path\n%s", suite, lastLines(detail, 12))
			return ctx.Err()
		}
		if e.cfg.Builds != nil {
			e.cfg.Builds.Tree.Add(1)
		}
	}
	for _, id := range tests {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		suite, name := splitID(id)
		s := e.suite(suite)
		o, hits := tree.run(ctx, s, name, "", e.schemaDeadline(id))
		b := &schemaBase{ok: o.reason == "" && o.started, hits: hits}
		if !b.ok {
			b.detail = "fails on the schemata tree with no mutant enabled (" + orElse(o.reason, "not started") + ")"
		}
		tree.base[id] = b
		e.logf("schemata: baseline %s ok=%v, reaches %d sites", id, b.ok, len(hits))
	}
	return nil
}

func orElse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (e *engine) itemOf(items []item, name string) int {
	for i, it := range items {
		if it.m.Name == name {
			return i
		}
	}
	return -1
}

func (e *engine) schemaDeadline(id string) time.Duration {
	return max(e.testFloor(), time.Duration(deadlineFactor*e.info(id).Seconds*float64(time.Second)))
}

// run runs one test of the tree with the mutant named enabled ("" none),
// and the sites the run reached by the trace.
func (t *schemaTree) run(ctx context.Context, s *Suite, test, mutant string, deadline time.Duration) (outcome, map[int]bool) {
	e := t.w.e
	trace := filepath.Join(t.w.bin, "trace-"+strconv.FormatInt(t.traces.Add(1), 10))
	argv, cwd, env := e.argv(s, t.w.root, t.w.bin, test, deadline)
	env = append(env, schemata.TraceEnv+"="+trace)
	if mutant != "" {
		env = append(env, MutantVariable+"="+mutant)
	}
	o := e.execStarted(ctx, cwd, argv, e.env(env...), deadline, startLine(s, test))
	hits := map[int]bool{}
	if data, err := os.ReadFile(trace); err == nil {
		for _, line := range strings.Fields(string(data)) {
			if n, err := strconv.Atoi(line); err == nil {
				hits[n] = true
			}
		}
	}
	_ = os.Remove(trace)
	return o, hits
}

// judgeSchemata is phase A for a row the tree holds: its source checks on
// the worker's copy, then its named tests that reach its site, on the
// tree, in order until the first kill. It reports a kill only of a test
// that failed and reached the mutant's site; ok is false (the row takes
// the isolated path) for every other end: no kill, a timeout, a test that
// did not start, a red test on the tree, or a row the tree does not hold.
func (e *engine) judgeSchemata(ctx context.Context, w *worker, m *Mutant) (r Result, ok bool) {
	t := e.schema
	if t == nil {
		return r, false
	}
	id, held := t.ids[m.Name]
	if !held {
		if why := t.isolated[m.Name]; why != "" {
			e.logf("%s: isolated: %s", m.Name, why)
		}
		return r, false
	}
	start := time.Now()
	r.Name = m.Name
	defer func() { r.Seconds += time.Since(start).Seconds() }()
	target, found := w.locate(m, &r)
	if !found {
		return r, false
	}
	named := e.namedKnown(m)
	r.Covering = len(named)
	restore, detail := w.applyChecked(ctx, m, target.path, target.original)
	if restore != nil {
		restore()
	}
	if detail != "" {
		e.logf("%s: isolated: the mutated file does not pass its checks", m.Name)
		return Result{}, false
	}
	r.checked = true
	for _, tid := range named {
		b := t.base[tid]
		if b == nil || !b.ok {
			e.logf("%s: isolated: %s %s", m.Name, tid, orElse(b.detailOf(), "has no run on the schemata tree"))
			return Result{}, false
		}
		if !b.hits[id] {
			continue
		}
		suite, name := splitID(tid)
		e.logf("%s: run %s on the schemata tree", m.Name, tid)
		o, hits := t.run(ctx, e.suite(suite), name, m.Name, e.schemaDeadline(tid))
		if ctx.Err() != nil {
			r.Status, r.Detail = "", "interrupted"
			return r, true
		}
		switch {
		case o.timeout || !o.started:
			e.logf("%s: isolated: %s did not end as a test on the schemata tree", m.Name, tid)
			return Result{}, false
		case o.reason == "":
			r.Ran = append(r.Ran, tid)
		case !hits[id]:
			e.logf("%s: isolated: %s failed on the schemata tree without reaching the site", m.Name, tid)
			return Result{}, false
		default:
			r.Ran = append(r.Ran, tid)
			r.Status, r.Killer, r.Killers, r.Reason = Killed, tid, []string{tid}, o.reason
			r.Detail = lastLines(o.tail, 30)
			return r, true
		}
	}
	e.logf("%s: isolated: no test killed it on the schemata tree; the isolated path confirms the verdict", m.Name)
	return Result{}, false
}

func (b *schemaBase) detailOf() string {
	if b == nil {
		return ""
	}
	return b.detail
}
