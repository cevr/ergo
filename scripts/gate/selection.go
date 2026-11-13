package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/scripts/gate/replay"
)

// Test-impact selection (gate-speed GS4). The fast tier runs, of the
// packages in selectPackages, only the tests that an edit can reach, judged
// against a coverage record of a base tree: each fast test's blocks
// (measured with -cover, each test in its own process) and every file,
// directory and variable its process tree read (the test log that
// internal/testinputs completes by observing children). The full tier
// still runs every test. A test is skipped only when the record knows it,
// it passed there, its reads were observed, and the edit changes neither a
// block it ran nor anything it read; any change the record cannot place
// runs every test (fail closed).
//
// What runs every test: no record, a record of another toolchain or of
// other runtime settings (selectionFacts), and a change to a compiled
// input that is not a function body's lines: a declaration's, an
// import's, a test file, an embedded file, a file added to or removed from
// a compiled directory, go.mod.

// selectPackages are the packages whose fast-tier tests the gate selects:
// the compiler's (check, lower) and the CLI's, the ones every compiler
// edit runs (about 170 CPU-s of the fast tier, fast-cpu.json).
var selectPackages = []string{"internal/sketch/check", "internal/sketch/lower", "cmd/ergo"}

// coverageVersion is the record format; a record of another version is
// no record.
const coverageVersion = 1

// coverageSubdir is where the records live in the gate's cache directory:
// one file per package, and the base sources the hunks are read against.
const coverageSubdir = "coverage"

// coverageRecord is one package's record.
type coverageRecord struct {
	Version int    `json:"version"`
	Package string `json:"package"`
	// Facts is selectionFacts at the record.
	Facts string `json:"facts"`
	// Env is the digest of each variable some test read (valueDigest).
	Env   map[string]string `json:"env"`
	Tests []recordedTest    `json:"tests"`
	// Blocks are, per module-relative file, the blocks some test ran.
	Blocks map[string][]recordedBlock `json:"blocks"`
	// Compiled is the digest of every compiled input (compiledInputs).
	Compiled map[string]string `json:"compiled"`
	// Reads are the paths the tests read, with their state (readState).
	Reads []recordedRead `json:"reads"`
}

type recordedTest struct {
	Name string `json:"name"`
	// Always says why the test runs on every edit: it failed, its reads
	// were not observed, it reads what a step of the run writes.
	Always string   `json:"always,omitempty"`
	Reads  []int    `json:"reads,omitempty"`
	Env    []string `json:"env,omitempty"`
}

type recordedBlock struct {
	Start int   `json:"s"`
	End   int   `json:"e"`
	Tests []int `json:"t"`
}

type recordedRead struct {
	// Path is relative to the module root with slashes, or absolute
	// outside it.
	Path string `json:"path"`
	// Lookup is a stat (or a chdir) rather than an open: it reads what
	// the path is, not a directory's entries.
	Lookup bool   `json:"lookup,omitempty"`
	State  string `json:"state"`
}

// performanceVariables change how fast a test runs, not what it does; a
// difference in them selects no test. GOFLAGS is compared without its -p.
var performanceVariables = []string{"GOCACHE", "GOMAXPROCS", "GOGC", "GOMEMLIMIT", "TMPDIR", "GOTMPDIR", "GOCOVERDIR", testenvToolDigest}

// testenvToolDigest is the variable through which the gate passes the
// tools' digest; the tools themselves are reads (a program run is an open).
const testenvToolDigest = "ERGO_TOOL_VERSIONS"

// selectionFacts are what every test depends on that no read names: the
// toolchain (its files under GOROOT are not recorded as reads), the
// platform, the runtime's own variables, and the record format.
func selectionFacts(env []string) (string, error) {
	command := exec.Command("go", "env", "GOVERSION", "GOROOT", "GOOS", "GOARCH", "GOEXPERIMENT", "CGO_ENABLED")
	command.Env = env
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("go env: %w", err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "v%d\x00%s\x00%s\x00", coverageVersion, output, fastFlag)
	for _, name := range []string{"GODEBUG", "GOTRACEBACK", "GOFLAGS"} {
		value, _ := environmentVariable(env, name)
		fmt.Fprintf(h, "%s=%s\x00", name, normalizedValue(name, value))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// normalizedValue drops what only bounds parallelism: GOFLAGS' -p.
func normalizedValue(name, value string) string {
	if name != "GOFLAGS" {
		return value
	}
	fields := slices.DeleteFunc(strings.Fields(value), func(f string) bool {
		return strings.HasPrefix(f, "-p=") || strings.HasPrefix(f, "--p=")
	})
	return strings.Join(fields, " ")
}

// valueDigest is the digest of a variable's value in env, or "unset".
func valueDigest(env []string, name string) string {
	value, ok := environmentVariable(env, name)
	if !ok {
		return "unset"
	}
	sum := sha256.Sum256([]byte(normalizedValue(name, value)))
	return hex.EncodeToString(sum[:8])
}

func coveragePath(cacheDir, dir string) string {
	return filepath.Join(cacheDir, coverageSubdir, strings.ReplaceAll(dir, "/", "_")+".json.gz")
}

func sourcePath(cacheDir, digest string) string {
	return filepath.Join(cacheDir, coverageSubdir, "src", digest)
}

func fileDigestOf(name string) (string, []byte, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data, nil
}

// buildExtensions are the non-Go files the go command compiles or links
// from a package directory.
var buildExtensions = []string{".s", ".S", ".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".hxx", ".m", ".f", ".F", ".for", ".f90", ".syso", ".swig", ".swigcxx"}

// moduleFiles are the files at the module root every build reads.
var moduleFiles = []string{"go.mod", "go.sum", "go.work", "go.work.sum"}

// compiledInputs are the files the test binary is built from: every file
// directly in a dependency directory of the closure, every embedded file,
// and the module files. Each maps to its content's digest.
func compiledInputs(root string, closure *replay.Closure) (map[string]string, error) {
	out := map[string]string{}
	add := func(rel string) error {
		digest, _, err := fileDigestOf(filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		out[rel] = digest
		return nil
	}
	for _, dir := range closure.Deps {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				if err := add(path.Join(dir, entry.Name())); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, rel := range append(slices.Clone(closure.Embeds), moduleFiles...) {
		if err := add(rel); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readState is what a read path is now: its content's digest inside the
// root, its size, mode and modification time outside it (as cmd/go's
// test cache rechecks), its entries' names for a directory opened (its
// mode for one looked up), or missing.
func readState(root, name string, lookup bool) string {
	full := name
	inside := !filepath.IsAbs(name)
	if inside {
		full = filepath.Join(root, filepath.FromSlash(name))
	}
	info, err := os.Stat(full)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	case err != nil:
		return "error " + err.Error()
	case info.IsDir() && lookup:
		return "dir " + info.Mode().String()
	case info.IsDir():
		entries, err := os.ReadDir(full)
		if err != nil {
			return "error " + err.Error()
		}
		h := sha256.New()
		for _, entry := range entries {
			fmt.Fprintf(h, "%s %v\n", entry.Name(), entry.Type())
		}
		return "dir " + hex.EncodeToString(h.Sum(nil)[:16])
	case !info.Mode().IsRegular():
		return "mode " + info.Mode().String()
	case inside:
		digest, _, err := fileDigestOf(full)
		if err != nil {
			return "error " + err.Error()
		}
		return "file " + digest
	}
	return fmt.Sprintf("file %d %v %d", info.Size(), info.Mode(), info.ModTime().UnixNano())
}

// selection is the outcome for one package: the known tests to skip, and
// the line the gate prints.
type selection struct {
	skip []string
	note string
}

// selectTests reads the package's record and judges the edit since it.
// names are the fast tests the step would run.
func selectTests(root, cacheDir string, p testPackage, env []string, names []string) selection {
	all := func(reason string) selection { return selection{note: "every test: " + reason} }
	if cacheDir == "" {
		return all("no gate cache")
	}
	record, err := loadCoverageRecord(coveragePath(cacheDir, p.dir))
	if errors.Is(err, os.ErrNotExist) {
		return all("no coverage record (./scripts/gate.sh -record-durations -coverage)")
	}
	if err != nil {
		return all(err.Error())
	}
	if record.Version != coverageVersion || record.Package != p.importPath {
		return all("the coverage record is of another format or package")
	}
	facts, err := selectionFacts(env)
	if err != nil {
		return all(err.Error())
	}
	if facts != record.Facts {
		return all("the toolchain or the runtime settings changed since the coverage record")
	}
	selected := make([]bool, len(record.Tests))
	for i, test := range record.Tests {
		if test.Always != "" {
			selected[i] = true
		}
	}
	// Compiled inputs: a function body's lines select the tests that ran
	// their blocks; any other change runs every test.
	compiled, err := compiledInputs(root, p.closure)
	if err != nil {
		return all(err.Error())
	}
	deps := map[string]bool{}
	for _, dir := range p.closure.Deps {
		deps[dir] = true
	}
	changed := 0
	for _, rel := range sortedUnion(record.Compiled, compiled) {
		before, after := record.Compiled[rel], compiled[rel]
		if before == after {
			continue
		}
		changed++
		dir, base := path.Dir(rel), path.Base(rel)
		built := strings.HasSuffix(base, ".go") || slices.Contains(buildExtensions, path.Ext(base))
		switch {
		case !deps[dir] || slices.Contains(p.closure.Embeds, rel) || slices.Contains(moduleFiles, rel):
			// An embedded file, a module file, or a file of a directory
			// that is no longer, or not yet, compiled in.
			return all(rel + " changed: the binary is built from it")
		case !built:
			// A data file of a package directory: its readers (below).
		case strings.HasSuffix(base, "_test.go") || !strings.HasSuffix(base, ".go") || before == "" || after == "":
			return all(rel + " changed, was added or was removed: the binary is built from it")
		default:
			old, err := os.ReadFile(sourcePath(cacheDir, before))
			if err != nil {
				return all(rel + ": the base source is missing: " + err.Error())
			}
			now, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				return all(err.Error())
			}
			lines, reason := bodyLines(string(old), string(now))
			if reason != "" {
				return all(rel + ": " + reason)
			}
			for _, b := range record.Blocks[rel] {
				for _, span := range lines {
					if b.Start <= span[1] && b.End >= span[0] {
						for _, t := range b.Tests {
							selected[t] = true
						}
					}
				}
			}
		}
	}
	// Reads: a test that read a path that is now otherwise.
	moved := make([]bool, len(record.Reads))
	for i, r := range record.Reads {
		moved[i] = readState(root, r.Path, r.Lookup) != r.State
		if moved[i] {
			changed++
		}
	}
	envMoved := map[string]bool{}
	for name, digest := range record.Env {
		if !slices.Contains(performanceVariables, name) && valueDigest(env, name) != digest {
			envMoved[name] = true
		}
	}
	for i, test := range record.Tests {
		for _, r := range test.Reads {
			if moved[r] {
				selected[i] = true
			}
		}
		for _, name := range test.Env {
			if envMoved[name] {
				selected[i] = true
			}
		}
	}
	known := map[string]int{}
	for i, test := range record.Tests {
		known[test.Name] = i
	}
	var skip []string
	run := 0
	for _, name := range names {
		if i, ok := known[name]; ok && !selected[i] {
			skip = append(skip, name)
		} else {
			run++
		}
	}
	return selection{skip: skip, note: fmt.Sprintf("selected %d of %d fast tests (%d changed inputs since the coverage record)", run, len(names), changed)}
}

func sortedUnion(a, b map[string]string) []string {
	var keys []string
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// bodyLines are the old lines, widened by one on each side, that the
// edit from old to now touches, when every hunk lies inside one function
// body in both versions; else the reason it does not.
func bodyLines(old, now string) ([][2]int, string) {
	oldSet, set := token.NewFileSet(), token.NewFileSet()
	oldFile, err := parser.ParseFile(oldSet, "", old, parser.SkipObjectResolution)
	if err != nil {
		return nil, "the base source does not parse"
	}
	nowFile, err := parser.ParseFile(set, "", now, parser.SkipObjectResolution)
	if err != nil {
		return nil, "it does not parse"
	}
	var spans [][2]int
	for _, h := range lineHunks(old, now) {
		before := enclosingBody(oldSet, oldFile, h.OldLo, h.OldHi)
		after := enclosingBody(set, nowFile, h.NewLo, h.NewHi)
		if before == "" || before != after {
			return nil, fmt.Sprintf("lines %d-%d change outside a function body", h.OldLo, h.OldHi)
		}
		spans = append(spans, [2]int{h.OldLo - 1, h.OldHi + 1})
	}
	return spans, ""
}

// enclosingBody names the function whose body strictly holds lines lo..hi
// (an insertion before line lo when hi < lo), or "".
func enclosingBody(set *token.FileSet, file *ast.File, lo, hi int) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		open, close := set.Position(fn.Body.Lbrace).Line, set.Position(fn.Body.Rbrace).Line
		inside := open < lo && hi < close
		if hi < lo {
			inside = open <= lo-1 && lo <= close
		}
		if !inside {
			continue
		}
		name := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			name = receiverName(fn.Recv.List[0].Type) + "." + name
		}
		return name
	}
	return ""
}

func receiverName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return "*" + receiverName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return receiverName(e.X)
	case *ast.IndexListExpr:
		return receiverName(e.X)
	}
	return fmt.Sprintf("%T", expr)
}

func loadCoverageRecord(name string) (*coverageRecord, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var record coverageRecord
	if err := json.NewDecoder(z).Decode(&record); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &record, nil
}

func saveCoverageRecord(name string, record *coverageRecord) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	z := gzip.NewWriter(f)
	err = json.NewEncoder(z).Encode(record)
	if closeErr := z.Close(); err == nil {
		err = closeErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// recordCoverage records the package's fast tests: it builds the test
// binary with coverage of every module package in its closure, runs each
// test alone (-short, the step's variables, a test log), and keeps its
// blocks, its reads and the variables it read. writes are the paths steps
// of the run write (bin/ergo): a test that reads one reads what the run
// rebuilds after selection, so it always runs.
// env runs the tests; declared is the step's environment as the gate
// declares it, whose values the record keeps (a path relative to the root
// in env is absolute, as the test binary runs in its package directory).
func recordCoverage(root, cacheDir string, limit limiter, p testPackage, env, declared []string, names []string, writes []string, progress io.Writer) (*coverageRecord, error) {
	module := modulePath(root)
	scratch, err := os.MkdirTemp("", "gate-coverage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	var coverpkg []string
	for _, dir := range p.closure.Deps {
		coverpkg = append(coverpkg, path.Join(module, dir))
	}
	binary := filepath.Join(scratch, "pkg.test")
	build := exec.Command("go", "test", "-c", "-cover", "-covermode=set", "-coverpkg="+strings.Join(coverpkg, ","), "-o", binary, "./"+p.dir)
	build.Env, build.Dir = env, root
	if output, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go test -c -cover %s: %w\n%s", p.dir, err, output)
	}
	facts, err := selectionFacts(declared)
	if err != nil {
		return nil, err
	}
	excluded := []string{filepath.Clean(scratch) + string(filepath.Separator), "/proc/", "/sys/", "/dev/"}
	if real, err := filepath.EvalSymlinks(scratch); err == nil {
		excluded = append(excluded, real+string(filepath.Separator))
	}
	for _, name := range []string{"GOROOT", "GOMODCACHE", "GOCACHE"} {
		command := exec.Command("go", "env", name)
		command.Env = env
		if output, err := command.Output(); err == nil && strings.TrimSpace(string(output)) != "" {
			excluded = append(excluded, filepath.Clean(strings.TrimSpace(string(output)))+string(filepath.Separator))
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	record := &coverageRecord{Version: coverageVersion, Package: p.importPath, Facts: facts, Env: map[string]string{}, Blocks: map[string][]recordedBlock{}}
	readIndex := map[recordedRead]int{}
	blocks := map[[3]any][]int{}
	packageDir := filepath.Join(root, filepath.FromSlash(p.dir))
	for i, name := range names {
		work := filepath.Join(scratch, strconv.Itoa(i))
		counters, temporary := filepath.Join(work, "cov"), filepath.Join(work, "tmp")
		for _, d := range []string{counters, temporary} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return nil, err
			}
		}
		log := filepath.Join(work, "log")
		// sh runs the built binary, as writeTestCPU does: the gate's host
		// programs are literals (hostPrograms).
		command := exec.Command("sh", "-c", `exec "$0" "$@"`, binary, "-test.run", exactNames([]string{name}), "-test.short", "-test.count=1", "-test.testlogfile="+log, "-test.gocoverdir="+counters)
		// GOTMPDIR is the test's own scratch too (gate-speed GS10): the go
		// command a test starts makes and removes its work directories
		// there, so a shared GOTMPDIR's listing, read by every test that
		// builds Go, changed between any two runs and selected those tests
		// after every edit, a docs edit included.
		command.Env = setEnvironmentVariable(setEnvironmentVariable(setEnvironmentVariable(env, "TMPDIR", temporary), "GOTMPDIR", temporary), "GOCOVERDIR", counters)
		command.Dir = packageDir
		var output bytes.Buffer
		command.Stdout, command.Stderr = &output, &output
		used, runErr := runBudgeted(limit, "coverage "+p.dir+" "+name, command)
		test := recordedTest{Name: name}
		switch {
		case used.verdict != "":
			test.Always = "over its budget at the record: " + used.verdict
		case runErr != nil:
			test.Always = "failed at the record"
		}
		// Blocks.
		profile := filepath.Join(work, "profile")
		convert := exec.Command("go", "tool", "covdata", "textfmt", "-i="+counters, "-o="+profile)
		convert.Env = env
		if out, err := convert.CombinedOutput(); err != nil {
			if test.Always == "" {
				test.Always = "no coverage: " + strings.TrimSpace(string(out))
			}
		} else if err := readProfile(profile, module, func(file string, start, end int) {
			key := [3]any{file, start, end}
			if tests := blocks[key]; len(tests) == 0 || tests[len(tests)-1] != i {
				blocks[key] = append(tests, i)
			}
		}); err != nil {
			return nil, err
		}
		// Reads.
		content, err := os.ReadFile(log)
		if err != nil || !bytes.HasSuffix(content, []byte("\n")) {
			if test.Always == "" {
				test.Always = "its reads were not observed"
			}
		}
		cwd := packageDir
		reads := map[int]bool{}
		envs := map[string]bool{}
		scanner := bufio.NewScanner(bytes.NewReader(content))
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			op, arg, ok := strings.Cut(scanner.Text(), " ")
			if !ok || strings.HasPrefix(op, "#") {
				continue
			}
			switch op {
			case "getenv":
				envs[arg] = true
				continue
			case "open", "stat", "chdir":
			default:
				continue
			}
			full := arg
			if !filepath.IsAbs(full) {
				full = filepath.Join(cwd, full)
			}
			full = filepath.Clean(full)
			if op == "chdir" {
				cwd = full
			}
			if slices.ContainsFunc(excluded, func(prefix string) bool { return strings.HasPrefix(full+string(filepath.Separator), prefix) }) {
				continue
			}
			name := full
			for _, base := range []string{filepath.Clean(root), realRoot} {
				if rel, err := filepath.Rel(base, full); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
					name = filepath.ToSlash(rel)
					break
				}
			}
			if !filepath.IsAbs(name) && slices.ContainsFunc(writes, func(w string) bool {
				w = strings.TrimSuffix(w, "/")
				return name == w || strings.HasPrefix(name, w+"/")
			}) && test.Always == "" {
				test.Always = "reads " + name + ", which a step of the run writes"
			}
			lookup := op != "open"
			at, ok := readIndex[recordedRead{Path: name, Lookup: lookup}]
			if !ok {
				at = len(record.Reads)
				readIndex[recordedRead{Path: name, Lookup: lookup}] = at
				record.Reads = append(record.Reads, recordedRead{Path: name, Lookup: lookup})
			}
			reads[at] = true
		}
		for at := range reads {
			test.Reads = append(test.Reads, at)
		}
		slices.Sort(test.Reads)
		for name := range envs {
			test.Env = append(test.Env, name)
			record.Env[name] = valueDigest(declared, name)
		}
		slices.Sort(test.Env)
		record.Tests = append(record.Tests, test)
		if progress != nil {
			state := "ok"
			if test.Always != "" {
				state = test.Always
			}
			fmt.Fprintf(progress, "coverage %s %d/%d %s: %s\n", p.dir, i+1, len(names), name, state)
		}
	}
	for key, tests := range blocks {
		file := key[0].(string)
		record.Blocks[file] = append(record.Blocks[file], recordedBlock{Start: key[1].(int), End: key[2].(int), Tests: tests})
	}
	for _, list := range record.Blocks {
		slices.SortFunc(list, func(a, b recordedBlock) int {
			if a.Start != b.Start {
				return a.Start - b.Start
			}
			return a.End - b.End
		})
	}
	for i := range record.Reads {
		record.Reads[i].State = readState(root, record.Reads[i].Path, record.Reads[i].Lookup)
	}
	if record.Compiled, err = compiledInputs(root, p.closure); err != nil {
		return nil, err
	}
	for rel, digest := range record.Compiled {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != digest {
			return nil, fmt.Errorf("%s changed while the coverage was recorded", rel)
		}
		destination := sourcePath(cacheDir, digest)
		if _, err := os.Stat(destination); err == nil {
			continue
		}
		if err := writeAtomically(destination, data); err != nil {
			return nil, err
		}
	}
	return record, nil
}

// writeCoverage records the named packages (selectPackages when none is
// named) in the gate cache: `./scripts/gate.sh -record-durations
// -coverage`. It first runs the fast tier's writers (build bin/ergo), as a
// run would before the tests.
func writeCoverage(root, cacheDir string, limit limiter, packages []string, out io.Writer) error {
	if cacheDir == "" {
		return errors.New("-coverage: the gate has no cache directory")
	}
	if len(packages) == 0 {
		packages = selectPackages
	}
	steps, err := fastSteps(root)
	if err != nil {
		return err
	}
	listed, _, err := testPackages(root)
	if err != nil {
		return err
	}
	durations, err := readDurations(root)
	if err != nil {
		return err
	}
	var writes []string
	for _, step := range steps {
		if len(step.Writes) == 0 {
			continue
		}
		writes = append(writes, step.Writes...)
		var command *exec.Cmd
		switch step.Argv[0] {
		case "go":
			command = exec.Command("go", step.Argv[1:]...)
		case "sh":
			command = exec.Command("sh", step.Argv[1:]...)
		default:
			return fmt.Errorf("%s: not a go build or a reviewed script: %q", step.Name, step.Argv)
		}
		command.Env, command.Dir = append(stepEnvironment(), step.Env...), root
		command.Stdout, command.Stderr = io.Discard, os.Stderr
		if used, err := runBudgeted(limit, step.Name, command); used.verdict != "" || err != nil {
			return fmt.Errorf("%s: %s %v", step.Name, used.verdict, err)
		}
	}
	for _, pkg := range packages {
		dir := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(pkg)), "./")
		at := slices.IndexFunc(listed, func(p testPackage) bool { return p.dir == dir })
		if at < 0 {
			return fmt.Errorf("-coverage: %s is not a test package", pkg)
		}
		p := listed[at]
		step := slices.IndexFunc(steps, func(s *Step) bool { return s.Package == p.closure || strings.HasPrefix(s.Name, "go test ./"+dir+" ") })
		if step < 0 {
			return fmt.Errorf("-coverage: no fast-tier step tests %s", pkg)
		}
		declared := append(stepEnvironment(), steps[step].Env...)
		env := slices.Clone(declared)
		for k, v := range env {
			if name, value, ok := strings.Cut(v, "="); ok && slices.Contains(cliTestEnvironment, v) {
				env[k] = name + "=" + filepath.Join(root, value)
			}
		}
		names, err := testNames(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			return err
		}
		fast, _ := fastSelection(names, durations[p.importPath])
		record, err := recordCoverage(root, cacheDir, limit, p, env, declared, fast, writes, out)
		if err != nil {
			return fmt.Errorf("-coverage %s: %w", pkg, err)
		}
		if err := saveCoverageRecord(coveragePath(cacheDir, dir), record); err != nil {
			return err
		}
		always := 0
		for _, test := range record.Tests {
			if test.Always != "" {
				always++
			}
		}
		fmt.Fprintf(out, "coverage %s: %d tests recorded, %d run on every edit, %d reads, %d compiled inputs\n", dir, len(record.Tests), always, len(record.Reads), len(record.Compiled))
	}
	return nil
}

// readProfile calls block for each block a textfmt profile ran in a file
// of the module.
func readProfile(name, module string, block func(file string, start, end int)) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		colon := strings.LastIndexByte(line, ':')
		fields := strings.Fields(line[colon+1:])
		if colon < 0 || len(fields) != 3 {
			return fmt.Errorf("coverage profile: bad line %q", line)
		}
		if fields[2] == "0" {
			continue
		}
		from, to, _ := strings.Cut(fields[0], ",")
		startLine, _, _ := strings.Cut(from, ".")
		endLine, _, _ := strings.Cut(to, ".")
		start, err1 := strconv.Atoi(startLine)
		end, err2 := strconv.Atoi(endLine)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("coverage profile: bad line %q", line)
		}
		if rel, ok := strings.CutPrefix(line[:colon], module+"/"); ok {
			block(rel, start, end)
		}
	}
	return scanner.Err()
}

func writeAtomically(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
