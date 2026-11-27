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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/scripts/mutants/schemata"
	"ergo.local/prototype/scripts/mutants/suitename"
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
	// copy; Tests is the number of one mutant's tests run at once, until
	// the first kill (judge.go); Procs is each child's GOMAXPROCS and go
	// -p. At most Workers × Tests children run at once, each under Memory.
	Workers int
	Tests   int
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
	// CoverStore, when set, is the directory of the coverage store
	// (coverstore.go): each suite's segment is taken from it where the
	// suite's key holds, and the measured ones are stored. Empty: every
	// suite is measured.
	CoverStore string
	// CoverDeadline bounds each child of the coverage pass, which has no
	// earlier run to derive a deadline from.
	CoverDeadline time.Duration
	Log           io.Writer
	// Sources check a mutant of a source file that go build does not
	// compile (an .ergo row) before any test runs.
	Sources []SourceCheck
	// TestFloor is the least deadline of a test of the mutant pass
	// (default testFloor).
	TestFloor time.Duration
	// JournalDir, when set, keeps each tree's journal (journal.go): a run
	// killed before its end resumes there. Empty: nothing is kept.
	JournalDir string
	// Progress, when set, gets the results so far after each verdict
	// (blank where a row is not judged yet).
	Progress func([]Result)
	// Schemata folds the Go rows that can fold into one tree whose suites
	// build once (schemata.go); the others, and every row it cannot judge
	// beyond doubt, take the isolated path. Off: every row is isolated.
	Schemata bool
	// Builds, when set, counts the run's suite builds.
	Builds *Builds
}

// SourceCheck validates the mutants of one kind of source file. A mutated
// file that the formatter would rewrite (FK7a), or that the compiler
// refuses with a diagnostic the unmutated file does not have (FK12a), fails
// every test that formats or checks it for that reason alone: such a
// mutant is invalid, never killed, unless its row declares that the
// refusal is its point (Mutant.Refused). Format and Check are commands;
// their words may hold {bin:TOOL} (Tool, built from the unmutated copy),
// {file} (the mutated file) and {dir} (its directory). Format exits 0 when
// the file is formatted. Check prints a JSON object whose "diagnostics"
// hold a "code" each; output that is not one counts as a refusal when the
// command fails.
type SourceCheck struct {
	Ext    string
	Tool   Tool
	Format []string
	Check  []string
}

// Tool is a binary that suites run, built from the checkout (with
// coverage in the coverage pass).
type Tool struct {
	Name    string
	Package string
	Flags   []string
	// Env is set over the runner's environment for the tool's build (the
	// ergo tool's: ergobuild.Cgo).
	Env []string
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
	// Tags are a Go suite's build tags (go test -tags); Only, when set,
	// are the only tests of the suite that the runner runs. Named are the
	// tests of the suite the ledgers name as killers: a Go suite that lacks
	// one is refused, and a command suite runs them besides the ones List
	// gives (a ledger's killer may be an example that is not a default).
	Tags  string
	Only  []string
	Named []string
	// Module is the directory, relative to the checkout, of the nested Go
	// module that holds Package (internal/selfhostcheck/testdata/selfhost);
	// empty for the checkout's own module. Package is then relative to it,
	// and the suite is built there without coverage: -coverpkg names the
	// checkout's packages, which another module cannot cover, so only its
	// named tests judge a mutant (killerIDs).
	Module string
	// Commands, set for the suite named command alone, are the ledgers'
	// commands by name (Ledger.Commands): its tests, which run only where
	// a row names them.
	Commands map[string][]string
	// Families are the Named tests of a Go suite that are no test but a
	// family split into parts, TestXPart0, TestXPart1 … (a test split to
	// fit the gate's step limit): the runner runs a family as every one of
	// its parts in one process, so a failing part kills, and a row names
	// the family, never a part number.
	Families []string
}

// measured says whether the coverage pass runs the listed test n of the
// Go suite s: every test, or with Only those it names, a family in Only
// as its parts (Families).
func measured(s *Suite, n string) bool {
	if !strings.HasPrefix(n, "Test") {
		return false
	}
	if s.Only == nil || slices.Contains(s.Only, n) {
		return true
	}
	return slices.ContainsFunc(s.Families, func(f string) bool {
		return slices.Contains(s.Only, f) && regexp.MustCompile(partsPattern(f)).MatchString(n)
	})
}

// partsPattern matches the parts of the test family name and nothing else.
func partsPattern(name string) string {
	return "^" + regexp.QuoteMeta(name) + "Part[0-9]+$"
}

// commandSuite is the name of the suite of the ledgers' commands.
const commandSuite = "command"

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
	Uncovered = "uncovered" // no test runs its lines, and the ledger names none
	Stale     = "stale"     // its edit does not apply: report it to its origin
	Invalid   = "invalid"   // the mutated tree does not build or the mutated file does not compile: report it to its origin
	Unjudged  = "unjudged"  // a test that covers it or the ledger names fails on the unmutated tree
	Hung      = "hung"      // no test killed it, and some test ran into its timeout: fix the row or the test
	Compiles  = "compiles"  // -compiles only: the mutated file compiles, and no test ran
)

// Result is the verdict on one mutant.
type Result struct {
	Ledger   string   `json:"ledger"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Line     int      `json:"line,omitempty"`
	Killer   string   `json:"killer,omitempty"`
	Reason   string   `json:"reason,omitempty"` // fail or memory (a timeout is no kill: Hung)
	Killers  []string `json:"killers,omitempty"`
	Covering int      `json:"covering"`
	// Ran are the tests that ran to their end on the mutant (the receipt
	// of its work), Stopped those the first kill stopped, Hung those of
	// Ran that failed only by their timeout.
	Ran     []string `json:"ran,omitempty"`
	Stopped []string `json:"stopped,omitempty"`
	Hung    []string `json:"hung,omitempty"`
	// NotStarted are the tests whose binary ended before the test started
	// (startLine): no kill, and the row is invalid unless another test
	// killed it.
	NotStarted []string `json:"not_started,omitempty"`
	Seconds    float64  `json:"seconds"`
	Detail     string   `json:"detail,omitempty"`
	// checked is set once phase A applied the row and its source checks
	// passed: phase B does not check it again.
	checked bool
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
	if e.cfg.Builds == nil {
		e.cfg.Builds = &Builds{}
	}
	results, err := e.judgeAll(ctx, jobs)
	e.logf("builds: %d of mutated copies, %d of the schemata tree", e.cfg.Builds.Rows.Load(), e.cfg.Builds.Tree.Load())
	return results, e.known(), err
}

// Measure prepares the coverage of the jobs' suites as Run does (taking
// and filling the coverage store, Config.CoverStore) and judges nothing.
func Measure(ctx context.Context, cfg Config, jobs []Job) (*coverage, error) {
	e, err := covered(ctx, cfg, jobs)
	if err != nil {
		return nil, err
	}
	return e.cov, nil
}

// covered is the engine of the jobs with its coverage prepared.
func covered(ctx context.Context, cfg Config, jobs []Job) (*engine, error) {
	e, err := newEngine(cfg)
	if err != nil {
		return nil, err
	}
	var ledgers []string
	for _, j := range jobs {
		ledgers = append(ledgers, j.Path)
	}
	if err := e.load(ledgers); err != nil {
		return nil, err
	}
	if err := e.prepareCoverage(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

type engine struct {
	cfg         Config
	compileOnly bool // CompileRows: each row is applied and compiled, no test runs
	module      string
	goRoot      string // the module's toolchain, whose bin is first on every child's PATH
	files       []treeFile
	key         string
	cov         *coverage
	logMu       sync.Mutex
	// plain are the coverage pass's test binaries built without coverage,
	// by suite name, built the first time one is needed (plainBinary).
	plainMu sync.Mutex
	plain   map[string]*plainBuild
	// base are the named tests' runs on the unmutated tree (phase A,
	// judge.go), builds the first time each build took on this run.
	base    map[string]*covTest
	buildMu sync.Mutex
	builds  map[string]float64
	workers []*worker
	schema  *schemaTree
}

type plainBuild struct {
	once sync.Once
	err  error
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
	if cfg.Tests < 1 {
		cfg.Tests = 1
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
	// The copies are made under the work directory, each emptied first
	// (copyTree) and restored by removing what the checkout lacks
	// (restoreUnder): a checkout inside it could be one of them.
	if inside(cfg.Root, cfg.Work) {
		return nil, fmt.Errorf("the checkout %s is inside the work directory %s", cfg.Root, cfg.Work)
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
	// The toolchain go test would put first on PATH (env): the one that
	// built this runner (go run and go test build it with the module's).
	// The go first on PATH may be another, which GOTOOLCHAIN=local keeps
	// from switching (the host's go1.26.5 refused the module's go 1.27).
	if root := toolchainRoot(); root != "" {
		e.goRoot = root
	} else {
		cmd := exec.Command("go", "env", "GOROOT")
		cmd.Dir, cmd.Env = cfg.Root, goEnviron()
		if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
			e.goRoot = strings.TrimSpace(string(out))
		}
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
		fmt.Fprintf(h, " suite %s %s %s %q %q %q %q %q %q %q", s.Name, s.Module, s.Package, s.Command, s.Env, s.Tags, s.Only, s.Named, s.Commands, s.Families)
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
	return commitCopy(dst, e.files)
}

// commitCopy makes the copy at dst a Git repository whose one commit holds
// the copied files. The tests that read the repository (internal/residue's
// rules run git ls-files) then see the checkout's file list in the copy and
// can kill a mutant; without it they fail on every tree. The commit also
// keeps go build's VCS stamp working. The user's Git configuration is left
// out: a signing or hook setting must not reach the copy. The copy's own
// configuration turns Git's automatic maintenance off (noMaintenance), so
// no git a test runs in it starts a repack: one ran 13 and 40 minutes on a
// nearly full disk (wf10, 2026-11-19).
// noMaintenance turns Git's automatic gc and maintenance off through the
// environment, for the gits the runner and its children run. The runner's
// own gits elsewhere (log, status, rev-parse, ls-files) are reads that
// start no maintenance.
var noMaintenance = []string{
	"GIT_CONFIG_COUNT=2",
	"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
	"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
}

func commitCopy(dst string, files []treeFile) error {
	var names bytes.Buffer
	for _, f := range files {
		names.WriteString(f.path)
		names.WriteByte(0)
	}
	steps := [][]string{
		{"init", "-q"},
		{"config", "gc.auto", "0"},
		{"config", "maintenance.auto", "false"},
		{"add", "-f", "--pathspec-from-file=-", "--pathspec-file-nul"},
		{"-c", "user.name=mutants", "-c", "user.email=mutants@invalid", "commit", "-q", "--no-verify", "-m", "mutants copy"},
	}
	for _, args := range steps {
		cmd := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
		cmd.Dir = dst
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		cmd.Env = append(cmd.Env, noMaintenance...)
		if args[0] == "add" {
			cmd.Stdin = bytes.NewReader(names.Bytes())
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s in the copy %s: %w\n%s", args[0], dst, err, out)
		}
	}
	return nil
}

// verifyCopy checks that every file a mutant touched in the copy at dst is
// back to the checkout's bytes: a file a row edited, and every file under
// a regenerate step's folder (for `go run ./scripts/linked`, the whole
// tree). A symbolic link is compared by its target, as treeFile keeps it:
// read through the link, every link under a regenerate folder "differed"
// (conformance/upstream/foldkit-d21db423/CLAUDE.md -> AGENTS.md; IR1, PF).
// Each changed file is named with its first differing line.
func (e *engine) verifyCopy(dst string, touched map[string]bool) error {
	var bad []string
	for _, f := range e.files {
		if !touched[f.path] {
			continue
		}
		p := filepath.Join(dst, filepath.FromSlash(f.path))
		var data []byte
		var err error
		if f.mode&fs.ModeSymlink != 0 {
			var target string
			target, err = os.Readlink(p)
			data = []byte(target)
		} else {
			data, err = os.ReadFile(p)
		}
		if err == nil && bytes.Equal(data, f.data) {
			continue
		}
		bad = append(bad, f.path+" ("+firstDiff(f.data, data, err)+")")
	}
	if len(bad) > 0 {
		return fmt.Errorf("a mutant was not restored in %s: %s", dst, strings.Join(bad, "; "))
	}
	return nil
}

// firstDiff is the first line where the copy's bytes (got, or its read
// error) differ from the checkout's (want).
func firstDiff(want, got []byte, err error) string {
	if err != nil {
		return "in the copy: " + err.Error()
	}
	w, g := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b || i >= len(w) || i >= len(g) {
			return fmt.Sprintf("line %d: checkout %.120q, copy %.120q", i+1, a, b)
		}
	}
	return "same lines, other bytes"
}

// env is the environment of every child: the runner's own, with the
// thread share, and extra. GOFLAGS is empty: a go command's default -p is
// GOMAXPROCS, and a set GOFLAGS makes ergo build refuse its direct path,
// which failed the cmd/ergo tests that require it on the unmutated tree
// (FT11b: TestDirectBuildStartsNoGoCommand, TestBuildGoesThroughIR). The
// module's toolchain is first on PATH, as go test puts it: a test binary
// run directly found the host's go first, and cmd/ergo's
// TestLRUCountsAsHandGo failed on the unmutated tree ("Go go1.27.0 has no
// port of the IR compiler patch"), so opt9-fail-around-noresult was
// unjudged (GT-BOUND).
func (e *engine) env(extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "GOCOVERDIR", "GOMAXPROCS", "GOFLAGS", "GOWORK", MutantVariable, schemata.TraceEnv, coverGoVariable, embedObserveVariable:
			continue
		case "PATH":
			if e.goRoot != "" {
				kv = "PATH=" + filepath.Join(e.goRoot, "bin") + string(os.PathListSeparator) + value
			}
		case "GOROOT":
			// The go first on PATH finds its own root. A GOROOT of another
			// toolchain fails every build ("compile: version go1.27.4 does
			// not match go tool version go1.27.0").
			continue
		}
		out = append(out, kv)
	}
	procs := strconv.Itoa(e.cfg.Procs)
	out = append(out, "GOMAXPROCS="+procs, "GOWORK=off")
	out = append(out, noMaintenance...)
	return append(out, extra...)
}

// outcome is how a child ended. Reason is empty when it exited 0. Timeout
// is set when its failure is only a timeout: the runner's deadline, or go
// test's own -test.timeout panic.
type outcome struct {
	reason  string // fail, deadline, memory
	seconds float64
	tail    string
	timeout bool
	// started is set when the child printed the start line it was run
	// with (execStarted), or had none to print.
	started bool
	// log is the file holding the child's whole output when it failed.
	log string
}

// timeoutPanic is how a go test binary says that its -test.timeout ran
// out (testing.(*M).startAlarm).
const timeoutPanic = "panic: test timed out after "

// toolchainRoot is the root of the toolchain that built this runner (go
// run and go test build it with the module's), or "" when its go is gone.
func toolchainRoot() string {
	//lint:ignore SA1019 the build-time root is the toolchain wanted here
	root := runtime.GOROOT()
	if _, err := os.Stat(filepath.Join(root, "bin", "go")); root == "" || err != nil {
		return ""
	}
	return root
}

// goCommand is the go of toolchainRoot, else the go first on PATH.
func goCommand() string {
	if root := toolchainRoot(); root != "" {
		return filepath.Join(root, "bin", "go")
	}
	return "go"
}

// goEnviron is this process's environment without GOROOT, for the go
// commands the runner starts itself: goCommand finds its own root, and an
// inherited GOROOT of another toolchain would make it refuse to run (as
// env drops it for every child).
func goEnviron() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GOROOT=") })
}

// lookPathIn finds a bare command name on the PATH of env, the child's,
// where exec.Command looks on this process's: the toolchain the runner puts
// first on a child's PATH (env) runs its go, not the host's go, which
// GOTOOLCHAIN=local keeps from switching (go1.26.5 refused the module's go
// 1.27). A name with a separator, or one not found there, is left as is.
func lookPathIn(name string, env []string) string {
	if strings.ContainsRune(name, filepath.Separator) {
		return name
	}
	for _, kv := range env {
		value, ok := strings.CutPrefix(kv, "PATH=")
		if !ok {
			continue
		}
		for _, dir := range filepath.SplitList(value) {
			path := filepath.Join(dir, name)
			if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				return path
			}
		}
	}
	return name
}

// exec runs argv in dir as the leader of its own process group, under the
// memory limit and the deadline, and stops the whole group when it ends.
func (e *engine) exec(ctx context.Context, dir string, argv, env []string, deadline time.Duration) outcome {
	return e.execStarted(ctx, dir, argv, env, deadline, "")
}

// execTokenVariable carries, to every process a child of exec starts, the
// token of that child: the processes it leaves in groups of their own are
// found by it when it ends (reapAfter).
const execTokenVariable = "ERGO_MUTANTS_EXEC"

var execTokens atomic.Uint64

// execStarted is exec of a child that prints startLine when the test it
// runs starts ("" for none): outcome.started says whether it did.
func (e *engine) execStarted(ctx context.Context, dir string, argv, env []string, deadline time.Duration, startLine string) outcome {
	start := time.Now()
	out := &tail{needles: []string{timeoutPanic, startLine}}
	cmd := exec.Command(lookPathIn(argv[0], env), argv[1:]...)
	cmd.Dir = dir
	// The token marks every process the child starts, for reapAfter.
	token := strconv.FormatUint(execTokens.Add(1), 10)
	cmd.Env = append(slices.Clip(env), execTokenVariable+"="+token)
	cmd.Stdout = out
	cmd.Stderr = out
	// The whole output goes to a file as well, kept when the child fails:
	// the tail drops the middle, where a test's own error may stand
	// (keepRed).
	var full *os.File
	if e.cfg.Work != "" {
		if err := os.MkdirAll(filepath.Join(e.cfg.Work, "out"), 0o755); err == nil {
			full, _ = os.Create(filepath.Join(e.cfg.Work, "out", token+".log"))
		}
	}
	if full != nil {
		defer full.Close()
		w := io.MultiWriter(out, full)
		cmd.Stdout, cmd.Stderr = w, w
	}
	// Read every byte before tail truncates the display. The shared writer
	// keeps stdout and stderr ordered, as they were before the watcher.
	watch := proclimit.Watch(cmd)
	// A process the child leaves behind may hold the output pipe: Wait
	// gives up on it a second after the child exits, and the group stop
	// below ends it.
	cmd.WaitDelay = time.Second
	ownGroup(cmd)
	subreaper()
	unlock := starting()
	if err := proclimit.Start(cmd, e.cfg.Memory); err != nil {
		unlock()
		return outcome{reason: "fail", tail: err.Error()}
	}
	track(cmd.Process.Pid)
	unlock()
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
	// The leader is gone; stop what it left in its group, and what it
	// left in groups of their own (reap_linux.go, GT-REAP).
	stopGroup(cmd.Process)
	exited = true
	mu.Unlock()
	untrack(cmd.Process.Pid)
	reapAfter(token)
	o := outcome{seconds: time.Since(start).Seconds(), tail: out.String()}
	o.reason = childReason(err, timedOut, e.cfg.Memory, watch.Stderr())
	o.timeout = o.reason == "deadline" || o.reason == "fail" && out.saw(0)
	if o.timeout {
		// One reason whichever clock fired: the test's own -test.timeout
		// and the runner's deadline race under load (MUTANTS-MODEL-TIMING).
		o.reason = "timeout"
	}
	o.started = startLine == "" || out.saw(1)
	if ctx.Err() != nil && o.reason != "" {
		o.reason, o.timeout = "interrupted", false
	}
	if full != nil {
		if o.reason == "" {
			os.Remove(full.Name())
		} else {
			o.log = full.Name()
		}
	}
	return o
}

// redVariable names the folder where keepRed puts the whole output of a
// test that fails on the unmutated tree; the default is the user cache's
// ergo-mutants/red.
const redVariable = "ERGO_MUTANTS_RED"

// keepRed copies the whole output of id's failing run on the unmutated
// tree (o.log, in the work folder the run removes) to a file that stays,
// and returns its path; "" when there is none. A red baseline judged
// nothing, and its cause was lost with the tail's middle (OWN1's
// TestValueRuleEveryBodyKind, SUPERVISE-2's TestStdActorLawsRun).
func keepRed(id string, o outcome) string {
	if o.log == "" {
		return ""
	}
	dir := os.Getenv(redVariable)
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(cache, "ergo-mutants", "red")
	}
	data, err := os.ReadFile(o.log)
	if err != nil || os.MkdirAll(dir, 0o755) != nil {
		return ""
	}
	name := strings.NewReplacer("/", "_", ":", "-", "[", "_", "]", "_").Replace(id)
	path := filepath.Join(dir, time.Now().UTC().Format("20060102T150405")+"-"+name+".log")
	if os.WriteFile(path, data, 0o644) != nil {
		return ""
	}
	return path
}

// startLine is what a test binary run with -test.v prints when the
// top-level test starts: a Go test that fails without it never ran (a
// usage error, a binary that died in its init, no test of that name), so
// its failure kills nothing (GT-NOTESTS). A command suite prints none.
func startLine(s *Suite, test string) string {
	if s.Package == "" {
		return ""
	}
	if slices.Contains(s.Families, test) {
		return "=== RUN   " + test + "Part" // any part
	}
	return "=== RUN   " + test + "\n"
}

// tail keeps the first and the last bytes a child wrote: a crash names its
// cause first, a failing test last.
type tail struct {
	mu      sync.Mutex
	head    []byte
	buf     []byte
	dropped bool
	// needles are texts to watch for wherever the display truncates (an
	// empty one is not watched): seen[i] is set once needles[i] was
	// written; carry is the end of the last write, for a line cut between
	// two writes.
	needles []string
	seen    []bool
	carry   []byte
}

// saw says whether needles[i] was written.
func (t *tail) saw(i int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return i < len(t.seen) && t.seen[i]
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
	if t.seen == nil {
		t.seen = make([]bool, len(t.needles))
		for i, n := range t.needles {
			t.seen[i] = n == "" // nothing to watch for
		}
	}
	if slices.Contains(t.seen, false) {
		scan := append(t.carry, p...)
		longest := 0
		for i, n := range t.needles {
			longest = max(longest, len(n))
			if n != "" && !t.seen[i] {
				t.seen[i] = bytes.Contains(scan, []byte(n))
			}
		}
		t.carry = append(t.carry[:0], scan[max(0, len(scan)-longest):]...)
	}
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
	words := append(slices.Clone(s.Env), s.Command...)
	for _, name := range slices.Sorted(maps.Keys(s.Commands)) {
		words = append(words, s.Commands[name]...)
	}
	for _, w := range words {
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

// coverMode is the counter mode of every coverage build of the coverage
// pass: the test binaries, the tools, and the programs a test builds
// (internal/sketch/build's coverage build, lower's dispatcher), whose
// counters share the test's GOCOVERDIR, and go tool covdata refuses two
// modes in one directory. It is atomic because a coverage build writes its
// counters itself when a signal ends it (runtime/coverage.WriteCountersDir
// needs atomic counters; GATE2 GT7b).
const coverMode = "atomic"

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
		argv = append(argv, "-cover", "-covermode="+coverMode, "-coverpkg="+strings.Join(append(slices.Clone(e.cfg.Cover), t.Package), ","))
	}
	argv = append(append(argv, t.Flags...), "-o", filepath.Join(bin, name), t.Package)
	return e.exec(ctx, root, argv, append(e.env(), t.Env...), deadline), nil
}

func (e *engine) buildSuite(ctx context.Context, root, bin string, s *Suite, cover bool, deadline time.Duration) outcome {
	argv := []string{"go", "test", "-c"}
	if cover && len(e.cfg.Cover) > 0 && s.Module == "" {
		argv = append(argv, "-cover", "-covermode="+coverMode, "-coverpkg="+strings.Join(e.cfg.Cover, ","))
	}
	if s.Tags != "" {
		argv = append(argv, "-tags="+s.Tags)
	}
	argv = append(argv, "-o", filepath.Join(bin, s.Name+".test"), s.Package)
	return e.exec(ctx, filepath.Join(root, filepath.FromSlash(s.Module)), argv, e.env(), deadline)
}

// argv returns how to run one test of a suite, and where. A Go test binary
// gets a -test.timeout inside the run's deadline, as go test always gives
// one: a test that bounds its children by t.Deadline (internal/closure's
// TestReapEndedLeavesTheHelperAndRunningChildren) fails at once without
// it, so it judged nothing (MUT-names r2). Its own timeout also names the
// stuck test before the runner stops the run.
// ergoCacheVariable is the ergo CLI's emission cache root (build.RootVariable).
const ergoCacheVariable = "ERGO_CACHE"

func (e *engine) argv(s *Suite, root, bin, test string, deadline time.Duration) ([]string, string, []string) {
	var env []string
	for _, kv := range s.Env {
		env = append(env, expand(kv, bin, root, test))
	}
	// A test of the checkout's module gets an emission cache of its own
	// from internal/testinputs; one of a nested module (hosts/mygo) has no
	// such harness, and the ergo it starts refuses the user's cache under
	// a Go test. The runner gives each such test a cache under its work
	// directory, unless the ledger's env names one (MUT-MODULES).
	if s.Module != "" && s.Package != "" && !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, ergoCacheVariable+"=") }) {
		env = append(env, ergoCacheVariable+"="+filepath.Join(bin, "ergo-cache", filepath.FromSlash(s.Name), test))
	}
	if s.Package != "" {
		run := "^" + regexp.QuoteMeta(test) + "$"
		if slices.Contains(s.Families, test) {
			run = partsPattern(test)
		}
		return []string{filepath.Join(bin, s.Name+".test"), "-test.run=" + run, "-test.count=1", "-test.v", "-test.timeout=" + (deadline * 9 / 10).String()},
			filepath.Join(root, filepath.FromSlash(s.Module), filepath.FromSlash(s.Package)), env
	}
	var argv []string
	words := s.Command
	if s.Commands != nil {
		words = s.Commands[test]
	}
	for _, w := range words {
		argv = append(argv, expand(w, bin, root, test))
	}
	return argv, root, env
}

// splitID splits a killer id SUITE:TEST into its suite and its test. A note
// after the test, past a space ("TestX (ERGO_CLOSURE_SWEEP=cmd/ergo)"), is
// for the reader and is not part of the test's name.
func splitID(id string) (string, string) {
	return suitename.Split(id)
}

// killerIDs are a mutant's named killers and anchor tests as the runner
// names its tests, SUITE:TEST, without their notes.
func killerIDs(m *Mutant) []string {
	var out []string
	for _, id := range append(slices.Clone(m.Killers), anchorTests(m)...) {
		suite, name := splitID(id)
		if id := suite + ":" + name; !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// prepareCoverage reads the coverage file, the cached one of this tree, or
// measures it.
func (e *engine) prepareCoverage(ctx context.Context) error {
	path := e.cfg.Coverage
	if path == "" {
		path = filepath.Join(e.cfg.Work, "coverage-"+e.key+".json.gz")
		if _, err := os.Stat(path); err != nil {
			e.logf("coverage: tree %s into %s", e.key, path)
			c, err := e.coverageOfTree(ctx)
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
	if e.cfg.Coverage != "" {
		for _, t := range c.Tests {
			suite, _ := splitID(t.ID)
			if dir := suitename.LegacyTarget(suite); dir != "" && e.suite(suite) == nil {
				return fmt.Errorf("%s: coverage uses legacy suite %q; measure it again for %q", path, suite, dir)
			}
		}
	}
	e.logf("coverage: %s (%d tests)", path, len(c.Tests))
	e.cov = c
	return nil
}

// measure runs every test of suites once, each in its own process with its
// own coverage directory, in a fresh copy of the checkout. It builds the
// tools those suites run.
func (e *engine) measure(ctx context.Context, suites []Suite) (*coverage, error) {
	root := filepath.Join(e.cfg.Work, "cover", "src")
	bin := filepath.Join(e.cfg.Work, "cover", "bin")
	if err := e.copyTree(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return nil, err
	}
	need := map[string]bool{}
	for i := range suites {
		for _, name := range toolsOf(&suites[i]) {
			need[name] = true
		}
	}
	c := &coverage{Key: e.key, Builds: map[string]float64{}}
	for _, t := range e.cfg.Tools {
		if !need[t.Name] {
			continue
		}
		o, err := e.buildTool(ctx, root, bin, t.Name, true, e.cfg.CoverDeadline)
		if err == nil && o.reason != "" {
			err = fmt.Errorf("build %s with coverage: %s\n%s", t.Name, o.reason, o.tail)
		}
		if err != nil {
			return nil, err
		}
		c.Builds[t.Name] = o.seconds
	}
	for i := range suites {
		s := &suites[i]
		var names []string
		if s.Package != "" {
			o := e.buildSuite(ctx, root, bin, s, true, e.cfg.CoverDeadline)
			if o.reason != "" {
				return nil, fmt.Errorf("build suite %s with coverage: %s\n%s", s.Name, o.reason, o.tail)
			}
			c.Builds[s.Name] = o.seconds
			// In the package's directory, as go test runs the binary: a
			// test file's init may read testdata (lower's loose JSON
			// cases), and from the root it panics before it lists.
			lister := exec.Command(filepath.Join(bin, s.Name+".test"), "-test.list", ".")
			lister.Dir = filepath.Join(root, filepath.FromSlash(s.Module), filepath.FromSlash(s.Package))
			var stderr strings.Builder
			lister.Stderr = &stderr
			list, err := lister.Output()
			if err != nil {
				return nil, fmt.Errorf("list the tests of %s: %w\n%s", s.Name, err, firstLine(stderr.String()))
			}
			listed := strings.Fields(string(list))
			for _, n := range listed {
				if measured(s, n) {
					names = append(names, n)
				}
			}
			// A test a ledger names that the suite does not have would
			// never run: a stale killer is refused, not silently dropped.
			var missing []string
			for _, n := range s.Named {
				parts := regexp.MustCompile(partsPattern(n))
				if !slices.Contains(listed, n) && !(slices.Contains(s.Families, n) && slices.ContainsFunc(listed, parts.MatchString)) {
					missing = append(missing, s.Name+":"+n)
				}
			}
			if len(missing) > 0 {
				return nil, fmt.Errorf("the ledgers name tests that %s does not have: %s", s.Name, strings.Join(missing, ", "))
			}
		} else {
			var err error
			if s.Only != nil {
				// The ledger names them: an example need not be a default one.
				names = slices.Clone(s.Only)
			} else if names, err = s.List(root); err != nil {
				return nil, fmt.Errorf("list the tests of %s: %w", s.Name, err)
			}
			for _, n := range s.Named {
				if !slices.Contains(names, n) {
					names = append(names, n)
				}
			}
		}
		for _, n := range names {
			c.Tests = append(c.Tests, &covTest{ID: s.Name + ":" + n})
		}
	}
	k := &collector{module: e.module, blocks: map[blockKey][]int{}, ergo: newErgoMapper(filepath.Join(e.cfg.Work, "cover", "ergo-go"), root)}
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
	argv, cwd, env := e.argv(s, root, bin, name, e.cfg.CoverDeadline)
	if s.Package != "" && s.Module == "" && len(e.cfg.Cover) > 0 {
		// A nested module's suite is built without coverage (Suite.Module),
		// and so is every suite when no package is covered (a ledger of
		// .ergo rows only: its tests are selected by what they read).
		argv = append(argv, "-test.gocoverdir="+dir)
	}
	testLog := ""
	if s.Package != "" {
		// The files the test opens (reads.go).
		testLog = testLogName(dir)
		argv = append(argv, "-test.testlogfile="+testLog)
		defer os.Remove(testLog)
	}
	o := e.execStarted(ctx, cwd, argv, e.env(append(env, "GOCOVERDIR="+dir, coverGoVariable+"="+k.ergo.dir, embedObserveVariable+"="+root)...), e.cfg.CoverDeadline, startLine(s, name))
	if testLog != "" {
		if f, err := os.Open(testLog); err == nil {
			mu.Lock()
			err = k.reads(i, f, cwd, root)
			mu.Unlock()
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	t.Seconds = o.seconds
	if o.reason != "" && s.Package != "" {
		// A test may fail only because its binary is built with coverage:
		// internal/closure's tests run their own binary in a sandbox where
		// all but fresh scratch is read-only, and a coverage-built binary
		// writes its coverage at exit under TMPDIR, so the run fails though
		// its tests pass. The mutant pass runs binaries built without
		// coverage, so such a test still judges mutants: run it once so,
		// and if it passes there, keep it, with the blocks its coverage run
		// recorded (MUT-names r2: 8 closure tests, rm-cgo-link-input's
		// killer among them, judged nothing).
		plain, err := e.plainRun(ctx, root, s, name)
		if err != nil {
			return err
		}
		if plain.reason == "" {
			e.logf("coverage: %s fails only when built with coverage (%s); it passes without, so it stays a killer", t.ID, o.reason)
			t.Seconds = max(t.Seconds, plain.seconds)
			o = plain
		}
	}
	if o = neverRan(o); o.reason != "" {
		t.Failed = true
		t.Detail = baseDetail(t.ID, o)
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

// plainRun runs one test of the Go suite s in the coverage pass's copy at
// root with the suite's binary built without coverage, built once per suite.
func (e *engine) plainRun(ctx context.Context, root string, s *Suite, test string) (outcome, error) {
	bin := filepath.Join(e.cfg.Work, "cover", "plain")
	e.plainMu.Lock()
	if e.plain == nil {
		e.plain = map[string]*plainBuild{}
	}
	b := e.plain[s.Name]
	if b == nil {
		b = &plainBuild{}
		e.plain[s.Name] = b
	}
	e.plainMu.Unlock()
	b.once.Do(func() {
		if err := os.MkdirAll(bin, 0o755); err != nil {
			b.err = err
			return
		}
		if o := e.buildSuite(ctx, root, bin, s, false, e.cfg.CoverDeadline); o.reason != "" {
			b.err = fmt.Errorf("build suite %s without coverage: %s\n%s", s.Name, o.reason, o.tail)
		}
	})
	if b.err != nil {
		return outcome{}, b.err
	}
	argv, cwd, env := e.argv(s, root, bin, test, e.cfg.CoverDeadline)
	return e.execStarted(ctx, cwd, argv, e.env(env...), e.cfg.CoverDeadline, startLine(s, test)), nil
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
