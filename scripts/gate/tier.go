package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"ergo.local/prototype/scripts/gate/replay"
)

// The fast tier (the default ./scripts/gate.sh) is what a lane runs before
// it hands back a unit; the full tier (--full) and the merge tier (--merge)
// run on every batch head before main moves (NORTH_STAR.md, docs/tooling.md).
// The fast tier runs every step of the graph but the Go tests as the full
// tier does. Its Go tests run one step per test package (sharded packages
// in shards), each with -short and without its large tests (sizeCutoff),
// and each step replays its last pass while its package key
// (replay.PackageKey) is unchanged: the package's closure, as
// internal/closure declares and checks it, with the tool and host
// fingerprints, the step environment, the gate executable and the step's
// own selection. A fast record never stands for a full run: every key
// names its tier (replay.WithTier), and --merge compares only with a
// full-tier record.
const fastTier = "fast"

// fullBuildStep is the full tier's step that builds every test binary
// before its test steps; the fast tier has none (fastSteps).
const fullBuildStep = "go test build"

// sizeCutoff is the size rule (gate-fast decision D3): a test whose
// recorded serial wall time (durationsFile) is this long or longer is large
// and runs only in the full tier. A test with no recorded time runs in the
// fast tier until the next record. The rule reads measured times, so a test
// that grows leaves the fast tier at the next -record-durations.
const sizeCutoff = 2.0

// fastFlag is what every fast-tier test step adds: properties and growth
// checks run their -short domain (testing.Short).
const fastFlag = "-short"

// hostPackageInputs are the inputs of a host test package's fast-tier key
// outside its compiled closure, declared here instead of read from
// internal/closure. That table declares scripts/gate as the whole checkout
// ("." subtree, a placeholder its reason marks as a follow-up): keyed so,
// the host package would run on every edit, and the fast tier could not
// meet its 60 s target (gate-fast plan section 5, which keys the host
// package by its compiled closure and the host fingerprint). These are the
// reads its fast-tier tests make of the real checkout: the scripts
// directory (the gate script, the durations, the authored example list,
// the conformance and examples runners the parity fixture copies), the
// observer the parity fixture copies, the Effect case list and the syntax
// source the parity fixture reads, and the closure table
// (TestTheClosureTableParses). The fast step runs its tests under the input
// observer (Step.ObserveInputs) and fails on a read outside its key
// (readsOutsideKey), so a test that reads more fails the step instead of
// making a stale pass. The tests that read the gate's declarations of the
// whole checkout (go list of every package) skip under -short
// (checkoutRoot), as do those that start the merge tier's sandbox, which
// the observer cannot observe (startsSandbox); the full tier runs every
// test uncached on every batch head.
var hostPackageInputs = map[string][]replay.Allowance{
	"scripts/gate": {
		{Path: "scripts", Grant: replay.GrantSubtree},
		{Path: "internal/testinputs", Grant: replay.GrantSubtree},
		{Path: "conformance/effect-cases.json", Grant: replay.GrantFile},
		{Path: "internal/sketch/syntax/probe.go", Grant: replay.GrantFile},
		{Path: replay.DeclaredFile, Grant: replay.GrantFile},
	},
}

// observable says whether the input observer (internal/testinputs) can
// check a fast step's reads here: it needs Linux's seccomp notification.
const observable = runtime.GOOS == "linux"

// tierFlags picks the run's tier from the flags: --merge keys and compares
// the full tier, --no-cache and -record-durations run as the full tier,
// and only a plain run is fast.
func tierFor(full, noCache, merge, recordDurations bool) string {
	if full || noCache || merge || recordDurations {
		return fullTier
	}
	return fastTier
}

// stepsFor declares the steps of a tier.
func stepsFor(root, tier string) ([]*Step, error) {
	if tier == fastTier {
		return fastSteps(root)
	}
	return gateSteps(root)
}

// listedPackage is the part of `go list -json` the fast tier reads.
type listedPackage struct {
	ImportPath, Dir, ForTest                    string
	Deps, TestGoFiles, XTestGoFiles             []string
	EmbedFiles, TestEmbedFiles, XTestEmbedFiles []string
}

// testPackage is one test package of the module, with its closure.
type testPackage struct {
	importPath string
	dir        string
	closure    *replay.Closure
}

// testPackages lists the module's test packages and the closure of each,
// as internal/closure computes it (modulePackages): the directories of
// `go list -deps -test` inside the module, the files they embed, and the
// declared inputs of DeclaredFile. A table the gate cannot read keys each
// package on the whole checkout, HEAD and the index included: it then
// replays only an unchanged commit.
func testPackages(root string) ([]testPackage, string, error) {
	command := exec.Command("go", "list", "-e", "-deps", "-test", "-json=ImportPath,Dir,ForTest,Deps,TestGoFiles,XTestGoFiles,EmbedFiles,TestEmbedFiles,XTestEmbedFiles", "./...")
	command.Dir = root
	command.Env = stepEnvironment()
	output, err := runOutput(command)
	if err != nil {
		return nil, "", fmt.Errorf("go list -deps -test ./...: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, "", err
	}
	relative := func(dir string) (string, bool) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		rel, err := filepath.Rel(resolved, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			return "", false
		}
		return filepath.ToSlash(rel), true
	}
	byPath := map[string]listedPackage{}
	var order []listedPackage
	for decoder := json.NewDecoder(bytes.NewReader(output)); ; {
		var p listedPackage
		if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, "", err
		}
		byPath[p.ImportPath] = p
		order = append(order, p)
	}
	closureOf := func(dir string, imports []string) *replay.Closure {
		c := &replay.Closure{Dir: dir}
		for _, imported := range imports {
			p, ok := byPath[imported]
			if !ok {
				continue
			}
			pdir, inside := relative(p.Dir)
			if !inside {
				continue
			}
			c.Deps = append(c.Deps, pdir)
			for _, files := range [][]string{p.EmbedFiles, p.TestEmbedFiles, p.XTestEmbedFiles} {
				for _, f := range files {
					c.Embeds = append(c.Embeds, path.Join(pdir, f))
				}
			}
		}
		return c
	}
	problem := ""
	source, err := os.ReadFile(filepath.Join(root, replay.DeclaredFile))
	var table map[string][]replay.Allowance
	if err == nil {
		table, err = replay.ParseDeclared(source)
	}
	if err != nil {
		problem = fmt.Sprintf("the closure table cannot be read (%v): every package is keyed on the whole checkout", err)
	}
	byDir := map[string]listedPackage{}
	for _, p := range order {
		if dir, inside := relative(p.Dir); inside && p.ForTest == "" && !strings.Contains(p.ImportPath, " ") && !strings.HasSuffix(p.ImportPath, ".test") {
			byDir[dir] = p
		}
	}
	var packages []testPackage
	for dir, p := range byDir {
		if len(p.TestGoFiles)+len(p.XTestGoFiles) == 0 {
			continue
		}
		main, ok := byPath[p.ImportPath+".test"]
		if !ok {
			return nil, "", fmt.Errorf("go list has no test main for %s", p.ImportPath)
		}
		c := closureOf(dir, append([]string{p.ImportPath + " [" + p.ImportPath + ".test]", p.ImportPath + "_test [" + p.ImportPath + ".test]"}, main.Deps...))
		switch declared, host := hostPackageInputs[dir]; {
		case problem != "":
			c.Declared = []replay.Allowance{{Path: ".", Grant: replay.GrantSubtree}}
		case host && observable:
			// Only the input observer (Linux) checks a declared list;
			// elsewhere the package keeps its table entry.
			c.Declared = declared
		default:
			c.Declared = table[dir]
		}
		for _, a := range c.Declared {
			if a.Grant != replay.GrantProgram {
				continue
			}
			program, ok := byDir[a.Path]
			if !ok {
				// No closure: the grant covers every path (Allowance).
				continue
			}
			if c.Programs == nil {
				c.Programs = map[string]*replay.Closure{}
			}
			c.Programs[a.Path] = closureOf(a.Path, append([]string{program.ImportPath}, program.Deps...))
		}
		packages = append(packages, testPackage{importPath: p.ImportPath, dir: dir, closure: c})
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].dir < packages[j].dir })
	return packages, problem, nil
}

// fastSelection splits a package's tests by the size rule: the tests the
// fast tier runs and the large ones it leaves to the full tier. Together
// they are every name.
func fastSelection(names []string, durations map[string]float64) (fast, large []string) {
	for _, name := range names {
		if d, ok := durations[name]; ok && d >= sizeCutoff {
			large = append(large, name)
		} else {
			fast = append(fast, name)
		}
	}
	return fast, large
}

// fastShardArgs are the selections of a fast-tier package: shards over its
// fast tests, each before the last running exactly its names, the last
// skipping every name the others run and every large test. So the shards
// run every test of the package that is not large, a test the source scan
// missed included, each once.
func fastShardArgs(groups [][]string, large []string) [][]string {
	var args [][]string
	var assigned []string
	for i, group := range groups {
		if i == len(groups)-1 {
			skip := append(slices.Clone(assigned), large...)
			if len(skip) == 0 {
				args = append(args, nil)
			} else {
				args = append(args, []string{"-skip", exactNames(skip)})
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

// fastSteps declares the fast tier: the full tier's steps but its Go tests
// (declareSteps), and one keyed step per test package or shard.
func fastSteps(root string) ([]*Step, error) {
	packages, problem, err := testPackages(root)
	if err != nil {
		return nil, err
	}
	var importPaths []string
	for _, p := range packages {
		importPaths = append(importPaths, p.importPath)
	}
	full, err := declareSteps(root, importPaths)
	if err != nil {
		return nil, err
	}
	durations, err := readDurations(root)
	if err != nil {
		return nil, err
	}
	module := modulePath(root)
	var steps, tests []*Step
	for _, step := range full {
		// Every step that is not a package's tests is the full tier's. The
		// hermetic step is a package's tests too: its packages get a keyed
		// step each, below. Kept, it relinked both test binaries to find
		// Go's cached result on every run: about 20 s of a 27 s docs-edit
		// run (measured 2026-11-06, leg-docs-3.log). The full tier's
		// "go test build" links and starts every test binary of the module
		// before any test step: in the fast tier a package step builds
		// only its own test binary, and only when its key changed, so the
		// step and the needs on it are dropped. go vet still type-checks
		// every package and its tests.
		if step.Hermetic || step.Name == fullBuildStep || runsGoTest(step) {
			continue
		}
		step.Needs = slices.DeleteFunc(slices.Clone(step.Needs), func(need string) bool { return need == fullBuildStep })
		steps = append(steps, step)
	}
	for _, p := range packages {
		relative := strings.TrimPrefix(p.importPath, module+"/")
		// A hermetic package builds as its full-tier step builds it.
		var env, needs []string
		if strings.HasPrefix(relative, hermeticTestRoot) && !slices.Contains(hostTestPackages, relative) {
			env = hermeticEnvironment
		}
		// A package whose tests run the CLI runs the one this run built.
		if slices.Contains(cliTestPackages, relative) {
			env, needs = cliTestEnvironment, []string{buildCLIStep}
		}
		names, err := testNames(filepath.Join(root, filepath.FromSlash(p.dir)))
		if err != nil {
			return nil, err
		}
		measured := durations[p.importPath]
		fast, large := fastSelection(names, measured)
		pkg := "./" + p.dir
		base := []string{"go", "test", fastFlag, testTimeout}
		name := "go test " + pkg + " (fast)"
		if slices.Contains(hostTestPackages, relative) {
			base = []string{"go", "test", "-count=1", fastFlag, "-parallel", hostTestParallel(), testTimeout}
			name = "go test " + pkg + " (host, fast)"
		}
		n := 1
		if slices.Contains(slowPackages, pkg) {
			total := 0.0
			for _, test := range fast {
				total += measured[test]
			}
			n = max(1, min(int(math.Ceil(total/shardTargetSeconds)), stepCPUs(), len(fast)))
		}
		selections := fastShardArgs(shard(fast, measured, n), large)
		for i, selection := range selections {
			step := &Step{Name: name, Needs: needs, Argv: append(append(slices.Clone(base), selection...), pkg), Env: env, ToolEnv: testTools, Package: p.closure, PackageProblem: problem}
			// A key from a declared list holds only while the input
			// observer checks the reads (testPackages).
			_, step.ObserveInputs = hostPackageInputs[p.dir]
			step.ObserveInputs = step.ObserveInputs && observable && problem == ""
			if len(selections) > 1 {
				step.Name = fmt.Sprintf("go test %s [%d/%d] (fast)", pkg, i+1, len(selections))
			}
			tests = append(tests, step)
		}
	}
	// The closure sweep of each package, keyed so that it runs only when
	// what decides its verdict in the fast tier changes (sweepClosure);
	// only where the sweep can run (sweepSupported).
	for _, p := range packages {
		if sweepSupported() {
			step := sweepStep(p.dir)
			step.Name = "closure sweep ./" + p.dir + " (fast)"
			step.Package, step.PackageProblem = sweepClosure(p.dir, problem), problem
			tests = append(tests, step)
		}
	}
	// The tests after the steps they need, in package order.
	return append(steps, tests...), nil
}

// sweepClosure is the key of a package's fast-tier closure sweep: the
// package's test files and the sweep's own package (internal/closure, the
// table in sweepTable among it), with what every key holds. So the fast
// tier sweeps every package after an edit of the table or of the sweep,
// one package after an edit of its tests, which is where a new test input
// comes from, and none after an edit of a non-test file: that keeps a
// checker edit's fast set as it was (TestFastTierReachOfAnEdit). The full
// tier sweeps every package on every run (declareSteps). A table the gate
// cannot read keys the sweep on the whole checkout, as it keys every
// package (testPackages).
func sweepClosure(dir, problem string) *replay.Closure {
	c := &replay.Closure{Dir: path.Dir(sweepTable), Declared: []replay.Allowance{{Path: dir + "/*_test.go", Grant: replay.GrantFiles}}}
	if problem != "" {
		c.Declared = []replay.Allowance{{Path: ".", Grant: replay.GrantSubtree}}
	}
	return c
}

// checkObservedInputs ends a passed step that ran under the input observer
// (Step.ObserveInputs): a checkout path its tests read that its package key
// does not cover fails the step, so a declared list that misses an input
// goes red the first time a test reads it. A log the observer could not
// complete fails the step too: its tests did something the observer
// cannot see through (a namespace, a second seccomp listener), and a pass
// left unrecorded would rerun the package on every gate in silence.
func (g *gate) checkObservedInputs(step *Step, log string, r *result) {
	outside, unobserved, err := readsOutsideKey(g.root, step.Package, log)
	switch {
	case err != nil:
		r.outcome = failed
		r.output = fmt.Appendf(r.output, "gate: %s: read the observed inputs: %v\n", step.Name, err)
	case unobserved != "":
		r.outcome = failed
		r.output = fmt.Appendf(r.output, "gate: %s: %s, so its declared key cannot be checked (a test that starts the merge tier's sandbox calls startsSandbox; see the observer's reason above)\n", step.Name, unobserved)
	case len(outside) > 0:
		r.outcome = failed
		const shown = 20
		r.output = fmt.Appendf(r.output, "gate: %s: its tests read %d checkout paths its package key does not cover (add each to hostPackageInputs in scripts/gate/tier.go, or run the test only in the full tier):\n", step.Name, len(outside))
		for _, p := range outside[:min(len(outside), shown)] {
			r.output = fmt.Appendf(r.output, "  %s\n", p)
		}
	}
}

// readsOutsideKey reads a test log that the input observer wrote
// (internal/testinputs: "open", "stat" and "chdir" lines with absolute
// paths) and returns the checkout paths in it that the closure does not
// cover, sorted, or why the log is not a complete observation.
func readsOutsideKey(root string, c *replay.Closure, log string) (outside []string, unobserved string, err error) {
	content, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil, "the tests wrote no input log", nil
	}
	if err != nil {
		return nil, "", err
	}
	text := string(content)
	if !strings.HasPrefix(text, "# test log\n") || !strings.HasSuffix(text, "\n") {
		return nil, "the input observer could not observe the tests", nil
	}
	roots := []string{filepath.Clean(root)}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != roots[0] {
		roots = append(roots, resolved)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n")[1:] {
		verb, name, _ := strings.Cut(line, " ")
		if verb != "open" && verb != "stat" && verb != "chdir" {
			continue
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(root, filepath.FromSlash(c.Dir), name)
		}
		name = filepath.Clean(name)
		for _, base := range roots {
			rel, err := filepath.Rel(base, name)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !c.Covers(rel) && !seen[rel] {
				seen[rel] = true
				outside = append(outside, rel)
			}
			break
		}
	}
	sort.Strings(outside)
	return outside, "", nil
}

// fastCPUFile holds, per test package, the CPU seconds (user and system,
// its children included) of its fast-tier steps, each run alone as the
// fast tier declares it (`-record-durations -fast`): -short, without the
// large tests, one process per shard, so each shard's own setup counts.
// Only the fast tier's CPU budget reads it (TestFastTierReachOfAnEdit):
// at a CPU budget of n a set of steps cannot finish in less than its CPU
// over n, so this sum predicts the wall where serial test times do not.
const fastCPUFile = "scripts/gate/fast-cpu.json"

func readFastCPU(root string) (map[string]float64, error) {
	content, err := os.ReadFile(filepath.Join(root, fastCPUFile))
	if os.IsNotExist(err) {
		return map[string]float64{}, nil
	}
	if err != nil {
		return nil, err
	}
	var table map[string]float64
	if err := json.Unmarshal(content, &table); err != nil {
		return nil, fmt.Errorf("%s: %w", fastCPUFile, err)
	}
	return table, nil
}

// writeFastCPU runs each named package's fast-tier steps one after
// another, each under the memory budget of limit, and rewrites their rows
// of fastCPUFile, keeping the other rows.
func writeFastCPU(root string, limit limiter, packages []string) error {
	steps, err := fastSteps(root)
	if err != nil {
		return err
	}
	table, err := readFastCPU(root)
	if err != nil {
		return err
	}
	module := modulePath(root)
	// A step a package step needs (the CLI's build, cliTestPackages) runs
	// once, before the package steps, and is not counted: the fast tier
	// runs it once per run whichever packages run.
	prepared := map[string]bool{}
	prepare := func(need string) error {
		if prepared[need] {
			return nil
		}
		prepared[need] = true
		i := slices.IndexFunc(steps, func(s *Step) bool { return s.Name == need })
		if i < 0 {
			return fmt.Errorf("no fast-tier step %q", need)
		}
		// The build is a go build, or the staged build's reviewed sh
		// script (stagedBuild): name each as a declared host program.
		var command *exec.Cmd
		switch argv := steps[i].Argv; argv[0] {
		case "go":
			command = exec.Command("go", argv[1:]...)
		case "sh":
			command = exec.Command("sh", argv[1:]...)
		default:
			return fmt.Errorf("%s: not a go build or a reviewed script: %q", need, argv)
		}
		command.Env = append(stepEnvironment(), steps[i].Env...)
		command.Dir = root
		command.Stdout = io.Discard
		command.Stderr = os.Stderr
		used, err := runBudgeted(limit, need, command)
		if used.verdict != "" {
			return fmt.Errorf("%s: %s", need, used.verdict)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", need, err)
		}
		return nil
	}
	for _, pkg := range packages {
		dir := strings.TrimPrefix(path.Clean(pkg), "./")
		cpu, ran := 0.0, 0
		for _, step := range steps {
			if step.Package == nil || step.Package.Dir != dir || step.Sweep != "" {
				continue
			}
			for _, need := range step.Needs {
				if err := prepare(need); err != nil {
					return err
				}
			}
			// Every package step is a go test (fastSteps).
			if step.Argv[0] != "go" {
				return fmt.Errorf("%s: not a go command: %q", step.Name, step.Argv)
			}
			command := exec.Command("go", step.Argv[1:]...)
			command.Env = append(stepEnvironment(), step.Env...)
			command.Dir = root
			command.Stdout = io.Discard
			command.Stderr = os.Stderr
			used, err := runBudgeted(limit, step.Name, command)
			if used.verdict != "" {
				return fmt.Errorf("%s: %s", step.Name, used.verdict)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", step.Name, err)
			}
			state := command.ProcessState
			cpu += (state.UserTime() + state.SystemTime()).Seconds()
			ran++
		}
		if ran == 0 {
			return fmt.Errorf("%s: no fast-tier step runs this package", pkg)
		}
		table[module+"/"+dir] = math.Round(cpu*10) / 10
	}
	data, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, fastCPUFile), append(data, '\n'), 0644)
}

// replayPackage is the recorded pass of a package step at key, or nil when
// none verifies (replay.Store.Lookup): the step then runs.
func (g *gate) replayPackage(step *Step, key string) *result {
	recorded, err := replay.Open(g.cacheDir).Packages().Lookup(key, g.spool)
	if err != nil {
		return nil
	}
	passed := recorded.Record.Steps[0]
	return &result{outcome: cached, spool: recorded.Outputs[0], duration: time.Duration(passed.Seconds * float64(time.Second)), passedAt: passed.Passed,
		note: fmt.Sprintf("passed in %.1fs at %s, package key %s", passed.Seconds, passed.Passed.UTC().Format(time.RFC3339), key[:12])}
}

// recordPackages ends the run's package steps (replay.Store.RecordPackage):
// a step that ran and passed records its output under its start key when
// its key from the end facts is the same; a failed one withdraws the
// record at its key. A step that replayed, or has no key, records nothing.
func (g *gate) recordPackages(out io.Writer, results []*result, objects []*replay.Object, end func() (*replay.Facts, error)) {
	store := replay.Open(g.cacheDir).Packages()
	for i, step := range g.steps {
		pk, ok := g.packageKeys[step]
		if !ok || pk.key == "" || pk.problem != "" {
			continue
		}
		r := results[i]
		line := ""
		switch {
		case r.outcome == passed && g.verdicts[step.Name] == replay.Pass:
			endKey := ""
			if facts, err := end(); err == nil {
				endKey, _ = replay.PackageKey(facts, step.Package, packageIdentity(step)...)
			}
			line = store.RecordPackage(pk.key, endKey, true, replay.Step{Name: step.Name, Outcome: "ran", Seconds: r.duration.Seconds(), Passed: r.passedAt}, objects[i], time.Now())
		case r.outcome == failed:
			line = store.RecordPackage(pk.key, "", false, replay.Step{Name: step.Name}, nil, time.Now())
		}
		if line != "" {
			fmt.Fprintln(out, line)
		}
	}
}

// packageIdentity names a package step in its key: its name, argv and
// variables, and whether its reads are observed.
func packageIdentity(step *Step) []string {
	return []string{step.Name, fmt.Sprintf("%q", step.Argv), fmt.Sprintf("%q", step.Env), fmt.Sprintf("observed %t", step.ObserveInputs)}
}
