package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

// Config is what the runner knows of a module: the packages whose blocks
// count, the tools its suites need, and the suites.
type Config struct {
	// Root is the checkout. The runner never writes it: it mutates copies
	// under Work (one per worker).
	Root string
	Work string
	// Cover are the packages (go package patterns relative to Root) whose
	// blocks the coverage pass records.
	Cover  []string
	Tools  []Tool
	Suites []Suite
	// Workers is the number of mutants judged at once, each in its own
	// copy; Procs is each child's GOMAXPROCS and go -p.
	Workers int
	Procs   int
	// Memory is each child's RLIMIT_DATA (internal/proclimit): a mutant
	// that makes a program grow without end kills that test, never the
	// runner.
	Memory uint64
	// All runs every covering test of a mutant, not only until the first
	// kill, to name every killer.
	All bool
	// Coverage, when set, is a coverage file to read instead of measuring.
	Coverage string
	// CoverDeadline bounds each child of the coverage pass, which has no
	// earlier run to derive a deadline from.
	CoverDeadline time.Duration
	Log           io.Writer
}

// Tool is a binary that suites run, built from the checkout (with
// coverage in the coverage pass).
type Tool struct {
	Name    string
	Package string
	Flags   []string
}

// Suite is a set of tests. A Go suite is a test package: its tests are the
// top-level Test functions of its test binary. A command suite names its
// tests with List and runs one with Command. Env and Command may hold the
// words {bin:TOOL} (a tool's binary), {root} (the copy's root) and {test}.
type Suite struct {
	Name    string
	Package string
	Command []string
	List    func(root string) ([]string, error)
	Env     []string
}

// Deadlines of the mutant pass, derived from the coverage pass: a child may
// take deadlineFactor times as long as the same test or build took there,
// and never less than the floor.
const (
	deadlineFactor = 10
	testFloor      = 2 * time.Minute
	buildFloor     = 10 * time.Minute
)

// MutantVariable names the mutant a child of the mutant pass runs under.
const MutantVariable = "ERGO_MUTANT"

// Statuses of a mutant.
const (
	Killed    = "killed"
	Survived  = "survived"
	Uncovered = "uncovered" // no test runs its lines
	Stale     = "stale"     // its edit does not apply: report it to its origin
	Invalid   = "invalid"   // the mutated tree does not build: report it to its origin
)

// Result is the verdict on one mutant.
type Result struct {
	Ledger   string   `json:"ledger"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Line     int      `json:"line,omitempty"`
	Killer   string   `json:"killer,omitempty"`
	Reason   string   `json:"reason,omitempty"` // fail, deadline or memory
	Killers  []string `json:"killers,omitempty"`
	Covering int      `json:"covering"`
	Ran      []string `json:"ran,omitempty"`
	Seconds  float64  `json:"seconds"`
	Detail   string   `json:"detail,omitempty"`
}

// Job is one ledger to run.
type Job struct {
	Path   string // the ledger's path, for the results
	Ledger *Ledger
	Select *regexp.Regexp // nil: every mutant
}

// Run judges every selected mutant of the jobs. It returns the results in
// ledger order, the coverage it used, and an error when the run itself
// failed (a build of the unmutated tree, a copy left mutated).
func Run(ctx context.Context, cfg Config, jobs []Job) ([]Result, *coverage, error) {
	e, err := newEngine(cfg)
	if err != nil {
		return nil, nil, err
	}
	var ledgers []string
	for _, j := range jobs {
		ledgers = append(ledgers, j.Path)
	}
	if err := e.load(ledgers); err != nil {
		return nil, nil, err
	}
	if err := e.prepareCoverage(ctx); err != nil {
		return nil, nil, err
	}
	results, err := e.judgeAll(ctx, jobs)
	return results, e.cov, err
}

type engine struct {
	cfg    Config
	module string
	files  []treeFile
	key    string
	cov    *coverage
	logMu  sync.Mutex
}

type treeFile struct {
	path string
	mode fs.FileMode
	data []byte // the link target for a symbolic link
}

func newEngine(cfg Config) (*engine, error) {
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.Procs < 1 {
		cfg.Procs = 1
	}
	if cfg.CoverDeadline <= 0 {
		cfg.CoverDeadline = time.Hour
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	var err error
	if cfg.Root, err = filepath.Abs(cfg.Root); err != nil {
		return nil, err
	}
	if cfg.Work, err = filepath.Abs(cfg.Work); err != nil {
		return nil, err
	}
	if inside(cfg.Work, cfg.Root) {
		return nil, fmt.Errorf("work directory %s is inside the checkout %s", cfg.Work, cfg.Root)
	}
	mod, err := os.ReadFile(filepath.Join(cfg.Root, "go.mod"))
	if err != nil {
		return nil, err
	}
	e := &engine{cfg: cfg}
	for _, line := range strings.Split(string(mod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			e.module = strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	if e.module == "" {
		return nil, errors.New("go.mod names no module")
	}
	return e, os.MkdirAll(cfg.Work, 0o755)
}

func inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func (e *engine) logf(format string, args ...any) {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	fmt.Fprintf(e.cfg.Log, format+"\n", args...)
}

// load reads the checkout's files (tracked and untracked, not ignored) and
// computes the tree's key, which leaves the ledgers out: writing a ledger
// does not change what the tests cover.
func (e *engine) load(ledgers []string) error {
	cmd := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "-c", "-o", "--exclude-standard")
	cmd.Dir = e.cfg.Root
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	skip := map[string]bool{}
	for _, l := range ledgers {
		if rel, err := filepath.Rel(e.cfg.Root, l); err == nil {
			skip[filepath.ToSlash(rel)] = true
		}
	}
	names := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	slices.Sort(names)
	names = slices.Compact(names)
	h := sha256.New()
	for _, name := range names {
		if name == "" {
			continue
		}
		path := filepath.Join(e.cfg.Root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the work tree
		}
		if err != nil {
			return err
		}
		f := treeFile{path: name, mode: info.Mode()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			f.data = []byte(target)
		case info.Mode().IsRegular():
			if f.data, err = os.ReadFile(path); err != nil {
				return err
			}
		default:
			continue
		}
		e.files = append(e.files, f)
		if !skip[name] {
			fmt.Fprintf(h, "%s\x00%o\x00%d\x00", name, f.mode&(fs.ModeSymlink|0o111), len(f.data))
			h.Write(f.data)
		}
	}
	fmt.Fprintf(h, "cover %q tools %q", e.cfg.Cover, toolNames(e.cfg.Tools))
	for _, s := range e.cfg.Suites {
		fmt.Fprintf(h, " suite %s %s %q %q", s.Name, s.Package, s.Command, s.Env)
	}
	e.key = hex.EncodeToString(h.Sum(nil))[:16]
	return nil
}

func toolNames(tools []Tool) []string {
	var out []string
	for _, t := range tools {
		out = append(out, t.Name+"="+t.Package)
	}
	return out
}

// copyTree writes a fresh copy of the checkout's files at dst.
func (e *engine) copyTree(dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	for _, f := range e.files {
		path := filepath.Join(dst, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if f.mode&fs.ModeSymlink != 0 {
			if err := os.Symlink(string(f.data), path); err != nil {
				return err
			}
			continue
		}
		if err := os.WriteFile(path, f.data, f.mode.Perm()|0o200); err != nil {
			return err
		}
	}
	return nil
}

// verifyCopy checks that every file a mutant touched in the copy at dst is
// back to the checkout's bytes.
func (e *engine) verifyCopy(dst string, touched map[string]bool) error {
	var bad []string
	for _, f := range e.files {
		if !touched[f.path] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(f.path)))
		if err != nil || !bytes.Equal(data, f.data) {
			bad = append(bad, f.path)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("a mutant was not restored: %s in %s differs from the checkout", strings.Join(bad, ", "), dst)
	}
	return nil
}

// env is the environment of every child: the runner's own, with the
// thread share, and extra.
func (e *engine) env(extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GOCOVERDIR", "GOMAXPROCS", "GOFLAGS", "GOWORK", MutantVariable:
			continue
		}
		out = append(out, kv)
	}
	procs := strconv.Itoa(e.cfg.Procs)
	out = append(out, "GOMAXPROCS="+procs, "GOFLAGS=-p="+procs, "GOWORK=off")
	return append(out, extra...)
}

// outcome is how a child ended. Reason is empty when it exited 0.
type outcome struct {
	reason  string // fail, deadline, memory
	seconds float64
	tail    string
}

// exec runs argv in dir as the leader of its own process group, under the
// memory limit and the deadline, and stops the whole group when it ends.
func (e *engine) exec(ctx context.Context, dir string, argv, env []string, deadline time.Duration) outcome {
	start := time.Now()
	out := &tail{}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = out
	cmd.Stderr = out
	// Read every byte before tail truncates the display. The shared writer
	// keeps stdout and stderr ordered, as they were before the watcher.
	watch := proclimit.Watch(cmd)
	// A process the child leaves behind may hold the output pipe: Wait
	// gives up on it a second after the child exits, and the group stop
	// below ends it.
	cmd.WaitDelay = time.Second
	ownGroup(cmd)
	if err := proclimit.Start(cmd, e.cfg.Memory); err != nil {
		return outcome{reason: "fail", tail: err.Error()}
	}
	var mu sync.Mutex
	exited, timedOut := false, false
	stop := func(deadline bool) {
		mu.Lock()
		defer mu.Unlock()
		if !exited {
			timedOut = timedOut || deadline
			stopGroup(cmd.Process)
		}
	}
	timer := time.AfterFunc(deadline, func() { stop(true) })
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stop(false)
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	timer.Stop()
	mu.Lock()
	// The leader is gone; stop what it left in its group.
	stopGroup(cmd.Process)
	exited = true
	mu.Unlock()
	o := outcome{seconds: time.Since(start).Seconds(), tail: out.String()}
	o.reason = childReason(err, timedOut, e.cfg.Memory, watch.Stderr())
	if ctx.Err() != nil && o.reason != "" {
		o.reason = "interrupted"
	}
	return o
}

// tail keeps the first and the last bytes a child wrote: a crash names its
// cause first, a failing test last.
type tail struct {
	mu      sync.Mutex
	head    []byte
	buf     []byte
	dropped bool
}

const tailSize = 16 << 10

// childReason classifies a completed command. Success and the deadline take
// precedence over a memory report; a report without a cap stays a failure.
func childReason(err error, timedOut bool, memory uint64, report []byte) string {
	switch {
	case err == nil || errors.Is(err, exec.ErrWaitDelay):
		return ""
	case timedOut:
		return "deadline"
	case memory != 0 && proclimit.CauseOf(report) != proclimit.NoCause:
		return "memory"
	}
	return "fail"
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if room := tailSize/4 - len(t.head); room > 0 {
		k := min(room, len(p))
		t.head = append(t.head, p[:k]...)
		p = p[k:]
	}
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*tailSize {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-tailSize:]...)
		t.dropped = true
	}
	return n, nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	rest := t.buf
	if len(rest) > tailSize {
		rest, t.dropped = rest[len(rest)-tailSize:], true
	}
	if t.dropped {
		return string(t.head) + "\n…\n" + string(rest)
	}
	return string(t.head) + string(rest)
}

var toolWord = regexp.MustCompile(`\{bin:([A-Za-z0-9_-]+)\}`)

// expand replaces the words of a suite's Env and Command.
func expand(s, bin, root, test string) string {
	s = toolWord.ReplaceAllStringFunc(s, func(w string) string {
		return filepath.Join(bin, toolWord.FindStringSubmatch(w)[1])
	})
	return strings.ReplaceAll(strings.ReplaceAll(s, "{root}", root), "{test}", test)
}

// toolsOf returns the tools a suite runs.
func toolsOf(s *Suite) []string {
	var out []string
	for _, w := range append(slices.Clone(s.Env), s.Command...) {
		for _, m := range toolWord.FindAllStringSubmatch(w, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	return out
}

func (e *engine) suite(name string) *Suite {
	for i := range e.cfg.Suites {
		if e.cfg.Suites[i].Name == name {
			return &e.cfg.Suites[i]
		}
	}
	return nil
}

func (e *engine) tool(name string) *Tool {
	for i := range e.cfg.Tools {
		if e.cfg.Tools[i].Name == name {
			return &e.cfg.Tools[i]
		}
	}
	return nil
}

// buildTool builds a tool into bin; with cover, with coverage of the
// covered packages and of the tool's own package (a binary whose main
// package is not covered writes no counters).
func (e *engine) buildTool(ctx context.Context, root, bin, name string, cover bool, deadline time.Duration) (outcome, error) {
	t := e.tool(name)
	if t == nil {
		return outcome{}, fmt.Errorf("no tool %q", name)
	}
	argv := []string{"go", "build"}
	if cover {
		argv = append(argv, "-cover", "-covermode=set", "-coverpkg="+strings.Join(append(slices.Clone(e.cfg.Cover), t.Package), ","))
	}
	argv = append(append(argv, t.Flags...), "-o", filepath.Join(bin, name), t.Package)
	return e.exec(ctx, root, argv, e.env(), deadline), nil
}

func (e *engine) buildSuite(ctx context.Context, root, bin string, s *Suite, cover bool, deadline time.Duration) outcome {
	argv := []string{"go", "test", "-c"}
	if cover {
		argv = append(argv, "-cover", "-covermode=set", "-coverpkg="+strings.Join(e.cfg.Cover, ","))
	}
	argv = append(argv, "-o", filepath.Join(bin, s.Name+".test"), s.Package)
	return e.exec(ctx, root, argv, e.env(), deadline)
}

// argv returns how to run one test of a suite, and where.
func (e *engine) argv(s *Suite, root, bin, test string) ([]string, string, []string) {
	var env []string
	for _, kv := range s.Env {
		env = append(env, expand(kv, bin, root, test))
	}
	if s.Package != "" {
		return []string{filepath.Join(bin, s.Name+".test"), "-test.run=^" + regexp.QuoteMeta(test) + "$", "-test.count=1"},
			filepath.Join(root, filepath.FromSlash(s.Package)), env
	}
	var argv []string
	for _, w := range s.Command {
		argv = append(argv, expand(w, bin, root, test))
	}
	return argv, root, env
}

func splitID(id string) (string, string) {
	suite, name, _ := strings.Cut(id, ":")
	return suite, name
}

// prepareCoverage reads the coverage file, the cached one of this tree, or
// measures it.
func (e *engine) prepareCoverage(ctx context.Context) error {
	path := e.cfg.Coverage
	if path == "" {
		path = filepath.Join(e.cfg.Work, "coverage-"+e.key+".json.gz")
		if _, err := os.Stat(path); err != nil {
			e.logf("coverage: measuring (tree %s) into %s", e.key, path)
			c, err := e.measure(ctx)
			if err != nil {
				return err
			}
			if err := saveCoverage(path, c); err != nil {
				return err
			}
		}
	}
	c, err := loadCoverage(path)
	if err != nil {
		return err
	}
	if e.cfg.Coverage == "" && c.Key != e.key {
		return fmt.Errorf("%s: measured on tree %s, not %s", path, c.Key, e.key)
	}
	e.logf("coverage: %s (%d tests)", path, len(c.Tests))
	e.cov = c
	return nil
}

// measure runs every test of every suite once, each in its own process
// with its own coverage directory, in a fresh copy of the checkout.
func (e *engine) measure(ctx context.Context) (*coverage, error) {
	root := filepath.Join(e.cfg.Work, "cover", "src")
	bin := filepath.Join(e.cfg.Work, "cover", "bin")
	if err := e.copyTree(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return nil, err
	}
	c := &coverage{Key: e.key, Builds: map[string]float64{}}
	for _, t := range e.cfg.Tools {
		o, err := e.buildTool(ctx, root, bin, t.Name, true, e.cfg.CoverDeadline)
		if err == nil && o.reason != "" {
			err = fmt.Errorf("build %s with coverage: %s\n%s", t.Name, o.reason, o.tail)
		}
		if err != nil {
			return nil, err
		}
		c.Builds[t.Name] = o.seconds
	}
	for i := range e.cfg.Suites {
		s := &e.cfg.Suites[i]
		var names []string
		if s.Package != "" {
			o := e.buildSuite(ctx, root, bin, s, true, e.cfg.CoverDeadline)
			if o.reason != "" {
				return nil, fmt.Errorf("build suite %s with coverage: %s\n%s", s.Name, o.reason, o.tail)
			}
			c.Builds[s.Name] = o.seconds
			list, err := exec.Command(filepath.Join(bin, s.Name+".test"), "-test.list", ".").Output()
			if err != nil {
				return nil, fmt.Errorf("list the tests of %s: %w", s.Name, err)
			}
			for _, n := range strings.Fields(string(list)) {
				if strings.HasPrefix(n, "Test") {
					names = append(names, n)
				}
			}
		} else {
			var err error
			if names, err = s.List(root); err != nil {
				return nil, fmt.Errorf("list the tests of %s: %w", s.Name, err)
			}
		}
		for _, n := range names {
			c.Tests = append(c.Tests, &covTest{ID: s.Name + ":" + n})
		}
	}
	k := &collector{module: e.module, blocks: map[blockKey][]int{}}
	var mu sync.Mutex
	var firstErr error
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < e.cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				err := e.measureOne(ctx, root, bin, c.Tests[i], i, k, &mu)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := range c.Tests {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	if firstErr != nil {
		return nil, firstErr
	}
	c.Files = k.files()
	return c, nil
}

func (e *engine) measureOne(ctx context.Context, root, bin string, t *covTest, i int, k *collector, mu *sync.Mutex) error {
	dir := filepath.Join(e.cfg.Work, "cover", "counters", strconv.Itoa(i))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	suiteName, name := splitID(t.ID)
	s := e.suite(suiteName)
	argv, cwd, env := e.argv(s, root, bin, name)
	if s.Package != "" {
		argv = append(argv, "-test.gocoverdir="+dir)
	}
	o := e.exec(ctx, cwd, argv, e.env(append(env, "GOCOVERDIR="+dir)...), e.cfg.CoverDeadline)
	t.Seconds = o.seconds
	if o.reason != "" {
		t.Failed = true
		t.Detail = o.reason + ": " + lastLines(o.tail, 20)
		e.logf("coverage: %s fails on the unmutated tree (%s): it kills nothing", t.ID, o.reason)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return err
	}
	profile := dir + ".txt"
	defer os.Remove(profile)
	text := e.exec(ctx, root, []string{"go", "tool", "covdata", "textfmt", "-i=" + dir, "-o=" + profile}, e.env(), e.cfg.CoverDeadline)
	if text.reason != "" {
		return fmt.Errorf("covdata textfmt for %s: %s\n%s", t.ID, text.reason, text.tail)
	}
	f, err := os.Open(profile)
	if err != nil {
		return err
	}
	defer f.Close()
	mu.Lock()
	defer mu.Unlock()
	return k.add(i, f)
}

// headerLine reports whether the line of text at byte offset at is a case
// or an else-if header: lines the cover tool gives no block of their own,
// though the code that reaches them runs them (coverage.covering).
func headerLine(text string, at int) bool {
	start := strings.LastIndexByte(text[:at], '\n') + 1
	line, _, _ := strings.Cut(text[start:], "\n")
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "case ") || strings.HasPrefix(line, "} else if ")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// judgeAll runs the mutants on Workers copies of the checkout.
func (e *engine) judgeAll(ctx context.Context, jobs []Job) ([]Result, error) {
	type item struct {
		at     int
		ledger string
		m      *Mutant
	}
	var items []item
	for _, j := range jobs {
		for _, m := range j.Ledger.Mutants {
			if j.Select == nil || j.Select.MatchString(m.Name) {
				items = append(items, item{len(items), j.Path, m})
			}
		}
	}
	results := make([]Result, len(items))
	next := make(chan item)
	var wg sync.WaitGroup
	errs := make([]error, e.cfg.Workers)
	for w := 0; w < e.cfg.Workers; w++ {
		dir := filepath.Join(e.cfg.Work, "w"+strconv.Itoa(w))
		wk := &worker{e: e, root: filepath.Join(dir, "src"), bin: filepath.Join(dir, "bin"), touched: map[string]bool{}}
		if err := e.copyTree(wk.root); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(wk.bin, 0o755); err != nil {
			return nil, err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range next {
				r := wk.judge(ctx, it.m)
				r.Ledger = it.ledger
				results[it.at] = r
				e.logf("%s: %s %s %s (%.0f s, %d run of %d covering)", it.m.Name, r.Status, r.Killer, r.Reason, r.Seconds, len(r.Ran), r.Covering)
			}
			errs[w] = errors.Join(wk.err, e.verifyCopy(wk.root, wk.touched))
		}()
	}
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		next <- it
	}
	close(next)
	wg.Wait()
	return results, errors.Join(append(errs, ctx.Err())...)
}

type worker struct {
	e       *engine
	root    string
	bin     string
	touched map[string]bool
	err     error
}

// judge applies one mutant, runs its covering tests (known killers first,
// then cheapest first) until the first kill, and restores the file.
func (w *worker) judge(ctx context.Context, m *Mutant) (r Result) {
	start := time.Now()
	r.Name = m.Name
	defer func() { r.Seconds = time.Since(start).Seconds() }()
	path := filepath.Join(w.root, filepath.FromSlash(m.File))
	original, err := os.ReadFile(path)
	if err != nil {
		r.Status, r.Detail = Stale, err.Error()
		return r
	}
	if n := strings.Count(string(original), m.From); n != 1 {
		r.Status, r.Detail = Stale, fmt.Sprintf("its from occurs %d times in %s", n, m.File)
		return r
	}
	at := strings.Index(string(original), m.From)
	r.Line = 1 + strings.Count(string(original[:at]), "\n")
	file, lo, hi := m.File, r.Line, r.Line+strings.Count(m.From, "\n")
	header := headerLine(string(original), at)
	var extra []string
	if a := m.Anchor; a != nil {
		extra = a.Tests
		if a.Text != "" {
			if a.File != "" {
				file = a.File
			}
			text := original
			if file != m.File {
				if text, err = os.ReadFile(filepath.Join(w.root, filepath.FromSlash(file))); err != nil {
					r.Status, r.Detail = Stale, "anchor: "+err.Error()
					return r
				}
			}
			if n := strings.Count(string(text), a.Text); n != 1 {
				r.Status, r.Detail = Stale, fmt.Sprintf("its anchor text occurs %d times in %s", n, file)
				return r
			}
			at := strings.Index(string(text), a.Text)
			lo = 1 + strings.Count(string(text[:at]), "\n")
			hi = lo + strings.Count(a.Text, "\n")
		}
	}
	covering := w.e.cov.covering(file, lo, hi, header && m.Anchor == nil, extra)
	r.Covering = len(covering)
	if len(covering) == 0 {
		r.Status = Uncovered
		return r
	}
	info, err := os.Stat(path)
	if err != nil {
		w.err = errors.Join(w.err, err)
		r.Status, r.Detail = Invalid, err.Error()
		return r
	}
	w.touched[m.File] = true
	if err := os.WriteFile(path, []byte(strings.Replace(string(original), m.From, m.To, 1)), info.Mode().Perm()); err != nil {
		w.err = errors.Join(w.err, err)
		r.Status, r.Detail = Invalid, err.Error()
		return r
	}
	defer func() {
		if err := os.WriteFile(path, original, info.Mode().Perm()); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("restore %s: %w", m.File, err))
		}
	}()
	built := map[string]bool{}
	for _, id := range w.e.cov.order(covering, m.Killers) {
		if ctx.Err() != nil {
			r.Status, r.Detail = Invalid, "interrupted"
			return r
		}
		suiteName, name := splitID(id)
		s := w.e.suite(suiteName)
		if s == nil {
			continue
		}
		if detail := w.build(ctx, s, built); detail != "" {
			r.Status, r.Detail, r.Killers, r.Killer = Invalid, detail, nil, ""
			return r
		}
		argv, cwd, env := w.e.argv(s, w.root, w.bin, name)
		deadline := max(testFloor, time.Duration(deadlineFactor*w.e.cov.test(id).Seconds*float64(time.Second)))
		o := w.e.exec(ctx, cwd, argv, w.e.env(append(env, MutantVariable+"="+m.Name)...), deadline)
		r.Ran = append(r.Ran, id)
		if o.reason == "interrupted" {
			r.Status, r.Detail = Invalid, "interrupted"
			return r
		}
		if o.reason != "" {
			r.Killers = append(r.Killers, id)
			if r.Killer == "" {
				r.Killer, r.Reason, r.Detail = id, o.reason, lastLines(o.tail, 30)
			}
			if !w.e.cfg.All {
				break
			}
		}
	}
	r.Status = Survived
	if r.Killer != "" {
		r.Status = Killed
	}
	return r
}

// build builds what a suite needs in the mutated copy, once per mutant. It
// returns why the mutated tree does not build, or "".
func (w *worker) build(ctx context.Context, s *Suite, built map[string]bool) string {
	for _, t := range toolsOf(s) {
		if built["tool:"+t] {
			continue
		}
		o, err := w.e.buildTool(ctx, w.root, w.bin, t, false, w.buildDeadline(t))
		if err != nil {
			return err.Error()
		}
		if o.reason != "" {
			return "build " + t + ": " + o.reason + "\n" + lastLines(o.tail, 20)
		}
		built["tool:"+t] = true
	}
	if s.Package != "" && !built["suite:"+s.Name] {
		if o := w.e.buildSuite(ctx, w.root, w.bin, s, false, w.buildDeadline(s.Name)); o.reason != "" {
			return "build " + s.Name + ": " + o.reason + "\n" + lastLines(o.tail, 20)
		}
		built["suite:"+s.Name] = true
	}
	return ""
}

func (w *worker) buildDeadline(name string) time.Duration {
	return max(buildFloor, time.Duration(deadlineFactor*w.e.cov.Builds[name]*float64(time.Second)))
}
