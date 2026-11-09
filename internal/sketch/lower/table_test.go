package lower

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// Program tables (test-speed plan S2b). A test whose programs have fixed
// sources declares them at package level with declarePrograms. The first
// tableExe call of a test process lowers the programs of every declared
// table that the process's -test.run and -test.skip select, and links them
// all into one dispatcher (linkPrograms), so the process pays one go build
// for all of them, not one per test. Each program still runs as its own
// process. The dispatcher lives in a directory of the process
// (programTables.dir), which TestMain removes after the tests.
//
// A table that does not lower is left out of the shared build, and a shared
// build that fails is not used: each table then builds alone, through
// buildAll, in its own test, so a failure names that test and its program.
//
// TestProgramTablesAgreeWithSeparate is the equality property of this path
// against the reference path (one vetted binary per program).

// tableProgram is one declared program: its name (unique over every table)
// and its source, lowered as a program with an entry (mustLower) or as a
// test build (lowerTestBuild).
type tableProgram struct {
	name      string
	src       string
	testBuild bool
}

// entryProgram declares a program that mustLower lowers.
func entryProgram(name, src string) tableProgram { return tableProgram{name: name, src: src} }

// testBuildProgram declares a program that lowerTestBuild lowers.
func testBuildProgram(name, src string) tableProgram {
	return tableProgram{name: name, src: src, testBuild: true}
}

func (p tableProgram) lower() (*lowered, error) {
	if p.testBuild {
		return lowerTestBuildErr(p.name, p.src)
	}
	o := lowerSource(p.name, p.src, true)
	switch {
	case o.err != nil:
		return nil, fmt.Errorf("%s: %v\n%s", p.name, o.err, p.src)
	case len(o.diags) > 0:
		return nil, fmt.Errorf("%s: checker diagnostics %v\n%s", p.name, o.diags, p.src)
	case o.panicked != nil:
		return nil, fmt.Errorf("%s: lowering panicked: %v\n%s", p.name, o.panicked, p.src)
	case len(o.refused) > 0:
		return nil, fmt.Errorf("%s: refused %+v\n%s", p.name, o.refused, p.src)
	}
	return o, nil
}

// programTable is the programs one test declares.
type programTable struct {
	test     string
	programs []tableProgram
	// Set by the shared build.
	built   bool
	exes    map[string]string
	lowered map[string]*lowered
}

var programTables struct {
	sync.Mutex
	all    []*programTable
	byName map[string]*programTable
	dir    string
	// builds counts the shared builds of this process.
	builds int
}

// declarePrograms declares the programs test runs. It panics on a program
// name another table declares: the dispatcher holds one program per name.
func declarePrograms(test string, programs ...tableProgram) *programTable {
	tb := &programTable{test: test, programs: programs}
	if programTables.byName == nil {
		programTables.byName = map[string]*programTable{}
	}
	for _, p := range programs {
		if other := programTables.byName[p.name]; other != nil {
			panic(fmt.Sprintf("program table of %s: %s is already declared by %s", test, p.name, other.test))
		}
		programTables.byName[p.name] = tb
	}
	programTables.all = append(programTables.all, tb)
	return tb
}

// tableExe returns the executable of the declared program name, building
// the tables on first use.
//
// It builds Go because its callers run the program: what a built program
// prints and how it exits is their question.
func tableExe(t *testing.T, name string) string {
	t.Helper()
	fastTierGuard(t, "a program table links Go programs")
	programTables.Lock()
	defer programTables.Unlock()
	tb := programTables.byName[name]
	if tb == nil {
		t.Fatalf("%s: no program table declares it (declarePrograms)", name)
	}
	if !tb.built {
		buildTables(t, tb, false)
	}
	return tb.exes[name]
}

// tableExes is tableExe for each of names, by name.
func tableExes(t *testing.T, names ...string) map[string]string {
	t.Helper()
	exes := map[string]string{}
	for _, name := range names {
		exes[name] = tableExe(t, name)
	}
	return exes
}

// buildEveryTable builds every declared table that is not built yet, in one
// shared build.
func buildEveryTable(t *testing.T) {
	t.Helper()
	fastTierGuard(t, "a program table links Go programs")
	programTables.Lock()
	defer programTables.Unlock()
	for _, tb := range programTables.all {
		if !tb.built {
			buildTables(t, tb, true)
			return
		}
	}
}

// buildTables builds want with every other unbuilt table the run selects
// (all of them with every), in one shared build. If want does not lower or
// the shared build fails, want builds alone in t (a table built alone is
// not marked built: its directory is t's).
func buildTables(t *testing.T, want *programTable, every bool) {
	t.Helper()
	var batch []*programTable
	var programs []*lowered
	failure := ""
	for _, tb := range programTables.all {
		if tb.built || (tb != want && !every && !tableSelected(tb.test)) {
			continue
		}
		lowered, err := tb.lowerAll()
		if err != nil {
			if tb == want {
				failure = err.Error()
			}
			continue // its own test reports it
		}
		batch = append(batch, tb)
		for _, p := range tb.programs {
			programs = append(programs, lowered[p.name])
		}
		tb.lowered = lowered
	}
	if failure == "" {
		var exes map[string]string
		exes, failure = sharedBuild(programs)
		if exes != nil {
			programTables.builds++
			for _, tb := range batch {
				tb.built = true
				tb.exes = map[string]string{}
				for _, p := range tb.programs {
					tb.exes[p.name] = exes[p.name]
				}
			}
			return
		}
	}
	t.Logf("the shared build of the program tables failed; %s builds alone:\n%s", want.test, firstLines(failure, 40))
	var alone []*lowered
	for _, p := range want.programs {
		o, err := p.lower()
		if err != nil {
			t.Fatal(err)
		}
		alone = append(alone, o)
	}
	exes := buildAll(t, alone, true)
	if exes == nil {
		t.FailNow()
	}
	want.exes = exes
}

func (tb *programTable) lowerAll() (map[string]*lowered, error) {
	out := map[string]*lowered{}
	for _, p := range tb.programs {
		o, err := p.lower()
		if err != nil {
			return nil, err
		}
		out[p.name] = o
	}
	return out, nil
}

// sharedBuild links programs in the process's table directory, and returns
// their executables, or the failure.
func sharedBuild(programs []*lowered) (exes map[string]string, failure string) {
	if programTables.dir == "" {
		dir, err := os.MkdirTemp("", "ergo-lower-tables-")
		if err != nil {
			return nil, err.Error()
		}
		programTables.dir = dir
	}
	rec := &tableRecorder{root: programTables.dir}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(tableAbort); !ok {
				panic(r)
			}
			exes, failure = nil, rec.log.String()
		}
	}()
	exes = linkPrograms(rec, programs)
	if rec.failed || exes == nil {
		return nil, rec.log.String()
	}
	return exes, ""
}

// removeProgramTables removes the table directory (TestMain).
func removeProgramTables() {
	if programTables.dir != "" {
		_ = os.RemoveAll(programTables.dir)
	}
}

// tableSelected reports whether the run's -test.run and -test.skip select
// the top-level test name. A pattern it cannot read selects every test: a
// wrong guess only builds a table that no test of the run uses.
func tableSelected(test string) bool {
	return flagMatches("test.run", test, true) && !flagMatches("test.skip", test, false)
}

func flagMatches(name, test string, empty bool) bool {
	pattern := flagValue(name)
	if pattern == "" {
		return empty
	}
	top, _, _ := strings.Cut(pattern, "/")
	re, err := regexp.Compile(top)
	if err != nil {
		return empty
	}
	return re.MatchString(test)
}

// flagValue is the value of the test flag name, or "" when it is not set.
func flagValue(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// tableRecorder is the testing.TB of a shared build: it records failures
// instead of failing a test that did not cause them, and makes its
// temporary directories in the process's table directory, which outlives
// every test. Only the methods below are called (goModuleDir, linkPrograms,
// buildTwins, symlinkProgram).
type tableRecorder struct {
	testing.TB
	root   string
	failed bool
	log    strings.Builder
}

type tableAbort struct{}

func (r *tableRecorder) Helper()      {}
func (r *tableRecorder) Name() string { return "program tables" }
func (r *tableRecorder) TempDir() string {
	dir, err := os.MkdirTemp(r.root, "m")
	if err != nil {
		r.Fatal(err)
	}
	return dir
}
func (r *tableRecorder) Error(args ...any) { r.failed = true; fmt.Fprintln(&r.log, args...) }
func (r *tableRecorder) Errorf(format string, args ...any) {
	r.failed = true
	fmt.Fprintf(&r.log, format+"\n", args...)
}
func (r *tableRecorder) Logf(format string, args ...any) { fmt.Fprintf(&r.log, format+"\n", args...) }
func (r *tableRecorder) Fatal(args ...any)               { r.Error(args...); panic(tableAbort{}) }
func (r *tableRecorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	panic(tableAbort{})
}

// TestProgramTablesNameTheirTests: each table names a top-level test of
// this package, so a run's -test.run selects the table with its test. A
// wrong name fails no other test: the table then builds alone, in a go
// build of its own.
func TestProgramTablesNameTheirTests(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]bool{}
	for _, name := range files {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && strings.HasPrefix(fd.Name.Name, "Test") {
				tests[fd.Name.Name] = true
			}
		}
	}
	for _, tb := range programTables.all {
		if !tests[tb.test] {
			t.Errorf("a program table names %s, which is no test of this package", tb.test)
		}
	}
}

// tablesSeed replays one seed of TestProgramTablesAgreeWithSeparate.
var tablesSeed = flag.Uint64("tables.seed", 0, "replay one seed of TestProgramTablesAgreeWithSeparate")

// TestProgramTablesAgreeWithSeparate is the property of the table path
// (S2b): every declared program, built in the process's one shared build,
// runs as its own reference binary does (exit code, stdout and stderr), in
// a seeded order and with seeded arguments, each run in a fresh directory.
// The tables are built in a subtest, so an executable that lived in a
// test's own directory would be gone when the runs start. Building every
// table is at most one go build, a second use builds nothing, and a whole
// run of the package (no -run, no -skip) builds the tables once. A program whose
// own binary gives two different runs is not compared. A failing seed
// prints the program; -tables.seed=N replays it.
//
// It builds Go because its question is the built programs' runs, under two
// link paths.
func TestProgramTablesAgreeWithSeparate(t *testing.T) {
	needsBuild(t)
	var names []string
	for name := range programTables.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	exes := map[string]string{}
	programTables.Lock()
	before := programTables.builds
	programTables.Unlock()
	t.Run("build", func(t *testing.T) {
		buildEveryTable(t)
		for _, name := range names {
			exes[name] = tableExe(t, name)
		}
	})
	if t.Failed() {
		return
	}
	programTables.Lock()
	builds := programTables.builds
	var programs []*lowered
	for _, name := range names {
		tb := programTables.byName[name]
		if !tb.built {
			programTables.Unlock()
			t.Fatalf("%s's table was not in a shared build", tb.test)
		}
		programs = append(programs, tb.lowered[name])
	}
	programTables.Unlock()
	if builds-before > 1 {
		t.Errorf("building every table made %d shared builds; want at most 1", builds-before)
	}
	if flagValue("test.run") == "" && flagValue("test.skip") == "" && builds != 1 {
		t.Errorf("a whole run made %d shared builds of the program tables; want 1", builds)
	}
	separate := buildSeparate(t, programs)
	if separate == nil {
		t.Fatal("the reference path does not build the tables' programs")
	}
	seed := uint64(1)
	if *tablesSeed != 0 {
		seed = *tablesSeed
	}
	r := rand.New(rand.NewPCG(seed, 0x7461626c6573))
	words := []string{"--seed", "1", "one", "two words"}
	compared, unstable := 0, 0
	for _, i := range r.Perm(len(names)) {
		name := names[i]
		args := make([]string, r.IntN(3))
		for j := range args {
			args[j] = words[r.IntN(len(words))]
		}
		want := tableRun(t, separate[name], args)
		got := tableRun(t, exes[name], args)
		if got == want {
			compared++
			continue
		}
		if again := tableRun(t, separate[name], args); again != want {
			unstable++
			continue
		}
		t.Errorf("seed %d (-tables.seed=%d): %s %q runs differently from the table build\ntable: %s\nown binary: %s\nsource:\n%s", seed, seed, name, args, got, want, programTables.byName[name].lowered[name].src)
	}
	if compared < len(names)/2 {
		t.Errorf("only %d of %d programs compared (%d unstable): the property lost its domain?", compared, len(names), unstable)
	}
	if again := tableExe(t, names[0]); again != exes[names[0]] || programTables.builds != builds {
		t.Errorf("a second use rebuilt the tables (%d builds, then %d)", builds, programTables.builds)
	}
	t.Logf("seed %d: %d of %d programs compared, %d unstable, %d shared builds", seed, compared, len(names), unstable, builds)
}

// tableRun runs exe with args, no input and a deadline, in a fresh
// directory, and returns its exit code, stdout and stderr as one string.
func tableRun(t *testing.T, exe string, args []string) string {
	t.Helper()
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = t.TempDir()
	cmd.Stdin = bytes.NewReader(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd)
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("exit %d\nstdout %q\nstderr %q", code, stdout.String(), stderr.String())
}
