package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
	"sync"
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
// and runs only in the full tier. The fast tier reads the times its own
// and full runs observed over the record (testSeconds, GS33b): a test with
// no time runs in the fast tier once, a test that grows leaves it at the
// next run, and one whose last full runs are under the cutoff comes back.
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
	switch tier {
	case fastTier:
		return fastSteps(root)
	case guardsTier:
		steps, err := fastSteps(root)
		return guardSteps(steps), err
	case roundTier:
		steps, _, err := fastStepsSelected(root, "", nil, true)
		return steps, err
	}
	return fullSteps(root)
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
	// unread says the closure table could not be read: closure.Declared
	// is then the whole checkout as a key, not a grant the table gives.
	unread bool
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
		packages = append(packages, testPackage{importPath: p.ImportPath, dir: dir, closure: c, unread: problem != ""})
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
// -short step instead (fastSteps). The full tier's "go test build" links and starts every test binary of the module
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

// fullTierOnly says whether every test of the package in dir belongs to
// the full tier: it has a test, every Test function's first statement
// calls needsBuild (the -short skip of a test that builds or runs Go, as
// in internal/sketch/lower), and it has no Fuzz or Example function, which
// -short would still run. Such a package's -short run skips every test,
// yet it still compiles, links and starts its test binary: after a
// checker edit that was most of the cost of internal/selfhostcheck and
// internal/experimental/irspike (batch-18 verify B1). So the fast tier
// gives it no step, and the full tier runs it as it runs every package.
func fullTierOnly(dir string) (bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	tests := 0
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return false, err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name == "TestMain" {
				continue
			}
			name := fn.Name.Name
			switch {
			case strings.HasPrefix(name, "Fuzz"), strings.HasPrefix(name, "Example"):
				return false, nil
			case !strings.HasPrefix(name, "Test"):
				continue
			}
			tests++
			if fn.Body == nil || len(fn.Body.List) == 0 || !callsNeedsBuild(fn.Body.List[0]) {
				return false, nil
			}
		}
	}
	return tests > 0, nil
}

// callsNeedsBuild says whether the statement is a call of needsBuild, its
// results dropped or assigned.
func callsNeedsBuild(s ast.Stmt) bool {
	var e ast.Expr
	switch s := s.(type) {
	case *ast.ExprStmt:
		e = s.X
	case *ast.AssignStmt:
		if len(s.Rhs) != 1 {
			return false
		}
		e = s.Rhs[0]
	default:
		return false
	}
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "needsBuild"
}

// fastExamplesFile lists the example programs the fast tier runs
// (gate-speed GS3): those a mutant ledger names as a killer. The full tier
// runs every default program. internal/residue checks the list against
// the ledgers (TestFastTierExamplesAreLedgerKillers).
const fastExamplesFile = "scripts/examples/fast-tier.txt"

// fastExamples narrows the fast tier's examples step to the programs of
// fastExamplesFile. 35 of the 51 default programs were named by no ledger
// row: 147 of the step's 208 ergo test runs, 261 s (gate-speed.md).
// A missing or empty list is an error, never a silent full or empty run;
// a module with no runner (a test's fixture) has no list to read, and
// the step, if it runs there, fails (runnerInputs).
func fastExamples(root string, steps []*Step) error {
	i := slices.IndexFunc(steps, func(s *Step) bool { return s.Name == examplesStepName })
	if i < 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(examplesRunner, "./")))); os.IsNotExist(err) {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(fastExamplesFile)))
	if err != nil {
		return fmt.Errorf("the fast tier's example programs: %w", err)
	}
	var programs []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			programs = append(programs, line)
		}
	}
	if len(programs) == 0 {
		return fmt.Errorf("%s lists no program", fastExamplesFile)
	}
	step := *steps[i]
	step.Argv = append(slices.Clone(step.Argv), programs...)
	step.Weight = fastExamplesWeight
	steps[i] = &step
	return nil
}

// fastSteps declares the fast tier: the full tier's classFast steps
// (classFastSteps), and one keyed -short step per test package or shard
// but the host packages' and the full-tier-only packages' (fullTierOnly).
func fastSteps(root string) ([]*Step, error) {
	steps, _, err := fastStepsSelected(root, "", nil, false)
	return steps, err
}

// selector judges which fast tests of a selectPackages package an edit
// reaches (selectTests); env is the step's environment.
type selector func(p testPackage, env, names []string) selection

// plan is a package's fast-tier step before its selection: the tests it
// runs (fast) and those it leaves to the full tier (large).
type plan struct {
	p           testPackage
	relative    string
	env, needs  []string
	fast, large []string
	measured    map[string]float64
	chosen      *selection
	// whole is why the package runs whole (wholeOf), or "".
	whole string
}

// planOf is the plan of p's fast-tier step (relative is its directory in
// the module, measured its recorded test times), or nil when the fast tier
// gives p no step: a host package, or one every test of which builds Go
// (fullTierOnly) outside the round tier.
func planOf(root string, p testPackage, relative string, measured map[string]float64, round bool) (*plan, error) {
	// A host package's tests are heavy (classHeavy): they start gates
	// in systemd scopes and build in the merge tier's sandbox (754.8 s
	// wall, 392 CPU-s under -short at 2b089097, gate-minute
	// measure.md). The full tier runs them on every batch head.
	if slices.Contains(hostTestPackages, relative) {
		return nil, nil
	}
	if only, err := fullTierOnly(filepath.Join(root, filepath.FromSlash(p.dir))); err != nil {
		return nil, err
	} else if only && !round {
		return nil, nil
	}
	var env, needs []string
	// A package whose tests run the CLI runs the one this run built.
	if slices.Contains(cliTestPackages, relative) {
		env, needs = cliTestEnvironment, []string{buildCLIStep}
	}
	names, err := testNames(filepath.Join(root, filepath.FromSlash(p.dir)))
	if err != nil {
		return nil, err
	}
	fast, large := fastSelection(names, measured)
	// A guard package runs whole: no size cut here, no selection after
	// (whole.go).
	whole, err := wholeOf(root, p)
	if err != nil {
		return nil, err
	}
	if round || whole != "" {
		fast, large = names, nil
	}
	return &plan{p: p, relative: relative, env: env, needs: needs, fast: fast, large: large, measured: measured, whole: whole}, nil
}

// fastStepsSelected is fastSteps, with the fast tests of each of
// selectPackages narrowed by choose when it is not nil: a skipped test
// joins the large ones that every shard skips, so a test the record does
// not know still runs.
// A package whose every fast test the selection skips has no step; dropped
// says so, one line per package, for the gate to print. round declares
// the round tier (affected.go) instead: every test of each package, no
// -short, and choose judges every package, not only selectPackages.
// The size rule reads the times of cacheDir's store over durationsFile's
// (testSeconds); fastSteps reads durationsFile alone.
func fastStepsSelected(root, cacheDir string, choose selector, round bool) (steps []*Step, dropped []string, err error) {
	packages, problem, err := testPackages(root)
	if err != nil {
		return nil, nil, err
	}
	var importPaths []string
	for _, p := range packages {
		importPaths = append(importPaths, p.importPath)
	}
	if err := checkDurationRows(root, importPaths); err != nil {
		return nil, nil, err
	}
	full, err := declareSteps(root, importPaths)
	if err != nil {
		return nil, nil, err
	}
	durations, err := testSeconds(root, cacheDir)
	if err != nil {
		return nil, nil, err
	}
	module := modulePath(root)
	steps, err = classFastSteps(full)
	if err != nil {
		return nil, nil, err
	}
	if err := fastExamples(root, steps); err != nil {
		return nil, nil, err
	}
	// A package's step is planned first and declared after every
	// selection has returned: each selection reads its package's coverage
	// record and hashes its compiled inputs, independent of the others, so
	// they run at once. One after another they took 1.7 of the 6.8 s before
	// a std edit's gate clock started, at 4 CPUs (gate-speed GS28).
	var plans []*plan
	for _, p := range packages {
		pl, err := planOf(root, p, strings.TrimPrefix(p.importPath, module+"/"), durations[p.importPath], round)
		if err != nil {
			return nil, nil, err
		}
		if pl != nil {
			plans = append(plans, pl)
		}
	}
	if choose != nil {
		var wg sync.WaitGroup
		slots := make(chan struct{}, runtime.GOMAXPROCS(0))
		for _, pl := range plans {
			if pl.whole != "" || (!round && !slices.Contains(selectPackages, pl.relative)) {
				continue
			}
			wg.Go(func() {
				slots <- struct{}{}
				defer func() { <-slots }()
				chosen := choose(pl.p, append(stepEnvironment(), pl.env...), pl.fast)
				pl.chosen = &chosen
			})
		}
		wg.Wait()
	}
	var tests []*Step
	for _, pl := range plans {
		p, env, needs, fast, large, measured := pl.p, pl.env, pl.needs, pl.fast, pl.large, pl.measured
		note := ""
		pkg := "./" + p.dir
		suffix := "fast"
		if round {
			suffix = roundTier
		}
		name := "go test " + pkg + " (" + suffix + ")"
		if pl.whole != "" {
			note = wholeNote + pl.whole
		}
		if pl.chosen != nil {
			chosen := *pl.chosen
			fast = slices.DeleteFunc(fast, func(name string) bool { return slices.Contains(chosen.skip, name) })
			large = append(large, chosen.skip...)
			note = chosen.note
			// The record skipped every fast test: the step would link
			// the test binary to run none (bindc and syntax, 14 CPU-s of
			// a std edit, gate-speed GS24). go vet type-checks the
			// package and its tests, and the edit reaches no test, so
			// there is no step.
			if len(fast) == 0 && len(chosen.skip) > 0 {
				dropped = append(dropped, fmt.Sprintf("%s: %s; no step: no test to run, and go vet type-checks the package and its tests", name, note))
				continue
			}
		}
		if len(fast) == 0 && len(large) > 0 {
			// Every test is large: a step would run none and only build
			// the test binary, which go vet type-checks. The full tier
			// runs the tests (scripts/langmigrate's TestTryParensIsIdempotent).
			continue
		}
		base := []string{"go", "test", fastFlag, "-json"}
		if round {
			base = []string{"go", "test", "-json"}
		}
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
			step := &Step{Name: name, Needs: needs, Argv: append(append(append(slices.Clone(base), testTimeoutFor(weights[i])), selection...), pkg), Env: env, ToolEnv: testTools, Observe: true, GoStore: slices.Contains(goStorePackages, pl.relative), Package: p.closure, PackageProblem: problem, Weight: weights[i], Class: classFast, Note: note}
			if len(selections) > 1 {
				step.Name = fmt.Sprintf("go test %s [%d/%d] (%s)", pkg, i+1, len(selections), suffix)
			}
			tests = append(tests, step)
		}
	}
	// The tests after the steps they need, in package order.
	return append(steps, tests...), dropped, nil
}

// fastCPUFile holds, per test package, the CPU seconds (user and system,
// its children included) of its fast-tier steps, each run alone as the
// fast tier declares it (`-record-durations -fast`): -short, without the
// large tests, one process per shard, so each shard's own setup counts.
// Only the fast tier's CPU budget reads it (TestFastTierReachOfAnEdit):
// at a CPU budget of n a set of steps cannot finish in less than its CPU
// over n, so this sum predicts the wall where serial test times do not.
const fastCPUFile = "scripts/gate/fast-cpu.json"

// fastCPUSteps are the fast-tier steps that are no package's and that the
// CPU budget counts beside the package steps: their rows in fastCPUFile
// are keyed by the step's name (`-record-durations -fast 'examples
// (default)'`). The examples step runs every default example program,
// and it runs again after every edit of the compiler (its bin/ergo) or of
// the runner's closure.
var fastCPUSteps = []string{examplesStepName}

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
	// measure runs one step after what it needs and returns its CPU.
	measure := func(step *Step) (float64, error) {
		for _, need := range step.Needs {
			if err := prepare(need); err != nil {
				return 0, err
			}
		}
		// Every package step is a go test (fastSteps), and every budgeted
		// step a go command (fastCPUSteps).
		if step.Argv[0] != "go" {
			return 0, fmt.Errorf("%s: not a go command: %q", step.Name, step.Argv)
		}
		command := exec.Command("go", step.Argv[1:]...)
		command.Env = append(stepEnvironment(), step.Env...)
		command.Dir = root
		command.Stdout = io.Discard
		command.Stderr = os.Stderr
		used, err := runBudgeted(limit, step.Name, command)
		if used.verdict != "" {
			return 0, fmt.Errorf("%s: %s", step.Name, used.verdict)
		}
		if err != nil {
			return 0, fmt.Errorf("%s: %w", step.Name, err)
		}
		state := command.ProcessState
		return (state.UserTime() + state.SystemTime()).Seconds(), nil
	}
	for _, pkg := range packages {
		// A budgeted step that is no package's (fastCPUSteps) is named
		// by its step name, and its row is that name.
		if slices.Contains(fastCPUSteps, pkg) {
			i := slices.IndexFunc(steps, func(s *Step) bool { return s.Name == pkg })
			if i < 0 {
				return fmt.Errorf("no fast-tier step %q", pkg)
			}
			// The examples step keeps a verdict cache whose key a
			// compiler edit reaches only through what the edit changes
			// in each program's legs (early cutoff, gate-speed GS8): after
			// a checker edit a program whose emitted Go and check result
			// are the same replays. So the row is a run over a warm cache,
			// what that edit costs; the first run, which fills the cache,
			// is not counted. Counted cold, every program ran: 560.7 CPU-s
			// at the batch 19+20+21 head, against 120 measured after a
			// checker edit (GS29).
			if _, err := measure(steps[i]); err != nil {
				return err
			}
			cpu, err := measure(steps[i])
			if err != nil {
				return err
			}
			table[pkg] = cpuTenths(cpu)
			continue
		}
		dir := strings.TrimPrefix(path.Clean(pkg), "./")
		cpu, ran := 0.0, 0
		for _, step := range steps {
			if step.Package == nil || step.Package.Dir != dir {
				continue
			}
			used, err := measure(step)
			if err != nil {
				return err
			}
			cpu += used
			ran++
		}
		if ran == 0 {
			return fmt.Errorf("%s: no fast-tier step runs this package", pkg)
		}
		table[module+"/"+dir] = cpuTenths(cpu)
	}
	data, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, fastCPUFile), append(data, '\n'), 0644)
}

// cpuTenths is a measured CPU time as fastCPUFile records it, in tenths of
// a second, rounded up: a step that used any CPU keeps a positive row. A
// warm `go run` of a small program uses under 0.05 s, which rounding to the
// nearest tenth recorded as 0 (merge tier r4b, batch 19).
func cpuTenths(seconds float64) float64 {
	return math.Ceil(seconds*10) / 10
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
// variables, and its KeyParts.
func packageIdentity(step *Step) []string {
	return append([]string{step.Name, fmt.Sprintf("%q", step.Argv), fmt.Sprintf("%q", step.Env)}, step.KeyParts...)
}
