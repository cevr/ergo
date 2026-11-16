package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
)

// testCommand runs a source's tests: `ergo test FILE [--seed N] [--states N]
// [--law NAME] [--skip-law NAME] [--run NAME] [--receipt FILE]
// [--explore [dfs|pct|random] [--preempt B] [--depth D] [--schedules S]]
// [--schedule TOKEN] [-work] [--debug] [--target go]`. --states is the
// budget of states of each reachability law's search (machine laws;
// 1,000,000 by default). --law runs only the law NAME and no test;
// --skip-law runs all but that law, with a skip line. --run runs the one
// test or law of that name (a law by the title its report prints), plain,
// explored or replayed; a name the package does not have is a usage error
// of the test binary (exit 2). --explore links the same test
// module with the ergoexplore tag, so its tasks run on one baton under the
// cooperative schedule, and a deadlock is a defect that names the tasks
// (runtime.md, Explored schedules). With a mode, the tests run under many
// schedules in worker processes of the one binary, and a failure prints
// its schedule (explore.go); --schedule replays one. It is a transport over sketchfront's test build
// (build.ModeTest, lower.LowerTests): it checks the source, builds the test
// binary into a private temporary directory, never the working directory,
// dist/ or the module, and runs it as a child with --seed, through the
// same child path as run (runEntryProcess), so SIGINT gives 130 and
// SIGTERM 143. Its exit status is the runner's: 0 when every test passes,
// 1 when one fails, 2 when a defect stops the run. An invalid invocation, a
// malformed --seed included, is a usage error (exit 2) that runs nothing
// and prints no report line; a check, lowering or Go build failure prints
// its diagnostics and exits 1 with no PASS line. -work prints WORK=DIR on
// stderr, DIR being the test module in the emission cache. A passing run
// is cached by everything it reads, and replayed with no link and no run
// (testcache.go); --count 1 always runs. A package with nothing to test is
// never built (empty.go).
func testCommand(args []string) error {
	opts, err := parseFileOptions("test", args)
	if err != nil {
		return invalidInvocation(err)
	}
	if recursivePattern(opts.file) {
		if opts.emitted {
			// One process for every package: the std packages they
			// import are checked once, not once per package (the
			// examples runner's key, gate-speed GS27).
			return recursiveCommand(opts, nil, func(one fileOptions) error { return printEmitted(one, build.ModeTest) })
		}
		return recursiveTestCommand(opts)
	}
	if opts.emitted {
		return printEmitted(opts, build.ModeTest)
	}
	return testPackage(opts)
}

// testPackage tests one package. An unchanged package folder whose last
// run passed is a warm answer (warm.go; compile-speed CS8): its stored
// result is replayed with no check, no emission, no link and no process.
// The request's key adds the law ledger a full run writes.
func testPackage(opts fileOptions) error {
	var w *warm
	if testCacheable(opts) {
		w = startWarm(opts, warmToolchain(opts), ledgerPart(ledgerPath(opts.file)))
	}
	if w != nil && w.hit {
		return replayTestResult(w.answer.Test, os.Stdout, os.Stderr)
	}
	l, err := loadThrough(w.fsys(), opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	if !l.result.Checked {
		return refuseUnchecked(l, opts)
	}
	return testLoadedPackage(opts, l, func(result []byte) {
		if result != nil {
			w.recordAs(sketchfront.WarmAnswer{Test: result}, ledgerPart(ledgerPath(opts.file)))
		}
	})
}

// testLoadedPackage tests a checked package. keep, when not nil, is given
// the stored result of a passing run whose build printed nothing (nil for
// a package with nothing to test), to be kept as a warm answer.
func testLoadedPackage(opts fileOptions, l loaded, keep func(result []byte)) error {
	if keep == nil {
		keep = func([]byte) {}
	}
	// A package with nothing to test is neither built nor run, so its
	// initializers never run (empty.go); a schedule search still builds it.
	if opts.search.mode == "" && nothingToTest(l.analysis.Root().Package) {
		err := emptyTestRun(opts, l.result.Revision, os.Stdout, os.Stderr)
		if err == nil && opts.law == "" && opts.skipLaw == "" && opts.run == "" && !opts.explore {
			err = writeLedger(opts, l)
		}
		if err == nil {
			keep(nil)
		}
		return err
	}
	dir, err := os.MkdirTemp("", "ergo-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, l.name+".test")
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	tc, err := buildAction(opts)
	if err != nil {
		return err
	}
	if opts.explore {
		// The tag chooses the runtime's controlled scheduler at link
		// time; the emitted module is the one a plain `ergo test` uses
		// (runtime.md, Explored schedules).
		tc.Tags = []string{"ergoexplore"}
	}
	childArgs, err := testChildArgs(opts, l.result.Revision)
	if err != nil {
		return err
	}
	// The test binary runs under a memory limit, 2 GiB unless
	// ERGO_PROGRAM_MEMORY sets another, and its stderr passes through a
	// watcher, so a test that grows without bound stops early and `ergo`
	// says which limit stopped it (docs/tooling.md, "Test memory").
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		return err
	}
	// A passing run's output is cached under the key of everything it
	// read (testcache.go); a warm run that finds it neither links nor runs
	// the binary and replays the output.
	cache := build.Cache{Root: root}
	ledger := ledgerPath(opts.file)
	key := testResultKey(opts, tc, childArgs, limit, ledger)
	var cached []byte
	hit := false
	// A build that printed a note is not answered warm, since the answer
	// would not print it.
	buildStderr := &quiet{w: os.Stderr}
	// The direct path links nothing for an unchanged module (its action
	// store holds the executable, build/direct.go); a cached passing
	// result skips even that.
	module, err := sketchfront.Build(l.analysis, sketchfront.BuildRequest{
		Mode:   build.ModeTest,
		Source: l.source.Admitted(),
		Output: binary,
		Reuse: func(m build.Module) bool {
			if key == "" {
				return false
			}
			data, ok := cache.Result(m, key)
			cached, hit = data, ok
			return ok
		},
		Cache:     cache,
		Toolchain: tc,
		IR:        true,
		Stderr:    buildStderr,
	})
	if opts.work && module.Dir != "" {
		fmt.Fprintf(os.Stderr, "WORK=%s\n", module.Dir)
	}
	if err != nil {
		return err
	}
	if hit {
		if !buildStderr.written {
			keep(cached)
		}
		return replayTestResult(cached, os.Stdout, os.Stderr)
	}
	var store func(testResult)
	if key != "" {
		store = func(result testResult) {
			// Only a pass is kept, under the key of the ledger it left; a
			// failure runs again next time. A result that cannot be stored
			// costs only the next run.
			_ = cache.PutResult(module, testResultKey(opts, tc, childArgs, limit, ledger), result.encode())
			if !buildStderr.written {
				keep(result.encode())
			}
		}
	}
	return runTestBinary(opts, l, binary, nil, store)
}

// runTestBinary runs the tests of the package l in the test binary, its
// arguments after prefix: none for a package's own binary, the package's
// index for a module's one binary (sharedTestCommand). store, when not
// nil, is given the output of a passing run, after the run wrote the law
// ledger (testcache.go).
func runTestBinary(opts fileOptions, l loaded, binary string, prefix []string, store func(testResult)) error {
	args, err := testChildArgs(opts, l.result.Revision)
	if err != nil {
		return err
	}
	childArgs := append(prefix[:len(prefix):len(prefix)], args...)
	// The test binary runs under a memory limit, 2 GiB unless
	// ERGO_PROGRAM_MEMORY sets another, and its stderr passes through a
	// watcher, so a test that grows without bound stops early and `ergo`
	// says which limit stopped it (docs/tooling.md, "Test memory").
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		return err
	}
	if opts.search.mode != "" {
		// A search runs each schedule in a worker of the binary, each
		// under the same limit (explore.go).
		seed := uint64(1)
		if opts.seed != "" {
			seed, _ = strconv.ParseUint(opts.seed, 10, 64)
		}
		return exploreSearch(os.Stdout, l, binary, childArgs, opts.search, opts.run, seed, limit)
	}
	// The law runs whose verdict the store holds at this seed and budget
	// print it "(cached)" and do not run (lawcache); an explored run
	// takes none, since it runs under another scheduler.
	var verdicts *sketchfront.TestVerdicts
	if !opts.explore && opts.receipt == "" {
		seed, states := uint64(1), 0
		if opts.seed != "" {
			seed, _ = strconv.ParseUint(opts.seed, 10, 64)
		}
		if opts.states != "" {
			states, _ = strconv.Atoi(opts.states)
		}
		verdicts = l.analysis.TestVerdicts(lawStore(), seed, states)
		if text := verdicts.CachedFile(); text != "" {
			// A file of this run's own: a module's one test binary runs
			// once per package (sharedTestCommand).
			dir, err := os.MkdirTemp("", "ergo-cached-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "cached")
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				return err
			}
			childArgs = append(childArgs, "--cached", path)
		}
	}
	// The verdicts this run and its ledger write are bounded by the
	// emission cache's trim (lawtrim.go).
	defer boundLaws(lawStore())
	child := testProgramCommand(binary, childArgs, limit)
	var out bytes.Buffer
	var result testResult
	// A rewrite shadow that finds its law false writes a refutation line
	// (shadow.RefutedPrefix) before its defect: the ledger records it.
	shadows := &shadowLines{}
	stdout, stderr := []io.Writer{os.Stdout}, []io.Writer{os.Stderr, shadows}
	if verdicts != nil {
		stdout = append(stdout, &out)
	}
	if store != nil {
		stdout, stderr = append(stdout, &result.stdout), append(stderr, &result.stderr)
	}
	if len(stdout) > 1 {
		child.Stdout = io.MultiWriter(stdout...)
	}
	if len(stderr) > 1 {
		child.Stderr = io.MultiWriter(stderr...)
	}
	err = runEntryProcess(child, limit, proclimit.Watch(child))
	// Each run that printed its ok line held, whatever later runs did.
	verdicts.Record(out.String())
	if len(shadows.refs) > 0 {
		// A failed run too: the refutation is a fact of the sources.
		if werr := writeRefutations(opts, l, shadows.refs); werr != nil && err == nil {
			err = werr
		}
	}
	if err != nil {
		return err
	}
	if opts.law != "" || opts.skipLaw != "" || opts.run != "" || opts.explore {
		// A partial run checks only some laws: it leaves the ledger.
		if store != nil {
			store(result)
		}
		return nil
	}
	if err := writeLedger(opts, l); err != nil {
		return err
	}
	if store != nil {
		store(result)
	}
	return nil
}

// writeLedger is a passing full run's write of the law ledger (runtime.md,
// Tests 10): the laws and site instances the run build uses, checked
// alone. In a module's one test binary (sharedTestCommand) this runs once
// per package, as each package's own `ergo test` does. A binary with no
// test of its own writes it too (testLoadedPackage): its sites may rest on
// an imported provider's law (LFL: a foreign key change left catalog's
// entries stale, and its ergo test, which tests nothing, never rewrote
// them).
func writeLedger(opts fileOptions, l loaded) error {
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	tc, err := buildAction(opts)
	if err != nil {
		return err
	}
	changed, err := sketchfront.UpdateLedger(l.analysis, sketchfront.BuildRequest{
		Mode:      build.ModeBuild,
		Source:    l.source.Admitted(),
		Cache:     build.Cache{Root: root},
		Toolchain: tc, // no explore tag: an explored run returned before
		Stderr:    os.Stderr,
		Ledger:    ledgerPath(opts.file),
		Laws:      lawStore(),
	})
	if err != nil {
		return err
	}
	if changed > 0 {
		fmt.Fprintf(os.Stderr, "ergo.laws: %d entries changed\n", changed)
	}
	return nil
}

// shadowLines reads the test program's standard error for the rewrite
// shadows' refutation lines (sketchfront.ParseShadowLine).
type shadowLines struct {
	part []byte
	refs []sketchfront.ShadowRefutation
}

func (s *shadowLines) Write(p []byte) (int, error) {
	s.part = append(s.part, p...)
	for {
		i := bytes.IndexByte(s.part, '\n')
		if i < 0 {
			break
		}
		if r, ok := sketchfront.ParseShadowLine(string(s.part[:i])); ok {
			s.refs = append(s.refs, r)
		}
		s.part = s.part[i+1:]
	}
	if len(s.part) > 1<<16 {
		// A long line is no refutation line: keep only its tail's room.
		s.part = s.part[:0]
	}
	return len(p), nil
}

// writeRefutations writes the shadows' refutations of a test run into the
// module's ergo.laws and ergo.cases (sketchfront.RecordRefutations).
func writeRefutations(opts fileOptions, l loaded, refs []sketchfront.ShadowRefutation) error {
	changed, err := sketchfront.RecordRefutations(l.analysis, sketchfront.BuildRequest{
		Source: l.source.Admitted(),
		Stderr: os.Stderr,
		Ledger: ledgerPath(opts.file),
	}, refs)
	if err != nil {
		return err
	}
	if changed > 0 {
		fmt.Fprintf(os.Stderr, "ergo.laws: %d entries changed\n", changed)
	}
	return nil
}

// testChildArgs are the test binary's arguments for opts. The binary names
// the source, as it was given here (a file or a package's folder), in a
// refuted case's or law's replay line (X1b), and a receipt names the
// source and its revision (L10).
func testChildArgs(opts fileOptions, revision string) ([]string, error) {
	childArgs := []string{"--source", opts.file, "--revision", revision}
	if opts.seed != "" {
		childArgs = append(childArgs, "--seed", opts.seed)
	}
	if opts.states != "" {
		childArgs = append(childArgs, "--states", opts.states)
	}
	if opts.receipt != "" {
		path, err := filepath.Abs(opts.receipt)
		if err != nil {
			return nil, err
		}
		childArgs = append(childArgs, "--receipt", path)
	}
	if opts.law != "" {
		childArgs = append(childArgs, "--law", opts.law)
	}
	if opts.skipLaw != "" {
		childArgs = append(childArgs, "--skip-law", opts.skipLaw)
	}
	if opts.run != "" {
		// The test binary selects the one test or law (lower's $Seed and
		// $Want), plain or explored: one name, one reading of it.
		childArgs = append(childArgs, "--run", opts.run)
	}
	return childArgs, nil
}

// lawStore is the store of law verdicts under the emission cache's root
// (lawcache.Open); nil, which stores nothing, when there is no
// root or ERGO_LAWCACHE is off: then every law runs, as go test -count=1
// runs every test (the examples runner's check leg, whose evaluation it
// compares with a native run).
func lawStore() *lawcache.Store {
	compiler, err := build.CompilerIdentity()
	if err != nil {
		return nil
	}
	return lawStoreOf(compiler)
}

// boundLaws runs the emission cache's trim of the law verdicts when this
// process wrote any to store (build.Cache.LawsWritten).
func boundLaws(store *lawcache.Store) {
	if root, err := build.DefaultRoot(); err == nil {
		build.Cache{Root: root}.LawsWritten(store.Wrote())
	}
}

// lawStoreOf is lawStore for the compiler identity compiler.
func lawStoreOf(compiler string) *lawcache.Store {
	if os.Getenv("ERGO_LAWCACHE") == "off" {
		return nil
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return nil
	}
	return lawcache.Open(root, compiler)
}

// testProgramCommand keeps the test program's collector inside its hard
// data limit. Build tools may use a larger heap and GOGC, but passing those
// settings to a smaller bounded program can reach ENOMEM before collection.
// Three quarters leaves headroom for stacks, metadata and non-heap data;
// the normal GC target also avoids an inherited off or high GOGC. An
// explicitly unbounded program retains the caller's collector settings.
func testProgramCommand(binary string, args []string, limit uint64) *exec.Cmd {
	stage.Add(stage.Processes, 1)
	child := exec.Command(binary, args...)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Stdin = os.Stdin
	child.Env = boundedProgramEnv(limit)
	return child
}

// boundedProgramEnv is the collector policy of every test-program child,
// plain or explored. An explicit zero keeps the caller's settings.
func boundedProgramEnv(limit uint64) []string {
	env := os.Environ()
	if limit != 0 {
		env = nil
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GOGC=") && !strings.HasPrefix(entry, "GOMEMLIMIT=") {
				env = append(env, entry)
			}
		}
		env = append(env, "GOGC=100", "GOMEMLIMIT="+strconv.FormatUint(limit/4*3, 10))
	}
	return env
}

// validSeed reports whether s is a seed the test binary reads: a decimal
// uint64 with no sign.
func validSeed(s string) bool {
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

// validLaw reports whether s may name a law for --law or --skip-law: empty
// (not given), or a word that is not an option and holds no space.
func validLaw(s string) bool {
	return !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, " \t\n")
}

// validStates reports whether s is a budget the test binary reads: a
// positive decimal int with no sign.
func validStates(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && s[0] != '+'
}
