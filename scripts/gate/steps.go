package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// durationsFile records measured per-test seconds for the sharded packages.
// It only balances shards; a stale file costs wall time, never coverage.
const durationsFile = "scripts/gate/durations.json"

// shardTarget is the measured work each Go test process should receive.
const shardTargetSeconds = 12.0

// testTimeout is a safety net for a hung test process, never a performance
// budget: a shard that needs it is a defect to split or fix.
const testTimeout = "-timeout=15m"

// testTools are the executables Go tests spawn besides the Go toolchain,
// whose binary and version Go's test cache already keys on: git, and sh,
// which runs the shell scripts tests write.
var testTools = []string{"git", "sh"}

// hostTestPackages are test packages whose results depend on the host in
// ways Go's test cache does not record: the programs they and the code they
// test run (git, sh, ps, systemd-run, the go command), the kernel's cgroups,
// the user's service manager and the file systems of the temporary
// directories. scripts/gate is one: the gate runs git and ps on every run,
// and its storage property runs gates in real systemd scopes. The gate runs
// each with -count=1, so a host test never replays: it runs on every gate,
// with -parallel hostTestParallel.
var hostTestPackages = []string{"scripts/gate"}

// maxHostTestParallel bounds the parallel tests of a host package
// (hostTestParallel): the step passes -parallel at most 8, and at most the
// CPU budget. A test binary's default -parallel is its GOMAXPROCS, which the
// gate holds at most at the CPU budget (stepGOMAXPROCS), so the bound can
// lower the default only when that is above 8. No parallel test of scripts/gate
// starts a gate in a systemd scope: the scope tests and the output-storage
// layouts run one at a time (TestBudgetOutputStorage). The 8 dates from
// before the CPU budget (02e1653e), when the 24 storage layouts ran as
// parallel gates: on a 16-CPU host at a 1-minute load of 13 to 60 the
// package took 151 s at 4, 126 s at 6 and 114 s at 8. It was not measured
// again since.
const maxHostTestParallel = 8

// hostTestParallel is the -parallel of a host package's step.
func hostTestParallel() string {
	return strconv.Itoa(min(maxHostTestParallel, stepCPUs()))
}

// sandboxHostTests are the tests of a host package that the full tier runs
// each in a step of its own, beside the package's step, which skips them:
// the tests that start the merge tier's sandbox (startsSandbox in
// scripts/gate) and start no gate in a systemd scope. Each runs alone in
// its process (it sets the environment, so it is not parallel), and most
// build Go programs in a cold sandbox cache, so they made up most of the
// package's step, whose tests otherwise ran one after another: at batch-9
// 4377b668 the package took 509 s at a CPU budget of 4 on a quiet host, of
// which these took 300 s (TestMergeTierSandboxRegression alone 164 s), and
// under the batch gate's load of 17 the one step passed the 15 minute
// testTimeout (b9-fix-gate, measured). As steps of their own they run at
// once with the package's step, within the gate's CPU budget. A test that
// starts a gate in a systemd scope stays in the package's step, where the
// scope tests run one at a time (maxHostTestParallel).
// TestSandboxHostTestsAreTheSandboxTests keeps the list.
var sandboxHostTests = map[string][]string{
	"scripts/gate": {
		"TestMergeTierSandboxRegression",
		"TestMergeTierInputContractRegression",
		"TestMergeTierSandboxConcurrent",
		"TestMergeGateStopsOnGroupSIGTERM",
		"TestSandboxAdmitsSymlinkRoots",
		"TestSandboxArgvBindsAdmittedRoots",
		"TestSandboxModuleZipIsConstructed",
		"TestSandboxPlanOfThisHost",
		"TestMergeTierFailsClosed",
		"TestSandboxMasksContaminatedInputs",
	},
}

// hostSteps declares the full tier's steps of the host package pkg: one
// step per test of sandboxHostTests, then the package's step, which skips
// them (shardArgs), so each test runs in exactly one step. Every step runs
// uncached (-count=1) with -parallel hostTestParallel. A step weighs its
// tests' recorded time (measured, the package's rows): the package's step
// the rows that no sandbox test's step holds.
func hostSteps(root, pkg, relative string, measured map[string]float64) []*Step {
	name := fmt.Sprintf("go test %s (host, every run)", shortPackage(root, pkg))
	var groups [][]string
	for _, test := range sandboxHostTests[relative] {
		groups = append(groups, []string{test})
	}
	rest := []string{}
	for test := range measured {
		if !slices.Contains(sandboxHostTests[relative], test) {
			rest = append(rest, test)
		}
	}
	selections := shardArgs(append(groups, nil))
	var steps []*Step
	for i, selection := range selections {
		step := &Step{Name: name, Needs: []string{"go test build"}, Argv: append(append([]string{"go", "test", "-count=1", "-parallel", hostTestParallel(), testTimeout}, selection...), pkg), Weight: recordedSeconds(rest, measured), Class: classHeavy}
		if i < len(groups) {
			step.Name += " " + groups[i][0]
			step.Weight = recordedSeconds(groups[i], measured)
		}
		steps = append(steps, step)
	}
	return steps
}

// hermeticTestRoot holds the gate's hermetic test packages: every package
// below it that is not a host package. Their step is the gate's one
// exception to uncached go tests (Step.Hermetic): they replay from Go's test
// cache, so each must be hermetic (package scripts/gate/hermetic): it runs
// no program but its own test binary, and its step builds it with
// CGO_ENABLED=0, so it loads no shared library.
// TestCachedGatePackagesAreHermetic checks every package of that step.
const hermeticTestRoot = "scripts/gate/"

// hermeticEnvironment is what a hermetic step adds to the environment.
var hermeticEnvironment = []string{"CGO_ENABLED=0"}

// goRuntimeEnvironment are variables every Go-built program reads at startup.
// They change what a program prints without changing its bytes: GODEBUG=
// inittrace=1 writes to standard error, GOTRACEBACK shapes a crash report. A
// step that runs Go programs and asserts their output keys on them, through
// the "go" tool fingerprint or the test tool digest.
var goRuntimeEnvironment = []string{"GODEBUG", "GOGC", "GOMEMLIMIT", "GOMAXPROCS", "GOTRACEBACK"}

// The closure sweep (internal/closure, TestEveryPackageRecordsOnlyItsClosure)
// builds a package's test binary, runs it with a test log and fails on a
// read of the checkout outside the package's declared closure: the check
// that the fast tier's package keys (replay.PackageKey) cover what each
// package's tests read. sweepVariable selects what it sweeps: "all", or a
// comma list of package directories; the gate's steps each sweep one
// package. Which packages it skips, and why, is internal/closure's (the
// gate names none).
const (
	sweepTest     = "TestEveryPackageRecordsOnlyItsClosure"
	sweepPackage  = "./internal/closure"
	sweepVariable = "ERGO_CLOSURE_SWEEP"
	// sweepTable is the file of the sweep and of the closure table.
	sweepTable = "internal/closure/closure_test.go"
)

// sweepOS is the GOOS whose steps the gate declares: runtime.GOOS, which a
// test sets to another to read that OS's steps (TestClosureSweepTier).
var sweepOS = runtime.GOOS

// sweepSupported says whether the closure sweep can run here. Its sandbox
// needs Linux user and mount namespaces; on any other OS (macOS)
// internal/closure reports the sweep unsupported and fails it. So the gate
// declares the sweep steps of both tiers only on Linux, as it observes a
// fast step's reads only there (observable).
func sweepSupported() bool { return sweepOS == "linux" }

// sweepTimeout is the test timeout of a closure sweep step. The sweep runs
// a package's test binary in runs of at most a set recorded test time each
// (internal/closure, planRuns), so many at a time as its jobs allow and the
// rest in turn, and stops a run that passes its own deadline (20 minutes,
// ERGO_CLOSURE_RUN_DEADLINE) with its package and slowest tests named. The
// step's timeout must outlast every run of the package in turn: with the
// gate's 15 minute testTimeout the binary's deadline cut each run of
// internal/sketch/lower at 13m58s, which the four runs, at once on the
// step's share of the batch-10 merge tier, reached (841 s; fu-sweep-
// deadline). It is a safety net for a sweep that hangs, as testTimeout is.
const sweepTimeout = "-timeout=45m"

// sweepStep runs the closure sweep over selection: one package directory.
func sweepStep(selection string) *Step {
	return &Step{
		Name:    "closure sweep",
		Argv:    []string{"go", "test", sweepTimeout, "-run", "^" + sweepTest + "$", sweepPackage},
		Env:     []string{sweepVariable + "=" + selection},
		ToolEnv: testTools,
		Sweep:   selection,
		Class:   classHeavy,
	}
}

// buildCLIStep builds the ergo CLI into bin/ergo, once per gate run.
const buildCLIStep = "build bin/ergo"

// cliTestPackages are the test packages whose tests run the ergo CLI. A
// test process built cmd/ergo for itself, so a package in n shards built it
// n times per run, each build racing the others after an edit of the
// compiler (fast-cpu, batch 9). In every tier each step of these packages
// needs buildCLIStep and names its output in cliTestEnvironment, so the
// tests run the binary that this run built. The package's key covers what
// the binary is built from: its closure table entry grants the cmd/ergo
// program (internal/closure), and the toolchain is in every key. The tests
// build the CLI themselves when the variable is unset (a bare go test), and
// fail when the file it names is missing.
var cliTestPackages = []string{"internal/sketch/lower"}

// cliTestEnvironment names the gate's CLI to a cliTestPackages step: the
// path is relative to the module root, so the value, a part of the step's
// key, is the same on every run and in every checkout.
var cliTestEnvironment = []string{"ERGO_TEST_CLI=bin/ergo"}

// corpusGcPackage's full-tier steps set corpusGcEnvironment, which runs
// its TestCorpusCompilesWithGc: gc and go vet over the whole lowered corpus
// in one go command each (266 CPU-s at the share, test-speed T5). It runs
// once per full tier (plan Q2), and not in the package's other runs (a
// bare go test, the closure sweep, the mutant ledger). The fast tier runs
// -short, which skips it, and checks with go/types in process.
const corpusGcPackage = "internal/sketch/lower"

var corpusGcEnvironment = []string{"ERGO_TEST_CORPUS_GC=1"}

// gofmtDirectories are the Go trees the gate requires to be gofmt-clean.
var gofmtDirectories = []string{"cmd", "internal", "runtime", "scripts/gate", "scripts/conformance", "scripts/examples", "examples/go-interop", "tools/provenance"}

// provenanceManifest lists the checker functions that may make syntax nodes;
// the provenance analyzer flags a node made anywhere else in the checker.
const provenanceManifest = "internal/sketch/check/testdata/provenance-manifest.txt"

// gateSteps declares the gate's steps over the module's Go packages.
func gateSteps(root string) ([]*Step, error) {
	packages, err := goPackages(root)
	if err != nil {
		return nil, err
	}
	return declareSteps(root, packages)
}

// declareSteps declares the gate's steps, testing the given Go packages. A
// test that inspects a declaration passes the packages itself: listing them
// walks the whole module, so Go's test cache would key that test on every
// directory's entries, including the ones the gate's own steps rewrite.
func declareSteps(root string, packages []string) ([]*Step, error) {
	var steps []*Step
	add := func(step *Step) *Step {
		steps = append(steps, step)
		return step
	}
	add(&Step{Name: "no tracked python bytecode", Argv: bytecodeScript.command(), Class: classFast})
	add(&Step{Name: "wayfinder snapshot check", Argv: []string{"go", "run", "./cmd/wayfinder", "check", "--snapshot"}, Inputs: wayfinderSnapshotInputs(), Class: classFast})

	// The Foldkit checks walk the snapshot directory, so files Git ignores
	// there are inputs too. The importer's controls run with the other Go
	// packages.
	foldkit := &Inputs{Paths: []string{"scripts/conformance/", "conformance/", "go.mod"}, Tools: []string{"go"}, Installed: []string{"conformance/upstream/foldkit-d21db423"}}
	add(&Step{Name: "foldkit corpus self-check", Argv: []string{"go", "run", "./scripts/conformance", "foldkit", "--self-check"}, Inputs: foldkit, Class: classFast})
	add(effectMappingStep())

	add(&Step{Name: "gofmt", Argv: gofmtScript.command(append([]string{"gofmt"}, gofmtDirectories...)...), Class: classFast})
	// Building every test binary once lets vet, the shards and the CLI build
	// reuse one compilation instead of racing to repeat it.
	add(&Step{Name: fullBuildStep, Argv: []string{"go", "test", "-run", "^$", "./..."}, Class: classHeavy})
	add(&Step{Name: "go vet", Needs: []string{"go test build"}, Argv: []string{"go", "vet", "./..."}, Class: classFast})
	// The analyzer's controls are a go test of another module, uncached,
	// and its run builds and loads the checker with x/tools: full tier.
	add(&Step{Name: "provenance analyzer", Argv: provenanceAnalyzer(root), Class: classHeavy})

	durations, err := readDurations(root)
	if err != nil {
		return nil, err
	}
	add(&Step{Name: "explored scheduler", Argv: []string{"go", "test", "-tags", "ergoexplore", testTimeout, "./runtime/rt"}, Weight: packageSeconds(durations[modulePath(root)+"/runtime/rt"]), Class: classHeavy})
	var unsharded, hermetic []string
	for _, pkg := range packages {
		relative := strings.TrimPrefix(pkg, modulePath(root)+"/")
		if slices.Contains(hostTestPackages, relative) {
			for _, step := range hostSteps(root, pkg, relative, durations[pkg]) {
				add(step)
			}
			continue
		}
		if strings.HasPrefix(relative, hermeticTestRoot) {
			hermetic = append(hermetic, pkg)
			continue
		}
		measured, ok := durations[pkg]
		cli := slices.Contains(cliTestPackages, relative)
		// A CLI package always gets steps of its own, which need the
		// CLI's build (cliTestPackages); unmeasured, it runs in one.
		if !cli && (!ok || !slices.Contains(slowPackages, "./"+relative)) {
			unsharded = append(unsharded, pkg)
			continue
		}
		needs, env := []string{"go test build"}, []string(nil)
		if cli {
			needs, env = append(needs, buildCLIStep), cliTestEnvironment
		}
		if relative == corpusGcPackage {
			env = append(slices.Clone(env), corpusGcEnvironment...)
		}
		dir := filepath.Join(root, strings.TrimPrefix(pkg, modulePath(root)+"/"))
		names, err := testNames(dir)
		if err != nil {
			return nil, err
		}
		total := 0.0
		for _, name := range names {
			total += measured[name]
		}
		n := int(math.Ceil(total / shardTargetSeconds))
		n = max(1, min(n, stepCPUs(), len(names)))
		groups := shard(names, measured, n)
		selections, weights := shardArgs(groups), shardWeights(groups, measured)
		for i, selection := range selections {
			argv := append([]string{"go", "test", testTimeout}, selection...)
			add(&Step{Name: fmt.Sprintf("go test %s [%d/%d]", shortPackage(root, pkg), i+1, len(selections)), Needs: needs, Argv: append(argv, pkg), Env: env, ToolEnv: testTools, Weight: weights[i], Class: classHeavy})
		}
	}
	others := 0.0
	for _, pkg := range unsharded {
		others += packageSeconds(durations[pkg])
	}
	add(&Step{Name: "go test (other packages)", Needs: []string{"go test build"}, Argv: append([]string{"go", "test", testTimeout}, unsharded...), ToolEnv: testTools, Weight: others, Class: classHeavy})
	// The full tier sweeps every package's closure on every run (gate-tier
	// round 1 and plan G2: "always in the full tier"; owner question 3 of
	// batch 9, on its default), one package per step, as the fast tier
	// does: as one step over every package the sweep ran about 10 minutes
	// on a quiet host, and under the batch-9 gate's load it passed the
	// 15 minute testTimeout (b9-fix-gate). Only where the sweep can run
	// (sweepSupported).
	if sweepSupported() {
		for _, pkg := range packages {
			dir := strings.TrimPrefix(pkg, modulePath(root)+"/")
			// Only a package with tests is a test package of the sweep.
			if tests, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*_test.go")); err != nil || len(tests) == 0 {
				continue
			}
			sweep := sweepStep(dir)
			sweep.Name = "closure sweep ./" + dir
			sweep.Needs = []string{"go test build"}
			sweep.Weight = sweepWeight(durations, pkg)
			add(sweep)
		}
	}
	// The one step that keeps Go's test cache (Step.Hermetic); every other
	// step runs its go tests uncached.
	if len(hermetic) > 0 {
		weight := 0.0
		for _, pkg := range hermetic {
			weight += packageSeconds(durations[pkg])
		}
		add(&Step{Name: "go test (hermetic gate packages)", Needs: []string{"go test build"}, Argv: append([]string{"go", "test", testTimeout}, hermetic...), Env: hermeticEnvironment, Hermetic: true, Weight: weight, Class: classHeavy})
	}
	// prototypes/sketch9 is its own module, so ./... never reaches it, but its
	// kernel mode links this checkout's runtime/rt. ERGO_REPO_ROOT gives its
	// kernel tests that checkout explicitly: set, any problem with it (a
	// missing directory, a go.mod Go refuses, another module) fails them, and
	// they skip only when it is unset (#218). The tests build and run programs
	// against that runtime through a go build subprocess, whose reads Go's test
	// cache cannot see, so they run uncached; env sets the variable, which the
	// filtered environment of a plain go test step would drop.
	add(&Step{Name: "go test ./prototypes/sketch9/sketch (kernel)", Argv: []string{"env", "ERGO_REPO_ROOT=" + root, "go", "test", "-C", "prototypes/sketch9", testTimeout, "-count=1", "-run", "^TestKernel", "./sketch/"}, Class: classHeavy})
	// Without a VCS stamp the binary depends only on its sources, so a commit
	// that changes none of them reuses the link instead of relinking for a
	// new revision. The gate's bin/ergo therefore declares no vcs.revision in
	// its producer identity, while its artifact digest still identifies the
	// executing bytes.
	// It writes bin/ (Step.Writes): no closure sweep runs beside it.
	add(&Step{Name: buildCLIStep, Needs: []string{"go test build"}, Argv: stagedBuild(root, "bin/ergo", "./cmd/ergo"), Writes: []string{"bin/"}, Class: classFast})
	examplesRun, err := examplesStep(root)
	if err != nil {
		return nil, err
	}
	add(examplesRun)
	add(&Step{Name: "model baton", Needs: []string{buildCLIStep}, Argv: []string{"./bin/ergo", "test", "models/baton"}, Class: classHeavy})

	examples, err := authoredExamples(root)
	if err != nil {
		return nil, err
	}
	add(&Step{Name: "ergo fmt --check", Needs: []string{buildCLIStep}, Argv: append([]string{"./bin/ergo", "fmt", "--check"}, examples...), Class: classFast})

	return steps, nil
}

// uncachedStep is the step as the merge tier runs it: without the hermetic
// exception, so it runs with the gate's uncached default (stepEnvironment)
// like every other step, and no go test it starts, at any depth, replays
// Go's test cache. Its argv is the inner tier's. Go's build cache stays: it
// is keyed by content, the toolchain included.
func uncachedStep(step *Step) *Step {
	uncached := *step
	uncached.Hermetic = false
	return &uncached
}

// stagedBuild returns a command that builds a package's executable to a
// staging file beside the gate's cache and renames it onto out only when its
// bytes differ. go build touches its output even when it relinks nothing,
// and Go's test cache keys a test that listed a directory on every entry's
// modification time, so rewriting an unchanged bin/ergo would rerun every test
// that lists bin/. Without a cache directory it builds in place. The script
// is stagedBuildScript, a reviewed script (reviewed.go).
func stagedBuild(root, out, pkg string) []string {
	cache := cacheDirectory()
	if cache == "" {
		return []string{"go", "build", "-buildvcs=false", "-o", out, pkg}
	}
	sum := sha256.Sum256([]byte(root))
	staging := filepath.Join(cache, "build", hex.EncodeToString(sum[:8]), filepath.Base(out))
	// The script's $0 names it, not a shell.
	return stagedBuildScript.command("stagedbuild", staging, out, pkg)
}

// effectMappingStep checks the Effect behavior mapping and its evidence
// tests. The check reads the mapping, the evidence files it names and Git's
// index: an evidence file must be tracked. No digest of declared paths sees
// the index, so the step has no receipt of its own and runs on every
// whole-gate miss; it takes about 0.15 s (measured, gate-testcache-gap
// mapping-cost.log), less than the receipt lookup it replaces. A whole-gate
// record keys the index, so the step still replays inside one. Its own
// tests run with the other Go packages.
func effectMappingStep() *Step {
	return &Step{Name: "effect conformance mapping", Argv: []string{"go", "run", "./scripts/conformance", "check"}, Class: classFast}
}

// provenanceAnalyzer returns a command that runs the provenance analyzer's own
// controls, then the analyzer on the checker. tools/provenance is its own
// module, so the main module keeps no dependency on golang.org/x/tools and
// ./... never reaches it. The analyzer binary goes beside the gate's cache, or
// to a temporary directory without one.
//
// The controls run with -count=1, so Go's test cache never replays them. They
// read main-module sources (the checker's syntax), in their own process and
// through analysistest, and Go rechecks a cached result only against files
// inside the package's own module: a field removed from a main-module source
// would replay a stale pass (gate-testcache-gap accept-base.log). The
// observation of the main module's tests (internal/testinputs) does not reach
// another module. The gate's uncached default (stepEnvironment) gives them
// -count=1 too; the flag in the script keeps them uncached if the default
// changes. Go's build cache still skips an unchanged
// build. The script is provenanceScript, a reviewed script (reviewed.go).
func provenanceAnalyzer(root string) []string {
	binary := ""
	// go build -C resolves -o in tools/provenance, so the path is absolute;
	// like stagedBuild, each checkout gets its own.
	if cache, err := filepath.Abs(cacheDirectory()); cacheDirectory() != "" && err == nil {
		sum := sha256.Sum256([]byte(root))
		binary = filepath.Join(cache, "build", hex.EncodeToString(sum[:8]), "provenance")
	}
	return provenanceScript.command("provenance", binary, provenanceManifest)
}

// wayfinderSnapshotInputs keys the check that the committed Wayfinder snapshot
// is a structurally valid map: the snapshot, the command's non-test sources
// (it imports only the standard library) and the Go toolchain. The check never
// reads GitHub, so a stale snapshot passes; the command's fixture tests run
// with the other Go packages.
func wayfinderSnapshotInputs() *Inputs {
	return &Inputs{
		Paths:   []string{"docs/wayfinder/snapshot.json", "cmd/wayfinder/", "go.mod"},
		Exclude: []string{"cmd/wayfinder/*_test.go"},
		Tools:   []string{"go"},
		Env:     []string{"GOFLAGS", "GOEXPERIMENT"},
	}
}

// examplesStep runs the example programs of the default tag against their
// recorded cases (scripts/examples). The runner walks examples/ on disk, so
// the folder is installed: a file Git ignores there can change its result,
// and no step writes under it. It runs the bin/ergo its Need wrote, which
// holds no VCS stamp and embeds the runtime that ergo build emits, so that
// file's digest stands for the compiler. go builds the runner and, through
// ergo, the programs; sh runs setup.sh and record.sh. The runner's own Go
// inputs are its closure as go list -deps lists it (examplesRunnerInputs):
// a runner that links the checker (batch 9 D3) keys on the checker's
// sources too, so a checker edit cannot replay a stale pass.
func examplesStep(root string) (*Step, error) {
	paths, exclude, err := examplesRunnerInputs(root)
	if err != nil {
		return nil, err
	}
	return &Step{
		Name:  "examples (default)",
		Class: classFast,
		Needs: []string{"build bin/ergo"},
		Argv:  []string{"go", "run", examplesRunner},
		Inputs: &Inputs{
			Paths:     append([]string{"scripts/examples/", "go.mod"}, paths...),
			Exclude:   append([]string{"scripts/examples/*_test.go", "scripts/examples/testdata/"}, exclude...),
			Tools:     []string{"go", "sh"},
			Installed: []string{"examples"},
			Built:     []string{"bin/ergo"},
		},
	}, nil
}

// examplesRunner is the package the examples step runs.
const examplesRunner = "./scripts/examples"

// examplesRunnerInputs are the Go files of the module that the examples
// runner links besides its own directory: for each package of the module
// in `go list -deps` of the runner, its non-test Go files and the files it
// embeds, as Inputs patterns. A checkout with no runner directory (a test
// fixture module) has nothing to list: the step's own paths key it, and the
// step, if it runs there, fails.
func examplesRunnerInputs(root string) (paths, exclude []string, err error) {
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(examplesRunner, "./")))); os.IsNotExist(err) {
		return nil, nil, nil
	}
	command := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}\t{{join .EmbedFiles \"\\t\"}}{{end}}", examplesRunner)
	command.Dir = root
	command.Env = stepEnvironment()
	output, err := runOutput(command)
	if err != nil {
		return nil, nil, fmt.Errorf("go list -deps %s: %w", examplesRunner, err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "\t")
		if fields[0] == "" {
			continue
		}
		dir := fields[0]
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		rel, err := filepath.Rel(resolved, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			// Outside the module: go.mod and the go fingerprint key it.
			continue
		}
		rel = filepath.ToSlash(rel)
		if rel == strings.TrimPrefix(examplesRunner, "./") {
			continue
		}
		paths = append(paths, rel+"/*.go")
		exclude = append(exclude, rel+"/*_test.go")
		for _, embedded := range fields[1:] {
			if embedded != "" {
				paths = append(paths, rel+"/"+embedded)
			}
		}
	}
	return paths, exclude, nil
}

func goPackages(root string) ([]string, error) {
	command := exec.Command("go", "list", "./...")
	command.Dir = root
	output, err := runOutput(command)
	if err != nil {
		return nil, fmt.Errorf("go list ./...: %w", err)
	}
	return strings.Fields(string(output)), nil
}

func modulePath(root string) string {
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func shortPackage(root, pkg string) string {
	return "./" + strings.TrimPrefix(pkg, modulePath(root)+"/")
}

func readDurations(root string) (map[string]map[string]float64, error) {
	content, err := os.ReadFile(filepath.Join(root, durationsFile))
	if os.IsNotExist(err) {
		return map[string]map[string]float64{}, nil
	}
	if err != nil {
		return nil, err
	}
	var durations map[string]map[string]float64
	if err := json.Unmarshal(content, &durations); err != nil {
		return nil, fmt.Errorf("%s: %w", durationsFile, err)
	}
	return durations, nil
}

func authoredExamples(root string) ([]string, error) {
	const list = "scripts/authored_examples.txt"
	content, err := os.ReadFile(filepath.Join(root, list))
	if err != nil {
		return nil, err
	}
	var examples []string
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		example := scanner.Text()
		if example == "" {
			return nil, fmt.Errorf("empty authored example entry in %s", list)
		}
		if info, err := os.Stat(filepath.Join(root, example)); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("missing authored example: %s", example)
		}
		examples = append(examples, example)
	}
	if len(examples) == 0 {
		return nil, fmt.Errorf("authored example selection is empty: %s", list)
	}
	return examples, nil
}

// shardWeights are the recorded seconds of each shard that shardArgs and
// fastShardArgs make of groups, in their order: they skip an empty group
// before the last, and the last shard runs the last group.
func shardWeights(groups [][]string, measured map[string]float64) []float64 {
	var weights []float64
	for i, group := range groups {
		if len(group) == 0 && i < len(groups)-1 {
			continue
		}
		weights = append(weights, recordedSeconds(group, measured))
	}
	return weights
}

// recordedSeconds is the recorded time of the tests names, or 0 when their
// package has no row (measured is nil), which the slot pool reads as
// unrecordedWeight. A recorded total under minRecordedWeight counts as it,
// so a package of quick tests is not taken for one with no record.
func recordedSeconds(names []string, measured map[string]float64) float64 {
	if measured == nil {
		return 0
	}
	total := 0.0
	for _, name := range names {
		total += measured[name]
	}
	return max(total, minRecordedWeight)
}

// minRecordedWeight is the least weight of a recorded step: the resolution
// of durations.json (writeDurations rounds to a hundredth of a second).
const minRecordedWeight = 0.01

// sweepWeightFactor is a closure sweep's expected time over its package's
// recorded test time (Step.Weight): the sweep runs every test observed,
// with a cold GOCACHE per run. internal/sketch/lower recorded 368 s and its
// sweep took 1472 CPU-s in the batch-10 merge tier (try9), a factor of 4.
const sweepWeightFactor = 4

// sweepWeight is the expected seconds of the closure sweep of the package
// importPath, or 0 (unrecordedWeight) when durations.json has no row for
// it. A host package's sweep weighs 0 too: the sweep does not run its tests
// (internal/closure's unsweepable: they start bwrap), so their recorded
// time, hundreds of seconds for scripts/gate, does not predict the step.
func sweepWeight(durations map[string]map[string]float64, importPath string) float64 {
	for _, host := range hostTestPackages {
		if strings.HasSuffix(importPath, "/"+host) {
			return 0
		}
	}
	return sweepWeightFactor * packageSeconds(durations[importPath])
}

// packageSeconds is the recorded time of every test of a package's rows
// (recordedSeconds).
func packageSeconds(measured map[string]float64) float64 {
	return recordedSeconds(slices.Collect(maps.Keys(measured)), measured)
}

// slowPackages are sharded by the gate over their recorded durations.
// The checker and the lowering have run their tests in one process, one at
// a time: 294 s and 509 s serial at a CPU budget of 4 (gate-fast plan, run
// wf10, section 1.1, measured). Their shards are processes, so they need no
// t.Parallel audit; each shard is a step with its own TMPDIR (stepTempDir).
// The shard count is the measured work over shardTargetSeconds, at most the
// CPU budget.
var slowPackages = []string{"./cmd/ergo", "./internal/sketch/check", "./internal/sketch/lower"}

// testedPackages are the module's packages that have tests, as "./dir".
func testedPackages(root string) ([]string, error) {
	command := exec.Command("go", "list", "-f", "{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}", "./...")
	command.Dir = root
	output, err := runOutput(command)
	if err != nil {
		return nil, fmt.Errorf("go list ./...: %w", err)
	}
	var packages []string
	for _, pkg := range strings.Fields(string(output)) {
		packages = append(packages, shortPackage(root, pkg))
	}
	return packages, nil
}

// writeDurations measures each package's tests serially (as a single
// process runs them), each under the memory budget of limit, and rewrites
// their rows of the durations file, keeping the rows of the packages it
// did not measure. The sharded packages (slowPackages) balance their
// shards by these rows; every package's rows decide its large tests (the
// size rule, sizeCutoff).
func writeDurations(root string, limit limiter, packages []string) error {
	durations, err := readDurations(root)
	if err != nil {
		return err
	}
	module := modulePath(root)
	for _, pkg := range packages {
		name := "go test -json " + pkg
		command := exec.Command("go", "test", "-count=1", "-json", testTimeout, pkg)
		command.Env = stepEnvironment()
		command.Dir = root
		var stdout bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = os.Stderr
		used, err := runBudgeted(limit, name, command)
		if used.verdict != "" {
			return fmt.Errorf("%s: %s", name, used.verdict)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		output := stdout.Bytes()
		// Elapsed omits time a test spends waiting for its parallel
		// subtests, so a test's weight is its wall time to finish, counted
		// from its start or, for a parallel test, from when it resumes.
		measured := map[string]float64{}
		started := map[string]time.Time{}
		decoder := json.NewDecoder(bytes.NewReader(output))
		for decoder.More() {
			var event struct {
				Time   time.Time
				Action string
				Test   string
			}
			if err := decoder.Decode(&event); err != nil {
				return err
			}
			if event.Test == "" || strings.Contains(event.Test, "/") {
				continue
			}
			switch event.Action {
			case "run", "cont":
				started[event.Test] = event.Time
			case "pass", "fail", "skip":
				measured[event.Test] = math.Round(event.Time.Sub(started[event.Test]).Seconds()*100) / 100
			}
		}
		durations[module+"/"+strings.TrimPrefix(pkg, "./")] = measured
	}
	data, err := json.MarshalIndent(durations, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, durationsFile), append(data, '\n'), 0644)
}
