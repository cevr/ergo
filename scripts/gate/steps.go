package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

// hostTestParallel bounds the parallel tests of a host package. In
// scripts/gate each parallel storage layout runs a gate in its own systemd
// scope (MemoryMax 576M), so the bound is also the number of those gates
// that run at once; Go's default, GOMAXPROCS, would scale it with the host's
// CPUs. Measured on a 16-CPU host at a 1-minute load of 13 to 60, the
// package took 151 s at 4, 126 s at 6 and 114 s at 8.
const hostTestParallel = "8"

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
	add(&Step{Name: "no tracked python bytecode", Argv: bytecodeScript.command()})
	add(&Step{Name: "wayfinder snapshot check", Argv: []string{"go", "run", "./cmd/wayfinder", "check", "--snapshot"}, Inputs: wayfinderSnapshotInputs()})

	// The Foldkit checks walk the snapshot directory, so files Git ignores
	// there are inputs too. The importer's controls run with the other Go
	// packages.
	foldkit := &Inputs{Paths: []string{"scripts/conformance/", "conformance/", "go.mod"}, Tools: []string{"go"}, Installed: []string{"conformance/upstream/foldkit-d21db423"}}
	add(&Step{Name: "foldkit corpus self-check", Argv: []string{"go", "run", "./scripts/conformance", "foldkit", "--self-check"}, Inputs: foldkit})
	add(effectMappingStep())

	add(&Step{Name: "gofmt", Argv: gofmtScript.command(append([]string{"gofmt"}, gofmtDirectories...)...)})
	// Building every test binary once lets vet, the shards and the CLI build
	// reuse one compilation instead of racing to repeat it.
	add(&Step{Name: "go test build", Argv: []string{"go", "test", "-run", "^$", "./..."}})
	add(&Step{Name: "go vet", Needs: []string{"go test build"}, Argv: []string{"go", "vet", "./..."}})
	add(&Step{Name: "provenance analyzer", Argv: provenanceAnalyzer(root)})

	durations, err := readDurations(root)
	if err != nil {
		return nil, err
	}
	var unsharded, hermetic []string
	for _, pkg := range packages {
		relative := strings.TrimPrefix(pkg, modulePath(root)+"/")
		if slices.Contains(hostTestPackages, relative) {
			add(&Step{Name: fmt.Sprintf("go test %s (host, every run)", shortPackage(root, pkg)), Needs: []string{"go test build"}, Argv: []string{"go", "test", "-count=1", "-parallel", hostTestParallel, testTimeout, pkg}})
			continue
		}
		if strings.HasPrefix(relative, hermeticTestRoot) {
			hermetic = append(hermetic, pkg)
			continue
		}
		measured, ok := durations[pkg]
		if !ok {
			unsharded = append(unsharded, pkg)
			continue
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
		n = max(1, min(n, runtime.NumCPU(), len(names)))
		selections := shardArgs(shard(names, measured, n))
		for i, selection := range selections {
			argv := append([]string{"go", "test", testTimeout}, selection...)
			add(&Step{Name: fmt.Sprintf("go test %s [%d/%d]", shortPackage(root, pkg), i+1, len(selections)), Needs: []string{"go test build"}, Argv: append(argv, pkg), ToolEnv: testTools})
		}
	}
	add(&Step{Name: "go test (other packages)", Needs: []string{"go test build"}, Argv: append([]string{"go", "test", testTimeout}, unsharded...), ToolEnv: testTools})
	// The one step that keeps Go's test cache (Step.Hermetic); every other
	// step runs its go tests uncached.
	if len(hermetic) > 0 {
		add(&Step{Name: "go test (hermetic gate packages)", Needs: []string{"go test build"}, Argv: append([]string{"go", "test", testTimeout}, hermetic...), Env: hermeticEnvironment, Hermetic: true})
	}
	// prototypes/sketch9 is its own module, so ./... never reaches it, but its
	// kernel mode links this checkout's runtime/rt. ERGO_REPO_ROOT gives its
	// kernel tests that checkout explicitly: set, any problem with it (a
	// missing directory, a go.mod Go refuses, another module) fails them, and
	// they skip only when it is unset (#218). The tests build and run programs
	// against that runtime through a go build subprocess, whose reads Go's test
	// cache cannot see, so they run uncached; env sets the variable, which the
	// filtered environment of a plain go test step would drop.
	add(&Step{Name: "go test ./prototypes/sketch9/sketch (kernel)", Argv: []string{"env", "ERGO_REPO_ROOT=" + root, "go", "test", "-C", "prototypes/sketch9", testTimeout, "-count=1", "-run", "^TestKernel", "./sketch/"}})
	// Without a VCS stamp the binary depends only on its sources, so a commit
	// that changes none of them reuses the link instead of relinking for a
	// new revision. The gate's bin/ergo therefore declares no vcs.revision in
	// its producer identity, while its artifact digest still identifies the
	// executing bytes.
	add(&Step{Name: "build bin/ergo", Needs: []string{"go test build"}, Argv: stagedBuild(root, "bin/ergo", "./cmd/ergo")})
	add(examplesStep())

	examples, err := authoredExamples(root)
	if err != nil {
		return nil, err
	}
	add(&Step{Name: "ergo fmt --check", Needs: []string{"build bin/ergo"}, Argv: append([]string{"./bin/ergo", "fmt", "--check"}, examples...)})

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
	return &Step{Name: "effect conformance mapping", Argv: []string{"go", "run", "./scripts/conformance", "check"}}
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
// ergo, the programs; sh runs setup.sh and record.sh.
func examplesStep() *Step {
	return &Step{
		Name:  "examples (default)",
		Needs: []string{"build bin/ergo"},
		Argv:  []string{"go", "run", "./scripts/examples"},
		Inputs: &Inputs{
			Paths:     []string{"scripts/examples/", "go.mod"},
			Exclude:   []string{"scripts/examples/*_test.go", "scripts/examples/testdata/"},
			Tools:     []string{"go", "sh"},
			Installed: []string{"examples"},
			Built:     []string{"bin/ergo"},
		},
	}
}

func goPackages(root string) ([]string, error) {
	command := exec.Command("go", "list", "./...")
	command.Dir = root
	output, err := command.Output()
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

// slowPackages are measured by -record-durations and sharded by the gate.
var slowPackages = []string{"./cmd/ergo"}

// writeDurations measures each slow package's tests serially (as a single
// process runs them), each under the memory budget of limit, and rewrites
// the durations file.
func writeDurations(root string, limit limiter) error {
	durations := map[string]map[string]float64{}
	module := modulePath(root)
	for _, pkg := range slowPackages {
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
