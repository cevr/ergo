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

// stepClass says which tiers run a step. Every step the gate declares has
// a class, and fastSteps derives the fast tier's step list from it: a step
// with no class fails the declaration, and a heavy step stays out of the
// fast tier. TestFastTierHoldsNoHeavyStep fails a step that does heavy work
// and is not declared classHeavy, so the fast tier stays fast by
// construction and not by a time limit.
type stepClass int

const (
	classUndeclared stepClass = iota
	// classFast steps run in every tier: in-process checks and the
	// packages' -short tests, and the examples step with the build of the
	// CLI it runs (test-speed decision Q1: "the fast tier keeps
	// in-process tests + the examples step").
	classFast
	// classHeavy steps run only in the full and merge tiers, on every
	// batch head: a go test without -short, a build and run of another
	// module, a closure sweep, the host packages' tests (they start gates
	// in systemd scopes and the merge tier's sandbox) and gc over the
	// corpus (decision Q1: "build-and-run unit tests run on every batch
	// head").
	classHeavy
)

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
		if problem != "" {
			c.Declared = []replay.Allowance{{Path: ".", Grant: replay.GrantSubtree}}
		} else {
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

// classFastSteps are the full tier's classFast steps, which the fast tier
// keeps (stepClass); a step with no class fails. Every go test step of the
// full tier is heavy: in the fast tier each test package gets a keyed
// -short step instead (fastSteps). The hermetic step, kept, relinked both
// test binaries to find Go's cached result on every run: about 20 s of a
// 27 s docs-edit run (measured 2026-11-06, leg-docs-3.log). The full
// tier's "go test build" links and starts every test binary of the module
// before any test step: in the fast tier a package step builds only its
// own test binary, and only when its key changed, so the step and the
// needs on it are dropped. go vet still type-checks every package and its
// tests.
func classFastSteps(full []*Step) ([]*Step, error) {
	var steps []*Step
	for _, step := range full {
		switch step.Class {
		case classFast:
		case classHeavy:
			continue
		default:
			return nil, fmt.Errorf("step %q declares no class (stepClass): say which tiers run it", step.Name)
		}
		step.Needs = slices.DeleteFunc(slices.Clone(step.Needs), func(need string) bool { return need == fullBuildStep })
		steps = append(steps, step)
	}
	return steps, nil
}

// fastSteps declares the fast tier: the full tier's classFast steps
// (classFastSteps), and one keyed -short step per test package or shard
// but the host packages'.
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
	steps, err := classFastSteps(full)
	if err != nil {
		return nil, err
	}
	var tests []*Step
	for _, p := range packages {
		relative := strings.TrimPrefix(p.importPath, module+"/")
		// A host package's tests are heavy (classHeavy): they start gates
		// in systemd scopes and build in the merge tier's sandbox (754.8 s
		// wall, 392 CPU-s under -short at 2b089097, gate-minute
		// measure.md). The full tier runs them on every batch head.
		if slices.Contains(hostTestPackages, relative) {
			continue
		}
		// A hermetic package builds as its full-tier step builds it.
		var env, needs []string
		if strings.HasPrefix(relative, hermeticTestRoot) {
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
		if len(names) > 0 && len(fast) == 0 {
			// Every test is large: a step would run none and only build
			// the test binary, which go vet type-checks. The full tier
			// runs the tests (scripts/langmigrate's TestTryParensIsIdempotent).
			continue
		}
		pkg := "./" + p.dir
		base := []string{"go", "test", fastFlag}
		name := "go test " + pkg + " (fast)"
		n := 1
		if slices.Contains(slowPackages, pkg) {
			total := 0.0
			for _, test := range fast {
				total += measured[test]
			}
			n = max(1, min(int(math.Ceil(total/shardTargetSeconds)), stepCPUs(), len(fast)))
		}
		groups := shard(fast, measured, n)
		selections, weights := fastShardArgs(groups, large), shardWeights(groups, measured)
		for i, selection := range selections {
			step := &Step{Name: name, Needs: needs, Argv: append(append(append(slices.Clone(base), testTimeoutFor(weights[i])), selection...), pkg), Env: env, ToolEnv: testTools, Package: p.closure, PackageProblem: problem, Weight: weights[i], Class: classFast}
			if len(selections) > 1 {
				step.Name = fmt.Sprintf("go test %s [%d/%d] (fast)", pkg, i+1, len(selections))
			}
			tests = append(tests, step)
		}
	}
	// The tests after the steps they need, in package order.
	return append(steps, tests...), nil
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
			if step.Package == nil || step.Package.Dir != dir {
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
// variables.
func packageIdentity(step *Step) []string {
	return []string{step.Name, fmt.Sprintf("%q", step.Argv), fmt.Sprintf("%q", step.Env)}
}
