// Command gate runs the repository merge gate.
//
// Every check runs as a step of a small dependency graph: independent steps run
// concurrently, each step's output is printed whole in declaration order, and
// the gate exits nonzero naming every failed step. A step's output goes to an
// unlinked spool file on disk, which the gate prints through a fixed buffer,
// so what the gate holds in memory for a step does not grow with what the
// step prints. The gate writes step output only to a pipe, a device or a
// file on a disk file system, judged on the opened file: it refuses to run
// with a spool or standard output file elsewhere (tmpfs, for one), and it
// does not record a pass whose cache file would be there. A step
// whose result is a pure function of declared inputs records its pass under
// the content hash of those inputs (outside the tree, shared by every
// worktree), its output in a file beside the receipt; unchanged inputs replay
// that output instead of running again, while it has the size and digest the
// receipt names. Go
// tests are not cached here: Go's own per-package test cache stays
// authoritative, and slow packages are split into processes whose union is
// complete by construction (the last shard skips exactly what the others run).
// The gate's own tests are split by what their results depend on. Go's test
// cache records files, environment variables and the module's source, not
// the programs a test runs or the libraries they load. So this package, whose
// tests run git, ps, shells, the go command and gates in systemd scopes, is a
// host package (hostTestPackages): its step runs with -count=1, so Go's test
// cache never replays it, with its parallel tests bounded by hostTestParallel.
// The packages below it are hermetic: each runs no program
// but its own test binary and builds with CGO_ENABLED=0 (package
// scripts/gate/hermetic checks them), so they replay.
//
// Above the steps sits the whole-gate replay (package replay, wholegate.go).
// One key covers every input of every step: the gate executable, the
// checkout root, HEAD, the index, every path of the checkout, the declared
// step environment, the gate's settings and the host fingerprint, which
// includes every declared host program. A gate whose steps all pass records
// the run under that key when the key at its end equals the key at its
// start; a later gate at the same key prints the recorded run and starts no
// step, so the host step replays only inside a whole-gate replay. A failed
// run withdraws the record at its key. The merge tier (--merge) runs every
// step uncached and compares each verdict with the record; a difference
// fails it and withdraws the record.
//
// Every step runs under a memory budget: a hard cap for the step with all the
// processes it starts (ERGO_GATE_STEP_MEMORY, default 8G), one for all the
// steps of the gate together (ERGO_GATE_MEMORY, default half of physical
// memory) and one for all the gates of the host together, the bound of the
// gates' slice. ERGO_GATE_HOST_MEMORY lowers only this gate's own budgets to
// it; it never changes the slice's bound. Go processes in a step get
// GOMEMLIMIT at 3/4 of the step cap unless the caller set it. On Linux with a
// working `systemd-run --user`, the gate re-executes itself in one transient
// scope in the user slice ergo-gates.slice; the scope's MemoryMax is the gate
// budget plus a reserve for the gate itself, with swap denied. The slice's
// MemoryMax belongs to the host: a gate sets it only when the slice has none,
// and then only to the default (three quarters of physical memory), and never
// lowers or raises a set bound. The steps run in a cgroup bounded at the gate
// budget, which the gate is not in, and each step starts in its own child
// cgroup whose memory.max is the step budget and whose memory.oom.group stops
// the whole step at an out-of-memory kill, so the kernel enforces all three
// caps, and the gate reads the step's OOM events before it removes the
// cgroup. A gate started inside another gate's step opens no scope: the outer
// step's caps hold, and it watches its steps' RSS. Elsewhere (darwin, or
// where the probe fails), each step runs in its own process group and one
// sampler stops a group whose resident memory passes the step budget, and the
// largest group when the sum passes the gate budget; this fallback has no
// bound across gates, and a step can pass its budget between two samples.
// ERGO_GATE_MEMORY_MECHANISM=rss forces it. The gate prints once which
// mechanism enforces the budget. A step stopped for its budget fails with
// "memory budget exceeded (<budget>)" and its output, and its result
// is never recorded as passed. Each step's summary line reports its peak
// memory. -record-durations runs its packages under the same budget.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/internal/testinputs/testenv"
	"ergo.local/prototype/scripts/gate/replay"
)

// cacheVersion is part of every key; bump it when a step's meaning changes in
// a way its declared inputs cannot see, or when the receipt's form changes
// (v3: the output moved out of the receipt into a file beside it; v4: a
// receipt follows the links among its inputs, and the whole-gate store).
const cacheVersion = replay.Version

// Step is one gate check.
type Step struct {
	Name  string
	Needs []string
	// Argv runs in the repository root with the gate's environment.
	Argv []string
	// Inputs, when set, makes the step's pass reusable under the hash of
	// exactly these inputs. Nil means the step always runs: the tool owns its
	// cache, the step is cheaper than hashing, or it reads inputs no digest
	// here captures, such as Git history.
	Inputs *Inputs
	// ToolEnv names executables the step's own cache cannot see, such as git
	// and sh, which Go tests spawn. Their fingerprints, with the environment
	// they and the Go programs a test runs read (testenv.Names, which
	// include goRuntimeEnvironment), reach the step as one digest in
	// ERGO_TOOL_VERSIONS; a test that reads it keys Go's test cache on it.
	ToolEnv []string
	// Env sets variables ("NAME=value") for the step, over the environment
	// the gate gives it; a hermetic Go test step sets CGO_ENABLED=0.
	Env []string
	// Hermetic marks the go test step of the hermetic gate packages (below
	// hermeticTestRoot), the one exception to the gate's default: every other
	// step runs with -count=1 added to GOFLAGS (stepEnvironment), so no go
	// test it starts replays Go's test cache. A hermetic step keeps the
	// caller's GOFLAGS, so Go may replay its tests.
	// TestCachedGatePackagesAreHermetic checks each of its packages.
	Hermetic bool
}

// Inputs declares everything a cached step's result depends on.
type Inputs struct {
	// Paths selects repository files (tracked and untracked, not ignored):
	// "dir/" matches a subtree, anything else is a path.Match pattern. Empty
	// selects every file.
	Paths   []string
	Exclude []string
	// Tools names executables whose fingerprint (toolVersion) is an input.
	Tools []string
	// Env names environment variables whose values are inputs.
	Env []string
	// Installed names directories a step walks on disk itself, such as a
	// committed snapshot, whose contents are an input, files Git ignores
	// included: every file's path, permissions and content and every
	// symlink's target, following links (replay.InstallationDigest). An absent
	// directory is a distinct input. Each is digested once per run, because
	// no step writes it.
	Installed []string
	// Built names checkout files that a step in Needs writes, such as
	// bin/ergo. Each is digested as a Paths file is (fileDigest: content,
	// keyed permission bits, kind and the link rule), inside key, which runs
	// only after every step in Needs has passed, and never from a memo: a
	// digest taken earlier would key the file before its build. A missing or
	// unreadable built file is a key error, so the step fails and never
	// replays; a kind that cannot be keyed (a FIFO) runs it without its
	// receipt.
	Built []string
}

type outcome int

const (
	passed outcome = iota
	cached
	failed
	blocked
)

type result struct {
	outcome outcome
	// spool holds the step's own output, in a file on disk the gate reads
	// back to print it: an unlinked spool file, or a recorded receipt's
	// output. Nil when the step printed nothing through the gate.
	spool *os.File
	// output follows spool: the gate's own lines about the step, a few
	// lines whatever the step printed.
	output   []byte
	duration time.Duration
	cpu      time.Duration
	// peak is the most memory the step held, in bytes, or 0 if unknown.
	peak int64
	// begun is when the step started, from the start of the run, so a
	// log shows the critical path and not only each step's own time.
	begun time.Duration
	note  string
	// passedAt is when the step passed: when it finished, or when the
	// receipt it replayed was recorded. duration is then the receipt's.
	passedAt time.Time
}

// receipt is a recorded pass. The step's output is in a file beside it
// (receiptOutputPath), which a replay streams; the receipt names its size
// and digest, so a replay prints only the output recorded with it.
type receipt struct {
	Step         string    `json:"step"`
	Key          string    `json:"key"`
	Duration     float64   `json:"durationSeconds"`
	Passed       time.Time `json:"passedAt"`
	OutputBytes  int64     `json:"outputBytes"`
	OutputSHA256 string    `json:"outputSHA256"`
}

type gate struct {
	started  time.Time
	root     string
	cacheDir string
	// spoolDir holds the steps' output spools (outputSpoolDir).
	spoolDir string
	noCache  bool
	steps    []*Step
	files    []string
	digests  map[string]string
	tools    map[string]*toolFact
	toolsMu  sync.Mutex

	// roots are the paths the tool fingerprints cover (installationRoots).
	roots []string

	installedDigests map[string]string
	installedMu      sync.Mutex

	// scope is the unit of the systemd scope the gate was re-executed in,
	// or empty; scopeNote says why there is none.
	scope, scopeNote string
	limit            limiter

	// whole, when set, keys the whole gate: run replays a recorded pass at
	// the key, records a passed run and withdraws the record on a failure.
	whole *wholeGate
	// verdicts are each step's verdict after run, for the merge tier.
	verdicts map[string]replay.Verdict
	// programs replaces hostPrograms in a test of the collector.
	programs []string
}

// wholeGate is what a run needs to key, replay and record the whole gate.
type wholeGate struct {
	// mechanism is the memory mechanism fact (mechanismFact).
	mechanism string
	// identity names the checkout in a record.
	identity checkoutState
}

// derive is a gate for another run of the same checkout: the merge tier's
// inner and uncached runs share the scope, the limiter and the caches.
func (g *gate) derive(steps []*Step, noCache bool, whole *wholeGate) *gate {
	return &gate{root: g.root, cacheDir: g.cacheDir, noCache: noCache, steps: steps, scope: g.scope, scopeNote: g.scopeNote, limit: g.limit, whole: whole, programs: g.programs}
}

// collect collects the whole-gate facts afresh: every fingerprint again
// (collectHost uses a fresh fingerprint cache).
func (g *gate) collect() (*replay.Facts, error) {
	return g.collectFacts(g.whole.mechanism)
}

func main() {
	list := flag.Bool("list", false, "print the checks the gate runs, one per line, and exit")
	noCache := flag.Bool("no-cache", false, "run every step even when a pass for identical inputs is recorded")
	merge := flag.Bool("merge", false, "the merge tier: run every step uncached (every go test with -count=1) and compare each verdict with the whole-gate record of the checkout's key")
	recordDurations := flag.Bool("record-durations", false, "measure the sharded Go packages and rewrite "+durationsFile)
	flag.Parse()
	if *merge && *noCache {
		fatal(errors.New("--merge runs every step uncached already; it takes no --no-cache"))
	}
	scope, scopeNote := os.Getenv(scopeVariable), ""
	os.Unsetenv(scopeVariable)
	root, err := repositoryRoot()
	if err != nil {
		fatal(err)
	}
	// File-mode assertions in the process tests expect the conventional
	// creation mask; the gate fixes it rather than inheriting a caller's.
	setCreationMask()
	// The gate's own git commands run concurrently with steps, and git
	// status otherwise takes the index lock to refresh stat data.
	os.Setenv("GIT_OPTIONAL_LOCKS", "0")
	probed := false
	if !*list && scope == "" {
		budget, err := currentBudget()
		if err != nil {
			fatal(err)
		}
		command, reason := scopeProbe(budget)
		probed = command != ""
		if !*noCache && !*merge && !*recordDurations {
			// The replay path starts no step, so it needs no scope.
			if code, replayed := replayWholeGate(root, mechanismFact("", probed, reason), os.Stdout); replayed {
				os.Exit(code)
			}
		}
		// Returns only when the gate cannot run in its own scope.
		scopeNote = enterScope(command, reason, budget)
	}
	if *recordDurations {
		g := &gate{root: root, scope: scope, scopeNote: scopeNote}
		if err := g.recordDurations(os.Stdout); err != nil {
			fatal(err)
		}
		return
	}
	steps, err := gateSteps(root)
	if err != nil {
		fatal(err)
	}
	if *list {
		for _, step := range steps {
			fmt.Printf("%s: %s\n", step.Name, strings.Join(append(slices.Clone(step.Env), step.Argv...), " "))
		}
		return
	}
	// The first line binds the log to the checkout it checked. A dirty
	// checkout still runs, and says so.
	state, err := checkoutStateOf(root)
	if err != nil {
		fatal(err)
	}
	fmt.Println(state)
	g := &gate{root: root, cacheDir: cacheDirectory(), noCache: *noCache, steps: steps, scope: scope, scopeNote: scopeNote}
	if g.cacheDir != "" {
		g.whole = &wholeGate{mechanism: mechanismFact(scope, probed, scopeNote), identity: state}
	}
	if *merge {
		os.Exit(g.mergeTier(os.Stdout))
	}
	os.Exit(g.run(os.Stdout))
}

// replayWholeGate replays the whole-gate record of the checkout's key, if
// one replays (replay.Store.TryReplay): the identity line, then the
// recorded run. It reports whether it replayed, and the exit code. It runs
// before the gate enters its scope, and starts no step.
func replayWholeGate(root, mechanism string, out *os.File) (int, bool) {
	g := &gate{root: root, cacheDir: cacheDirectory()}
	if g.cacheDir == "" {
		return 0, false
	}
	facts, err := g.collectFacts(mechanism)
	if err != nil {
		return 0, false
	}
	spoolDir, err := outputSpoolDir(g.cacheDir)
	if err != nil {
		return 0, false
	}
	recorded, _ := replay.Open(g.cacheDir).TryReplay(facts, func() (*os.File, error) { return newOutputSpool(spoolDir) })
	if recorded == nil {
		// The gate runs, and says there why it did not replay.
		return 0, false
	}
	defer recorded.Close()
	if err := outputDestination(out); err != nil {
		return 0, false
	}
	state, err := checkoutStateOf(root)
	if err != nil {
		return 0, false
	}
	fmt.Fprintln(out, state)
	if err := recorded.Print(out); err != nil {
		fmt.Fprintf(out, "gate: print the replay: %v\n", err)
		return 2, true
	}
	return 0, true
}

// mergeTier runs the merge tier (replay.MergeTier): the inner tier first
// when the key has no record, then every step uncached, then the verdict
// table. Both runs share one limiter: a gate sets up its scope's cgroups
// once.
func (g *gate) mergeTier(out io.Writer) int {
	if g.whole == nil {
		fmt.Fprintln(out, "gate: --merge needs a gate cache directory (ERGO_GATE_CACHE, XDG_CACHE_HOME or HOME)")
		return 2
	}
	if g.limit == nil {
		budget, err := currentBudget()
		if err == nil {
			g.limit, err = g.openLimiter(budget)
		}
		if err != nil {
			fmt.Fprintln(out, "gate:", err)
			return 2
		}
		defer g.limit.close()
	}
	var uncached []*Step
	for _, step := range g.steps {
		uncached = append(uncached, uncachedStep(step))
	}
	tier := replay.MergeTier{
		Store:   replay.Open(g.cacheDir),
		Collect: g.collect,
		Inner: func() bool {
			fmt.Fprintln(out, "merge: the inner tier")
			return g.derive(g.steps, false, g.whole).run(out) == 0
		},
		Uncached: func() ([]string, map[string]replay.Verdict) {
			fmt.Fprintln(out, "merge: every step uncached")
			run := g.derive(uncached, true, nil)
			run.run(out)
			var names []string
			for _, step := range uncached {
				names = append(names, step.Name)
			}
			return names, run.verdicts
		},
	}
	return tier.Run(out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gate:", err)
	os.Exit(2)
}

func repositoryRoot() (string, error) {
	executable, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("locate repository root: %w", err)
	}
	return strings.TrimSpace(string(executable)), nil
}

// checkoutIdentity names the commit and tree the gate starts from, and
// whether the checkout matches them: no staged, unstaged or untracked
// (non-ignored) changes, submodules included.
func checkoutIdentity(root string) (string, error) {
	state, err := checkoutStateOf(root)
	if err != nil {
		return "", err
	}
	return state.String(), nil
}

// checkoutState is the commit and tree a gate starts from, and whether the
// checkout matches them.
type checkoutState struct {
	head, tree string
	clean      bool
}

func (s checkoutState) String() string {
	return fmt.Sprintf("gate: head %s tree %s clean=%t", s.head, s.tree, s.clean)
}

func checkoutStateOf(root string) (checkoutState, error) {
	git := func(args ...string) (string, error) {
		command := exec.Command("git", args...)
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return string(output), nil
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return checkoutState{}, err
	}
	tree, err := git("rev-parse", "HEAD^{tree}")
	if err != nil {
		return checkoutState{}, err
	}
	// Submodule ignore settings in .gitmodules or config cannot hide a
	// change: the reported cleanliness covers every submodule.
	status, err := git("status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil {
		return checkoutState{}, err
	}
	return checkoutState{head: strings.TrimSpace(head), tree: strings.TrimSpace(tree), clean: status == ""}, nil
}

func cacheDirectory() string {
	if dir := os.Getenv("ERGO_GATE_CACHE"); dir != "" {
		return dir
	}
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "ergo-gate")
}

// run executes the graph and returns the process exit code.
func (g *gate) run(out io.Writer) int {
	started := time.Now()
	g.started = started
	// The gate prints every step's output to out. A file in memory there
	// would charge that output to the gate, so the gate refuses it before
	// any step runs.
	if file, ok := out.(*os.File); ok {
		if err := outputDestination(file); err != nil {
			fmt.Fprintf(out, "gate: standard output %v; redirect standard output to a disk file or a pipe\n", err)
			return 2
		}
	}
	index := map[string]int{}
	for i, step := range g.steps {
		if _, duplicate := index[step.Name]; duplicate {
			fmt.Fprintf(out, "gate: duplicate step %q\n", step.Name)
			return 2
		}
		index[step.Name] = i
	}
	for _, step := range g.steps {
		for _, need := range step.Needs {
			if _, ok := index[need]; !ok {
				fmt.Fprintf(out, "gate: step %q needs unknown step %q\n", step.Name, need)
				return 2
			}
		}
	}
	// Start every tool fingerprint while files load; each is computed once
	// per run, so a step that names a tool finds it ready or waits for it.
	// The prefetches are joined before run returns, so no fingerprint work
	// outlives the run that started it.
	var prefetch sync.WaitGroup
	defer prefetch.Wait()
	prefetched := map[string]bool{}
	for _, step := range g.steps {
		tools := step.ToolEnv
		if step.Inputs != nil {
			tools = append(slices.Clone(step.Inputs.Tools), tools...)
		}
		for _, tool := range tools {
			if !prefetched[tool] {
				prefetched[tool] = true
				prefetch.Go(func() { g.toolVersion(tool) })
			}
		}
	}
	if err := g.loadFiles(); err != nil {
		fmt.Fprintln(out, "gate:", err)
		return 2
	}
	fmt.Fprintf(out, "gate: steps receive only %s\n", strings.Join(testenv.Names, " "))
	if g.limit == nil {
		budget, err := currentBudget()
		if err == nil {
			g.limit, err = g.openLimiter(budget)
		}
		if err != nil {
			fmt.Fprintln(out, "gate:", err)
			return 2
		}
		defer g.limit.close()
	}
	fmt.Fprintf(out, "gate: memory budget: %s\n", g.limit.describe())
	var err error
	if g.spoolDir, err = outputSpoolDir(g.cacheDir); err != nil {
		fmt.Fprintln(out, "gate:", err)
		return 2
	}
	// The start key, after the limiter (which may bound the gates' slice, a
	// host fact) and before any step.
	var recording *replay.Run
	if g.whole != nil {
		store := replay.Open(g.cacheDir)
		facts, err := g.collect()
		switch {
		case err != nil:
			fmt.Fprintf(out, "gate: no whole-gate key: %v\n", err)
		case !g.noCache:
			if recorded, reason := store.TryReplay(facts, g.spool); recorded != nil {
				defer recorded.Close()
				if err := recorded.Print(out); err != nil {
					fmt.Fprintf(out, "gate: print the replay: %v\n", err)
					return 2
				}
				g.verdicts = map[string]replay.Verdict{}
				for _, step := range recorded.Record.Steps {
					g.verdicts[step.Name] = replay.Pass
				}
				return 0
			} else {
				fmt.Fprintln(out, "gate: "+reason)
			}
			fallthrough
		default:
			recording = store.Begin(facts)
		}
	}

	results := make([]*result, len(g.steps))
	done := make([]chan struct{}, len(g.steps))
	for i := range done {
		done[i] = make(chan struct{})
	}
	slots := make(chan struct{}, max(2, runtime.NumCPU()))
	for i, step := range g.steps {
		go func() {
			defer close(done[i])
			for _, need := range step.Needs {
				j := index[need]
				<-done[j]
				if results[j].outcome == failed || results[j].outcome == blocked {
					results[i] = &result{outcome: blocked, note: "needs " + need}
					return
				}
			}
			slots <- struct{}{}
			results[i] = g.execute(step)
			<-slots
		}()
	}

	var failures []string
	g.verdicts = map[string]replay.Verdict{}
	for i, step := range g.steps {
		<-done[i]
		r := results[i]
		switch r.outcome {
		case passed:
			fmt.Fprintf(out, "== %s: ok (%s)\n", step.Name, r.measures())
			g.verdicts[step.Name] = replay.Pass
		case cached:
			fmt.Fprintf(out, "== %s: ok, cached (%s)\n", step.Name, r.note)
			g.verdicts[step.Name] = replay.Pass
		case failed:
			fmt.Fprintf(out, "== %s: FAILED (%s)\n", step.Name, r.measures())
			failures = append(failures, step.Name)
			g.verdicts[step.Name] = replay.Failed
		case blocked:
			fmt.Fprintf(out, "== %s: not run (%s)\n", step.Name, r.note)
			failures = append(failures, step.Name)
			g.verdicts[step.Name] = replay.NotRun
		}
		// The whole-gate record keeps what is printed for the step, in an
		// object of the store.
		destination := out
		var object *replay.Object
		if recording != nil {
			var err error
			if object, err = recording.Output(); err == nil {
				if err := outputDestination(object.File()); err != nil {
					recording.Refuse(fmt.Sprintf("store output in %s %v; set ERGO_GATE_CACHE to a directory on disk", filepath.Dir(object.File().Name()), err))
				}
				destination = io.MultiWriter(out, object)
			}
		}
		if err := r.print(destination); err != nil {
			fmt.Fprintf(out, "gate: %s: print output: %v\n", step.Name, err)
			if !slices.Contains(failures, step.Name) {
				failures = append(failures, step.Name)
			}
			g.verdicts[step.Name] = replay.Failed
		} else if recording != nil && (r.outcome == passed || r.outcome == cached) {
			outcome := "ran"
			if r.outcome == cached {
				outcome = "cached"
			}
			recording.Passed(step.Name, object, outcome, r.duration.Seconds(), r.passedAt)
		}
	}
	if recording != nil {
		identity := g.whole.identity
		line := recording.Finish(len(failures) == 0, g.collect, replay.Meta{Head: identity.head, Tree: identity.tree, Clean: identity.clean, Now: time.Now()})
		if line != "" {
			fmt.Fprintln(out, line)
		}
	}
	elapsed := time.Since(started).Seconds()
	if len(failures) > 0 {
		fmt.Fprintf(out, "gate: FAILED in %.1fs: %s\n", elapsed, strings.Join(failures, ", "))
		return 1
	}
	fmt.Fprintf(out, "gate: passed %d steps in %.1fs\n", len(g.steps), elapsed)
	return 0
}

// spool is a new unlinked output spool in the gate's spool directory.
func (g *gate) spool() (*os.File, error) { return newOutputSpool(g.spoolDir) }

func (g *gate) execute(step *Step) *result {
	key := ""
	// notCached says why a step with declared inputs runs without its
	// receipt: an input that cannot be keyed.
	notCached := ""
	if step.Inputs != nil && g.cacheDir != "" {
		var err error
		key, err = g.key(step)
		var unkeyed unkeyedInput
		switch {
		case errors.As(err, &unkeyed):
			key, notCached = "", fmt.Sprintf("gate: %s: not cached: %s\n", step.Name, string(unkeyed))
		case err != nil:
			return &result{outcome: failed, output: []byte("gate: hash inputs: " + err.Error() + "\n")}
		}
		if key != "" && !g.noCache {
			if recorded, output, ok := g.lookup(key); ok {
				return &result{outcome: cached, spool: output, duration: time.Duration(recorded.Duration * float64(time.Second)), passedAt: recorded.Passed,
					note: fmt.Sprintf("passed in %.1fs at %s, inputs %s", recorded.Duration, recorded.Passed.UTC().Format(time.RFC3339), key[:12])}
			}
		}
	}
	tmpDir := stepTempDir(g.root, step.Name)
	if err := os.MkdirAll(filepath.Dir(tmpDir), 0o700); err != nil {
		return &result{outcome: failed, output: []byte(fmt.Sprintf("gate: %s: create step temp dir: %v\n", step.Name, err))}
	}
	lock, err := lockStepTemp(tmpDir + ".lock")
	if err != nil {
		return &result{outcome: failed, output: []byte(fmt.Sprintf("gate: %s: lock step temp dir: %v\n", step.Name, err))}
	}
	if err := emptyStepTempDir(tmpDir); err != nil {
		_ = unlockStepTemp(lock)
		return &result{outcome: failed, output: []byte(fmt.Sprintf("gate: %s: prepare step temp dir: %v\n", step.Name, err))}
	}
	started := time.Now()
	command := exec.CommandContext(context.Background(), step.Argv[0], step.Argv[1:]...)
	command.Dir = g.root
	command.Env = g.commandEnvironment(step)
	// The step's output goes to a spool file, never to the gate's memory, so
	// what the gate holds for a step does not grow with what it prints.
	spool, err := newOutputSpool(g.spoolDir)
	if err != nil {
		_ = unlockStepTemp(lock)
		return &result{outcome: failed, output: []byte(fmt.Sprintf("gate: %s: %v\n", step.Name, err))}
	}
	command.Stdout = spool
	command.Stderr = spool
	used, err := runBudgeted(g.limit, step.Name, command)
	r := &result{outcome: passed, spool: spool, duration: time.Since(started), begun: started.Sub(g.started), peak: used.peak, passedAt: time.Now(), output: []byte(notCached)}
	if command.ProcessState != nil {
		// A reaped child's usage includes the descendants it reaped.
		r.cpu = command.ProcessState.UserTime() + command.ProcessState.SystemTime()
	}
	if err != nil || used.verdict != "" || used.processIssue != "" {
		r.outcome = failed
		if err != nil {
			r.output = fmt.Appendf(r.output, "gate: %s: %v\n", step.Name, err)
		}
		// A step over its budget fails, and is never recorded, whatever its
		// exit status.
		if used.verdict != "" {
			r.output = fmt.Appendf(r.output, "gate: %s: %s\n", step.Name, used.verdict)
		}
		if used.processIssue != "" {
			r.output = fmt.Appendf(r.output, "gate: %s: %s\n", step.Name, used.processIssue)
		}
	}
	cleanupErr := emptyStepTempDir(tmpDir)
	unlockErr := unlockStepTemp(lock)
	if cleanupErr != nil {
		r.outcome = failed
		r.output = append(r.output, []byte(fmt.Sprintf("gate: %s: empty step temp dir: %v\n", step.Name, cleanupErr))...)
	}
	if unlockErr != nil {
		r.outcome = failed
		r.output = append(r.output, []byte(fmt.Sprintf("gate: %s: unlock step temp dir: %v\n", step.Name, unlockErr))...)
		return r
	}
	if r.outcome == passed && key != "" {
		// A pass not recorded still passes, with its output whole; the
		// next run executes the step again.
		if err := g.record(receipt{Step: step.Name, Key: key, Duration: r.duration.Seconds(), Passed: time.Now()}, spool); err != nil {
			r.output = fmt.Appendf(r.output, "gate: %s: pass not recorded: %v\n", step.Name, err)
		}
	}
	return r
}

// print writes the step's output, then the gate's lines about it, ending
// the step's text with a newline, and closes the spool. Output is streamed
// through a fixed buffer. An error means the output was not printed whole.
func (r *result) print(out io.Writer) error {
	tail := &lastByteWriter{writer: out}
	var err error
	if r.spool != nil {
		if _, err = r.spool.Seek(0, io.SeekStart); err == nil {
			_, err = io.CopyBuffer(tail, onlyReader{r.spool}, make([]byte, 32<<10))
		}
		if closeErr := r.spool.Close(); err == nil {
			err = closeErr
		}
		r.spool = nil
	}
	if _, writeErr := tail.Write(r.output); err == nil {
		err = writeErr
	}
	if tail.written > 0 && tail.last != '\n' {
		if _, writeErr := io.WriteString(tail, "\n"); err == nil {
			err = writeErr
		}
	}
	return err
}

// onlyReader hides a file's WriterTo, so io.CopyBuffer uses its buffer.
type onlyReader struct{ io.Reader }

// lastByteWriter remembers the last byte written and how many.
type lastByteWriter struct {
	writer  io.Writer
	written int64
	last    byte
}

func (w *lastByteWriter) Write(content []byte) (int, error) {
	n, err := w.writer.Write(content)
	if n > 0 {
		w.written += int64(n)
		w.last = content[n-1]
	}
	return n, err
}

// measures are a run step's wall time, CPU time, start and peak memory.
func (r *result) measures() string {
	text := fmt.Sprintf("%.1fs, cpu %.1fs, from %.1fs", r.duration.Seconds(), r.cpu.Seconds(), r.begun.Seconds())
	if r.peak > 0 {
		text += ", peak " + formatBytes(r.peak)
	}
	return text
}

// gcPercent is the Go collector target for every step's Go processes (tests,
// the toolchain and the CLI) unless the caller sets GOGC. Measured on the
// slowest compiler test, 400 instead of the default 100 cut CPU by 42% for 6%
// more peak memory; no test depends on collection frequency.
const gcPercent = "400"

// toolDigest identifies, for a step's own cache, the tools it names in
// ToolEnv and the whole environment a Go test step receives (testenv.Names,
// whose values the tools and the Go programs a test runs may read whether
// or not they were set when a result was cached).
func (g *gate) toolDigest(step *Step) string {
	digest := sha256.New()
	for _, tool := range step.ToolEnv {
		fmt.Fprintf(digest, "%s\x00%s\x00", tool, g.toolVersion(tool))
	}
	for _, name := range testenv.Names {
		if name == testenv.ToolDigest {
			continue
		}
		value, set := stepVariableFor(g.root, step, name)
		fmt.Fprintf(digest, "env\x00%s\x00%t\x00%s\x00", name, set, value)
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

// keyedEnvironment is the environment the whole-gate key holds
// (declaredEnvironment): the caller's variables that testenv names, and no
// other, as Bazel's strict action environment passes only declared
// variables, with the gate's collector target and the soft memory limit
// below the step budget (stepSoftLimit) unless the caller sets them. A
// variable outside the list cannot reach a step, so the key, which holds
// each listed variable set or not, covers all a step can read from its
// environment.
//
// The tool digest (testenv.ToolDigest) is the one listed variable the gate
// never takes from its caller: it derives the value from key inputs (the
// tool fingerprints and the other listed variables) for a step that names
// ToolEnv (commandEnvironment), and no other step or tool query receives
// it. So every variable a step receives is a key input or derived from key
// inputs the same way on every run.
func keyedEnvironment() []string {
	environment := slices.DeleteFunc(testenv.Environment(os.Environ()), func(entry string) bool {
		return strings.HasPrefix(entry, testenv.ToolDigest+"=")
	})
	if _, set := os.LookupEnv("GOGC"); !set {
		environment = append(environment, "GOGC="+gcPercent)
	}
	if _, set := os.LookupEnv("GOMEMLIMIT"); !set {
		environment = append(environment, "GOMEMLIMIT="+stepSoftLimit())
	}
	return environment
}

// uncachedTestFlag is what the gate adds to the caller's GOFLAGS for every
// step but a hermetic one (Step.Hermetic). Go's test cache replays a result
// only when every test flag is cacheable, and -count is not, so no go test
// that a step starts replays a result: one in the step's argv, one a shell
// script runs, one a program runs (go run, go test -exec, -toolexec,
// -vettool), at any depth, as long as the process inherits its environment
// (gate-testcache-gap probe-r4.log). Go applies a GOFLAGS flag only to the
// go commands that know it, so go build, go vet, go run and go list are
// unchanged. A step that sets GOFLAGS or -count itself would undo this; the
// policy test refuses both by name.
const uncachedTestFlag = "-count=1"

// stepEnvironment is the environment every step runs with, a hermetic one
// excepted (stepEnvironmentFor), and every tool query: keyedEnvironment with
// uncachedTestFlag after the caller's GOFLAGS. It is added, not set, so the
// caller's flags still apply, and it comes last, so it wins over a -count
// the caller gives. The flag follows from the gate's code, which the
// whole-gate key holds, so the key stays the caller's (declaredEnvironment).
// A step's own variables (Step.Env) come over it.
func stepEnvironment() []string {
	environment := keyedEnvironment()
	flags, _ := environmentVariable(environment, "GOFLAGS")
	return setEnvironmentVariable(environment, "GOFLAGS", strings.TrimSpace(flags+" "+uncachedTestFlag))
}

// stepTempDir is stable for the same checkout and step, so Go's test cache
// can replay tests that read TMPDIR. The gate empties the directory after the
// step's processes have stopped.
func stepTempDir(root, name string) string {
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	key := sha256.Sum256([]byte(filepath.Clean(root) + "\x00" + name))
	return filepath.Join(os.TempDir(), "ergo-gate", "tmp", hex.EncodeToString(key[:]))
}

// emptyStepTempDir restores directory permissions before removing contents,
// then leaves the stable root in place for the next run of this step.
func emptyStepTempDir(name string) error {
	if info, err := os.Lstat(name); err == nil && !info.IsDir() {
		if err := os.Remove(name); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(name, 0o700); err != nil {
		return err
	}
	if err := filepath.WalkDir(name, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode().Perm()|0o700)
	}); err != nil {
		return err
	}
	entries, err := os.ReadDir(name)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(name, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// stepEnvironmentFor is the environment of one step, before its own
// variables: stepEnvironment, or keyedEnvironment for a hermetic step (the
// one exception to the uncached default), with the step's TMPDIR.
func stepEnvironmentFor(root string, step *Step) []string {
	environment := stepEnvironment()
	if step.Hermetic {
		environment = keyedEnvironment()
	}
	return setEnvironmentVariable(environment, "TMPDIR", stepTempDir(root, step.Name))
}

// stepCommandEnvironment is the environment a step's command runs with,
// before the tool digest: the step environment with the step's TMPDIR and
// its own variables over it.
func stepCommandEnvironment(root string, step *Step) []string {
	environment := stepEnvironmentFor(root, step)
	for _, entry := range step.Env {
		name, value, _ := strings.Cut(entry, "=")
		environment = setEnvironmentVariable(environment, name, value)
	}
	return environment
}

// commandEnvironment is all that a step's command receives: its step
// environment, and the tool digest when the step names ToolEnv.
func (g *gate) commandEnvironment(step *Step) []string {
	environment := stepCommandEnvironment(g.root, step)
	if len(step.ToolEnv) > 0 {
		environment = append(environment, testenv.ToolDigest+"="+g.toolDigest(step))
	}
	return environment
}

func setEnvironmentVariable(environment []string, name, value string) []string {
	updated := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, name+"=") {
			updated = append(updated, entry)
		}
	}
	return append(updated, name+"="+value)
}

func environmentVariable(environment []string, name string) (string, bool) {
	for i := len(environment) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(environment[i], name+"="); ok {
			return value, true
		}
	}
	return "", false
}

// runsGoTest reports whether a step runs go test, which runs test binaries
// whose results Go's test cache replays.
func runsGoTest(step *Step) bool {
	return len(step.Argv) >= 2 && step.Argv[0] == "go" && step.Argv[1] == "test"
}

// goTestEnvironment is the environment of a step that runs go test, as of
// every step: only the variables testenv allows, so a variable the gate
// does not key cannot reach a cached test. Each step gets its own stable
// TMPDIR, and the tool digest keys that path along with the rest of its
// environment.
func goTestEnvironment() []string {
	return stepEnvironment()
}

// stepVariable reads a variable as steps see it, so a key names the value a
// step runs with rather than the caller's.
func stepVariable(name string) (string, bool) {
	return environmentVariable(stepEnvironment(), name)
}

func stepVariableFor(root string, step *Step, name string) (string, bool) {
	return environmentVariable(stepEnvironmentFor(root, step), name)
}

// loadFiles hashes every tracked and untracked (non-ignored) file once.
func (g *gate) loadFiles() error {
	listing, err := g.git("ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	g.digests = map[string]string{}
	for _, name := range strings.Split(string(listing), "\x00") {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		g.files = append(g.files, name)
	}
	sort.Strings(g.files)
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan string)
	for range max(2, runtime.NumCPU()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range work {
				digest := fileDigest(g.root, name)
				mu.Lock()
				g.digests[name] = digest
				mu.Unlock()
			}
		}()
	}
	for _, name := range g.files {
		work <- name
	}
	close(work)
	wg.Wait()
	return nil
}

// keyedPermissions are the permission bits that can change a step's result.
// Steps run as the user who owns the checkout, so the kernel grants their
// access from the owner bits alone: owner-read decides whether a step can
// read an input. A cached step never writes its inputs, so owner-write cannot
// change what it reads. The three executable bits are data a check can read,
// as the capture check reads the owner's, and they decide what a step can
// execute. Group and other read and write bits are consulted for no process
// a step starts and asserted by no check, so keying them would only split
// receipts between checkouts made under different umasks.
const keyedPermissions os.FileMode = replay.KeyedPermissions

// unreplayable prefixes the digest of an input that cannot be keyed.
const unreplayable = "unreplayable\x00"

// unkeyedInput says why a step's inputs cannot key its receipt.
type unkeyedInput string

func (u unkeyedInput) Error() string { return string(u) }

// fileDigest identifies a checkout file's content, kind and keyed
// permission bits. Missing tracked files, directories (submodule gitlinks)
// and symlinks hash distinctly from content. A symlink keys its target text
// and the content it reaches (replay.Links): a file in the checkout by its
// digest, a missing name by its absence. A link that reaches a directory,
// or anywhere outside the checkout, cannot be keyed: its digest says so,
// and the step that selects it runs without its receipt. Every file passes
// replay.KindOf, the rule of the whole-gate key: a FIFO, a socket or a
// device, at the path or where its link leads, cannot be keyed either.
func fileDigest(root, relative string) string {
	name := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil {
		return "error:" + err.Error()
	}
	display := relative
	if display == "" {
		display = name
	}
	kind, unkeyable := replay.KindOf(display, info.Mode())
	switch kind {
	case replay.Other:
		return unreplayable + unkeyable.Error()
	case replay.Symlink:
		target, err := os.Readlink(name)
		if err != nil {
			return "error:" + err.Error()
		}
		links, err := replay.NewLinks(root, nil)
		if err != nil {
			return unreplayable + err.Error()
		}
		reach, problem := links.Follow(relative)
		if problem != "" {
			return unreplayable + problem
		}
		resolved := links.Resolved(relative)
		if resolved == "" {
			return "symlink:" + target + "\x00" + reach
		}
		if info, err := os.Stat(resolved); err == nil && info.IsDir() {
			return unreplayable + fmt.Sprintf("%s reaches directory %s, which a step key does not cover", relative, strings.TrimPrefix(reach, "checkout "))
		}
		return "symlink:" + target + "\x00" + reach + "\x00" + fileDigest(resolved, "")
	case replay.Directory:
		return "directory"
	}
	content, err := os.ReadFile(name)
	if err != nil {
		return "error:" + err.Error()
	}
	sum := sha256.Sum256(content)
	return fmt.Sprintf("file:%v:%s", info.Mode().Perm()&keyedPermissions, hex.EncodeToString(sum[:]))
}

func selected(name string, inputs *Inputs) bool {
	match := func(patterns []string) bool {
		for _, pattern := range patterns {
			if strings.HasSuffix(pattern, "/") {
				if strings.HasPrefix(name, pattern) {
					return true
				}
				continue
			}
			if ok, _ := path.Match(pattern, name); ok {
				return true
			}
		}
		return false
	}
	if len(inputs.Paths) > 0 && !match(inputs.Paths) {
		return false
	}
	return !match(inputs.Exclude)
}

func (g *gate) key(step *Step) (string, error) {
	inputs := step.Inputs
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%q\x00", cacheVersion, step.Name, step.Argv)
	if len(step.Env) > 0 {
		fmt.Fprintf(hash, "step env\x00%q\x00", step.Env)
	}
	files := 0
	for _, name := range g.files {
		if selected(name, inputs) {
			digest := g.digests[name]
			if problem, ok := strings.CutPrefix(digest, unreplayable); ok {
				return "", unkeyedInput(problem)
			}
			fmt.Fprintf(hash, "file\x00%s\x00%s\x00", name, digest)
			files++
		}
	}
	if files == 0 {
		return "", fmt.Errorf("%s: declared inputs select no files", step.Name)
	}
	for _, tool := range inputs.Tools {
		fmt.Fprintf(hash, "tool\x00%s\x00%s\x00", tool, g.toolVersion(tool))
	}
	for _, name := range inputs.Env {
		// A step receives only the variables testenv names (stepEnvironment),
		// so an input variable outside them is a declaration no step can
		// read: refuse it rather than key a value the step never sees.
		if !slices.Contains(testenv.Names, name) {
			return "", fmt.Errorf("%s: declares the input variable %s, which no step receives (testenv.Names)", step.Name, name)
		}
		value, set := stepVariableFor(g.root, step, name)
		fmt.Fprintf(hash, "env\x00%s\x00%t\x00%s\x00", name, set, value)
	}
	for _, dir := range inputs.Installed {
		fmt.Fprintf(hash, "installed\x00%s\x00%s\x00", dir, g.installed(dir))
	}
	// A built file is read here and nowhere earlier: execute keys a step only
	// after its Needs passed, so the digest is of the file they left.
	for _, name := range inputs.Built {
		digest := fileDigest(g.root, name)
		if problem, ok := strings.CutPrefix(digest, unreplayable); ok {
			return "", unkeyedInput(problem)
		}
		// A file, or a link that reaches one; missing, unreadable, a
		// directory or a dangling link fails the step.
		if !strings.HasPrefix(digest, "file:") && !strings.Contains(digest, "\x00file:") {
			return "", fmt.Errorf("%s: built file %s is not a readable file: %q", step.Name, name, digest)
		}
		fmt.Fprintf(hash, "built\x00%s\x00%s\x00", name, digest)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// installed digests an installed tree once per gate run.
func (g *gate) installed(dir string) string {
	g.installedMu.Lock()
	defer g.installedMu.Unlock()
	if g.installedDigests == nil {
		g.installedDigests = map[string]string{}
	}
	if digest, ok := g.installedDigests[dir]; ok {
		return digest
	}
	digest, err := replay.InstallationDigest(filepath.Join(g.root, filepath.FromSlash(dir)), g.root)
	if err != nil {
		// An unreadable tree never matches a recorded pass.
		digest = fmt.Sprintf("error:%v:%d", err, time.Now().UnixNano())
	}
	g.installedDigests[dir] = digest
	return digest
}

func (g *gate) git(args ...string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = g.root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func (g *gate) receiptPath(key string) string {
	return filepath.Join(g.cacheDir, cacheVersion, key[:2], key+".json")
}

// receiptOutputPath is the file that holds a receipt's output.
func (g *gate) receiptOutputPath(key string) string {
	return strings.TrimSuffix(g.receiptPath(key), ".json") + ".out"
}

// maxReceiptSize bounds what lookup reads of a receipt, which names its
// output's size and digest but never holds the output.
const maxReceiptSize = 64 << 10

// lookup returns the pass recorded for key and its output, open for print,
// when the output file still has the size and digest the receipt names.
// It reads the output through a fixed buffer.
func (g *gate) lookup(key string) (receipt, *os.File, bool) {
	var recorded receipt
	file, err := os.Open(g.receiptPath(key))
	if err != nil {
		return receipt{}, nil, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReceiptSize+1))
	_ = file.Close()
	if err != nil || len(data) > maxReceiptSize || json.Unmarshal(data, &recorded) != nil || recorded.Key != key {
		return receipt{}, nil, false
	}
	output, err := os.Open(g.receiptOutputPath(key))
	if err != nil {
		return receipt{}, nil, false
	}
	digest := sha256.New()
	size, err := io.CopyBuffer(digest, onlyReader{output}, make([]byte, 32<<10))
	if err != nil || size != recorded.OutputBytes || hex.EncodeToString(digest.Sum(nil)) != recorded.OutputSHA256 {
		_ = output.Close()
		return receipt{}, nil, false
	}
	return recorded, output, true
}

// record stores a pass and the output in spool atomically, the output
// first; a lost write only costs a rerun. It copies through a fixed buffer.
// It declines, before it copies a byte, when outputDestination refuses the
// output file: the cache would then hold step output in memory charged to
// the gate. An error says why the pass is not recorded.
func (g *gate) record(r receipt, spool *os.File) error {
	name := g.receiptPath(r.Key)
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	output, err := os.CreateTemp(dir, ".output-")
	if err != nil {
		return err
	}
	if err := outputDestination(output); err != nil {
		_ = output.Close()
		_ = os.Remove(output.Name())
		return fmt.Errorf("cache output file in %s %w; set ERGO_GATE_CACHE to a directory on disk", dir, err)
	}
	digest := sha256.New()
	_, seekErr := spool.Seek(0, io.SeekStart)
	size, copyErr := io.CopyBuffer(io.MultiWriter(output, digest), onlyReader{spool}, make([]byte, 32<<10))
	closeErr := output.Close()
	if err := errors.Join(seekErr, copyErr, closeErr); err != nil {
		_ = os.Remove(output.Name())
		return fmt.Errorf("write cache output: %w", err)
	}
	if err := os.Rename(output.Name(), g.receiptOutputPath(r.Key)); err != nil {
		_ = os.Remove(output.Name())
		return err
	}
	r.OutputBytes, r.OutputSHA256 = size, hex.EncodeToString(digest.Sum(nil))
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".receipt-")
	if err != nil {
		return err
	}
	_, writeErr := temporary.Write(data)
	closeErr = temporary.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(temporary.Name())
		return fmt.Errorf("write receipt: %w", err)
	}
	if err := os.Rename(temporary.Name(), name); err != nil {
		_ = os.Remove(temporary.Name())
		return err
	}
	return nil
}

var testFunction = regexp.MustCompile(`(?m)^func ((?:Test|Fuzz|Example)[A-Za-z0-9_]*)\(`)

// testNames lists a package directory's top-level test, fuzz and example
// functions. It only balances shards: completeness never depends on it.
func testNames(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, match := range testFunction.FindAllSubmatch(content, -1) {
			name := string(match[1])
			if name == "TestMain" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// shard partitions names into n groups by longest-processing-time-first over
// recorded durations. Unknown tests weigh the median, so a new test does not
// reshuffle the others.
func shard(names []string, durations map[string]float64, n int) [][]string {
	known := make([]float64, 0, len(durations))
	for _, name := range names {
		if d, ok := durations[name]; ok {
			known = append(known, d)
		}
	}
	sort.Float64s(known)
	fallback := 0.1
	if len(known) > 0 {
		fallback = known[len(known)/2]
	}
	weight := func(name string) float64 {
		if d, ok := durations[name]; ok {
			return d
		}
		return fallback
	}
	ordered := append([]string(nil), names...)
	sort.SliceStable(ordered, func(i, j int) bool {
		wi, wj := weight(ordered[i]), weight(ordered[j])
		if wi != wj {
			return wi > wj
		}
		return ordered[i] < ordered[j]
	})
	groups := make([][]string, n)
	loads := make([]float64, n)
	for _, name := range ordered {
		lightest := 0
		for i := 1; i < n; i++ {
			if loads[i] < loads[lightest] {
				lightest = i
			}
		}
		groups[lightest] = append(groups[lightest], name)
		loads[lightest] += weight(name)
	}
	for _, group := range groups {
		sort.Strings(group)
	}
	return groups
}

func exactNames(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}

// shardArgs returns the go test selection flags for each shard. Shards before
// the last run exactly their names; the last skips exactly those names, so
// every test the package defines runs in exactly one shard even if testNames
// missed it.
func shardArgs(groups [][]string) [][]string {
	var args [][]string
	var assigned []string
	for i, group := range groups {
		if i == len(groups)-1 {
			if len(assigned) == 0 {
				args = append(args, nil)
			} else {
				args = append(args, []string{"-skip", exactNames(assigned)})
			}
			break
		}
		if len(group) == 0 {
			continue
		}
		args = append(args, []string{"-run", exactNames(group)})
		assigned = append(assigned, group...)
	}
	return args
}
