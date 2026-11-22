package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

// The verdict cache (gate-speed GS1). A program's legs (fmt, build, test,
// check, explore and its cases) give the same verdict and print the same
// lines whenever everything they read is the same: the program's folder,
// the compiler (bin/ergo, which embeds the checker, the lowering, the std
// snapshot and the runtime that ergo build emits), the runner itself (its
// law check links the checker's front end), the Go toolchain the compiler
// builds with, the environment and the runner's settings. With -cache DIR
// the runner keys each program on all of them; a passed program whose key
// is recorded is replayed (its recorded lines, then a line saying so) and
// not run, and any other program runs. Only a pass is recorded: a failure
// always runs again. A key that cannot be computed is no key: the program
// runs, and a line says why it was not cached (fail closed).
//
// Programs whose result depends on what the key cannot name are never
// cached: a ts program reads the Bun cache, a net program the network and
// a cc program the C toolchain (uncachedTags).
//
// Early cutoff (gate-speed GS8). Most compiler edits change bin/ergo's
// bytes but not the Go it emits for a given program. So in a checkout the
// key does not hold the compiler's bytes; it holds what the compiler's
// edit reaches in a program's legs:
//   - for the build, test and explore legs and the cases, the Go module the
//     compiler emits for each of the program's targets (`ergo build
//     --emitted`, `ergo test --emitted`), and the Go sources of the driver:
//     the packages of cmd/ergo and of this runner, less those reached only
//     through the checker and the lowering (driverPackages), whose effect
//     on these legs is the emitted Go;
//   - for the fmt and check legs, their whole result: the runner runs them
//     to make the key, and their output and status are in it.
//
// A program of packages makes its check and test --emitted calls once
// over DIR/..., not once per package (gate-speed GS27): about a quarter of
// the key's CPU, since each call checks the std packages it imports once.
// On a miss its legs then check each package again.
// The driver's sources stand for bin/ergo's driver: the runner trusts that
// -ergo was built from -root, as the gate's `build bin/ergo` step builds it.
// Where -root has no cmd/ergo to list (a test's fixture), the key holds the
// bytes of bin/ergo and of the runner instead, as before.

// cacheVariable names the cache directory when -cache is not given; the
// gate sets it for its examples step (scripts/gate, examplesStep).
const cacheVariable = "ERGO_EXAMPLES_CACHE"

// cacheVersion changes when what an entry holds or what the key covers
// changes, so an older entry is never read as a newer one.
const cacheVersion = "ergo-examples-cache v2"

// driverRoots are the packages whose code drives the program legs: the
// compiler's command and this runner. emissionRoots are the packages whose
// effect on those legs is the Go the compiler emits (or the check and fmt
// results, which the key holds): a package reached only through them is
// no driver. std and its gostd bindings are among them: the driver reads
// std only to resolve imports and to key its emission cache, and the Go
// the lowering emits from std's bodies is in the emitted digest.
var (
	driverRoots   = []string{"./cmd/ergo", "./scripts/examples"}
	emissionRoots = []string{"internal/sketch/check", "internal/sketch/check/snapshot", "internal/sketch/lower", "internal/sketch/std", "internal/sketch/stdlib", "internal/sketch/std/gostd"}
)

// cacheKeep is how many entries of one program the cache keeps: the newest.
const cacheKeep = 8

// uncachedTags are the tags of programs that are never cached: their
// verdict reads inputs outside the key.
var uncachedTags = []string{"ts", "net", "cc", "tty", "gui"}

// goSettings are the go env values that change what the compiler's go
// builds produce.
var goSettings = []string{"GOVERSION", "GOROOT", "GOOS", "GOARCH", "GOFLAGS", "GOEXPERIMENT", "GOAMD64", "GOARM64", "CGO_ENABLED", "CC", "CGO_CFLAGS", "CGO_LDFLAGS", "GOTOOLCHAIN"}

// volatileEnv are variables that differ on every invocation and that no
// compiler, Go build or program reads: the service manager's identifiers of
// the unit the runner runs in (systemd-run --scope sets a new INVOCATION_ID
// each time, so with it every program missed: GS1's receipt, 51 of 51 run
// again) and the shell's last command. Every other variable is keyed.
var volatileEnv = []string{"INVOCATION_ID", "JOURNAL_STREAM", "SYSTEMD_EXEC_PID", "MANAGERPID", "_"}

// cacheEntry is one recorded pass.
type cacheEntry struct {
	Version string `json:"version"`
	Program string `json:"program"`
	Key     string `json:"key"`
	Output  string `json:"output"`
	Cases   int    `json:"cases"`
}

// verdictCache is the run's cache: its directory, and the part of the key
// every program shares (or why there is none).
type verdictCache struct {
	dir    string
	common string
	reason string
	// hits and misses count the run's programs replayed and run; the
	// programs that run at once (runAll) count under mu.
	mu           sync.Mutex
	hits, misses int
	// early says the common part holds the driver's sources, and each
	// program's key its emitted Go and its fmt and check results (early
	// cutoff), not the compiler's bytes.
	early bool
}

// legKey writes into h what the compiler gives each leg of p, as the
// runner's legs ask it (early cutoff): the fmt leg's and each check leg's
// status and output, and the digest of the Go module each build, test and
// explore leg would link (--emitted). "" or why there is no key.
func (r *runner) legKey(h io.Writer, p *program) string {
	targets := slices.Clone(p.tests)
	for _, e := range p.explored {
		if !slices.Contains(targets, e.target) {
			targets = append(targets, e.target)
		}
	}
	calls := [][]string{append([]string{"fmt", "--check"}, p.sources...)}
	// A program of packages is keyed by one call over all its packages
	// (DIR/...), `ergo test --emitted --json`: for each package its check
	// envelope, the digest of its test module and, for the main package,
	// of its build, from one load of the module, where three calls (check,
	// test --emitted and build --emitted) each loaded it (gate-speed GS27,
	// GS31b). Its targets are every package, so the pattern reaches the
	// same packages, which coveredPackages holds.
	pattern := ""
	if p.module && len(targets) > 1 {
		pattern = filepath.Join(p.dir, "...")
		calls = append(calls, []string{"test", "--emitted", "--json", pattern})
		if r.cfg.rewrites != "" {
			// The pattern's build digest is the default build's; a
			// -rewrites run keys its own build (EVIDENCE-REPORT).
			calls = append(calls, r.buildArgs("--emitted", p.main))
		}
	} else {
		calls = append(calls, r.buildArgs("--emitted", p.main))
		for _, target := range targets {
			calls = append(calls, []string{"check", target}, []string{"test", "--emitted", target})
		}
	}
	for _, args := range calls {
		o, err := r.tool(r.cfg.timeout, args...)
		if err != nil {
			return fmt.Sprintf("ergo %s could not start: %v", strings.Join(args, " "), err)
		}
		if over := o.OverCap(); over != "" || o.TimedOut {
			return fmt.Sprintf("ergo %s: no result to key", strings.Join(args, " "))
		}
		if r.keyed == nil {
			r.keyed = map[string]outcome{}
		}
		r.keyed[keyedCall(args)] = o
		stdout := o.Stdout
		if args[0] == "check" {
			stdout = withoutProducer(stdout)
		}
		if pattern != "" && args[0] == "test" {
			lines, why := emittedLines(o.Stdout)
			if why == "" {
				why = coveredPackages(p, targets, lines)
			}
			if why != "" {
				return why
			}
			stdout = nil
			var envelopes []byte
			for _, l := range lines {
				stdout = append(stdout, l.key...)
				envelopes = append(append(envelopes, l.Check...), '\n')
			}
			// The check leg's call (checkModule) takes the envelopes, as
			// ergo check DIR/... prints them; the call's exit is check's
			// when every digest was computed, so only a pass is kept.
			if o.Passed() {
				check := o
				check.Stdout = envelopes
				r.keyed[keyedCall([]string{"check", pattern})] = check
			}
		}
		fmt.Fprintf(h, "leg %q status %d %s\nstdout %q\nstderr %q\n", args, o.Status, o.Signal, stdout, o.Stderr)
	}
	return ""
}

// emittedLine is one package's line of `ergo test --emitted --json`
// (cmd/ergo emittedjson.go), and key the line less its check envelope's
// producers, as the key holds it.
type emittedLine struct {
	Package string          `json:"package"`
	Check   json.RawMessage `json:"check"`
	Test    string          `json:"test"`
	Build   string          `json:"build"`
	key     []byte
}

// emittedLines decodes the lines of `ergo test --emitted --json`, or says
// why they are not such lines.
func emittedLines(stdout []byte) ([]emittedLine, string) {
	var out []emittedLine
	for _, text := range bytes.Split(bytes.TrimRight(stdout, "\n"), []byte("\n")) {
		var l emittedLine
		if err := json.Unmarshal(text, &l); err != nil || l.Package == "" || len(l.Check) == 0 {
			return nil, fmt.Sprintf("ergo test --emitted --json printed a line that is not a package's: %.80q", text)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(text, &fields) != nil {
			return nil, "ergo test --emitted --json printed a line that is not an object"
		}
		stripped, ok := envelopeWithoutProducer(l.Check)
		if !ok {
			return nil, fmt.Sprintf("package %s: its check is not an envelope", l.Package)
		}
		fields["check"] = stripped
		key, err := json.Marshal(fields)
		if err != nil {
			return nil, err.Error()
		}
		l.key = append(key, '\n')
		out = append(out, l)
	}
	return out, ""
}

// coveredPackages says why the packages of a pattern's lines are not the
// program's targets, or why its main package has no build digest, or "": a
// key that missed a target or the main package's build would not change
// when that one's emitted Go does, so the program is then not cached (fail
// closed).
func coveredPackages(p *program, targets []string, lines []emittedLine) string {
	module := ""
	if data, err := os.ReadFile(filepath.Join(p.dir, "ergo.mod")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
				module = strings.TrimSpace(name)
				break
			}
		}
	}
	if module == "" {
		return "ergo.mod names no module: the key cannot match its packages"
	}
	pathOf := func(folder string) (string, bool) {
		rel, err := filepath.Rel(p.dir, folder)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}
		if rel == "." {
			return module, true
		}
		return module + "/" + filepath.ToSlash(rel), true
	}
	var want, got []string
	for _, target := range targets {
		path, ok := pathOf(target)
		if !ok {
			return fmt.Sprintf("target %s is outside the program", target)
		}
		want = append(want, path)
	}
	main, ok := pathOf(p.main)
	if !ok {
		return fmt.Sprintf("main %s is outside the program", p.main)
	}
	built := false
	for _, l := range lines {
		got = append(got, l.Package)
		built = built || l.Package == main && l.Build != ""
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(slices.Compact(want), got) {
		return fmt.Sprintf("ergo test --emitted over the program named packages %v, not its targets %v", got, want)
	}
	if !built {
		return fmt.Sprintf("ergo test --emitted --json gave no build digest for the main package %s", main)
	}
	return ""
}

// keyedCall names a compiler call by its arguments, for runner.keyed. A
// call's result depends only on them and on what the key holds, so the leg
// that makes the same call next, in the same program's run, takes the
// key's result: on a miss the fmt leg and each check leg run once, not
// twice (gate-speed GS20).
func keyedCall(args []string) string { return strings.Join(args, "\x00") }

// buildArgs is an ergo build call of args, with -rewrites when the run
// names a mode; the verdict cache keys on these words, so a run under
// another mode never replays the default's pass.
func (r *runner) buildArgs(args ...string) []string {
	out := append([]string{"build"}, args...)
	if r.cfg.rewrites != "" {
		out = append(out, "-rewrites="+r.cfg.rewrites)
	}
	return out
}

// withoutProducer is a check envelope less its producers (its own and its
// snapshot's), the digest of the compiler's own bytes, which every compiler
// edit changes and no leg reads (checkTarget reads checked and
// evaluation). The output of a pattern's check is one envelope per
// package, each stripped alike (GS27). Output that is not JSON objects is
// kept whole.
func withoutProducer(stdout []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	var out [][]byte
	for {
		var envelope json.RawMessage
		err := decoder.Decode(&envelope)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stdout
		}
		stripped, ok := envelopeWithoutProducer(envelope)
		if !ok {
			return stdout
		}
		out = append(out, stripped)
	}
	if len(out) == 0 {
		return stdout
	}
	return bytes.Join(out, []byte("\n"))
}

// envelopeWithoutProducer is one check envelope less its producers, and
// whether it is a JSON object.
func envelopeWithoutProducer(stdout []byte) ([]byte, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(stdout, &envelope) != nil || envelope == nil {
		return nil, false
	}
	delete(envelope, "producer")
	var snapshot map[string]json.RawMessage
	if json.Unmarshal(envelope["snapshot"], &snapshot) == nil && snapshot != nil {
		delete(snapshot, "producer")
		if encoded, err := json.Marshal(snapshot); err == nil {
			envelope["snapshot"] = encoded
		}
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, false
	}
	return out, true
}

// count adds one program to the hits or the misses.
func (c *verdictCache) count(hit bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hit {
		c.hits++
	} else {
		c.misses++
	}
}

// openCache prepares the cache of a run, or returns nil without a
// directory. The common key part is computed once: a failure there leaves
// every program uncached with the reason.
func openCache(dir string, cfg *config) *verdictCache {
	if dir == "" {
		return nil
	}
	c := &verdictCache{dir: dir}
	common, early, err := commonKey(cfg)
	if err != nil {
		c.reason = err.Error()
	} else {
		c.common, c.early = common, early
	}
	return c
}

// commonKey hashes what every program's verdict reads beside its folder,
// and says whether it holds the driver's sources (early cutoff) rather
// than the compiler's and the runner's bytes.
func commonKey(cfg *config) (string, bool, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", cacheVersion)
	driver, err := driverDigest(cfg.root)
	early := err == nil
	if early {
		fmt.Fprintf(h, "driver %s\n", driver)
	} else {
		fmt.Fprintf(h, "no driver: %v\n", err)
		self, err := os.Executable()
		if err != nil {
			return "", false, fmt.Errorf("the runner's own binary: %w", err)
		}
		for _, f := range []struct{ what, path string }{{"runner", self}, {"ergo", cfg.ergo}} {
			sum, err := fileDigest(f.path)
			if err != nil {
				return "", false, fmt.Errorf("digest of the %s %s: %w", f.what, f.path, err)
			}
			fmt.Fprintf(h, "%s %s\n", f.what, sum)
		}
	}
	query := exec.Command("go", append([]string{"env", "-json"}, goSettings...)...)
	query.Dir = cfg.root
	out, err := query.Output()
	if err != nil {
		return "", false, fmt.Errorf("go env: %w", err)
	}
	fmt.Fprintf(h, "go %s\n", out)
	env := slices.Clone(os.Environ())
	sort.Strings(env)
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); slices.Contains(volatileEnv, name) {
			continue
		}
		fmt.Fprintf(h, "env %q\n", kv)
	}
	fmt.Fprintf(h, "settings timeout=%s budget=%s memory=%d root=%s\n", cfg.timeout, cfg.budget, cfg.memory, cfg.root)
	return hex.EncodeToString(h.Sum(nil)), early, nil
}

// fileDigest is the SHA-256 of a file's bytes, through links.
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// programKey is the key of one program: the common part and every entry of
// its folder (name, kind, mode and bytes; a link by its target), so an
// untracked file there counts too. "" and the reason when there is none.
func (c *verdictCache) programKey(r *runner, p *program) (string, string) {
	if c.reason != "" {
		return "", c.reason
	}
	for _, tag := range p.tags {
		if slices.Contains(uncachedTags, tag) {
			return "", "its tag " + tag + " reads inputs outside the key"
		}
	}
	h := sha256.New()
	fmt.Fprintf(h, "common %s\nprogram %s\n", c.common, p.name)
	err := filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(p.dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "link %q %q\n", filepath.ToSlash(rel), target)
		case d.IsDir():
			fmt.Fprintf(h, "dir %q %o\n", filepath.ToSlash(rel), info.Mode().Perm())
		case d.Type().IsRegular():
			sum, err := fileDigest(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "file %q %o %s\n", filepath.ToSlash(rel), info.Mode().Perm(), sum)
		default:
			return fmt.Errorf("%s is neither a file, a folder nor a link", path)
		}
		return nil
	})
	if err != nil {
		return "", err.Error()
	}
	if c.early {
		if why := r.legKey(h, p); why != "" {
			return "", why
		}
	}
	return hex.EncodeToString(h.Sum(nil)), ""
}

// entryPath is where the pass of a program at key is recorded.
func (c *verdictCache) entryPath(name, key string) string {
	return filepath.Join(c.dir, strings.ReplaceAll(name, "/", "_")+"-"+key[:32]+".json")
}

// lookup returns the recorded pass of p at key.
func (c *verdictCache) lookup(p *program, key string) (*cacheEntry, bool) {
	data, err := os.ReadFile(c.entryPath(p.name, key))
	if err != nil {
		return nil, false
	}
	var e cacheEntry
	if json.Unmarshal(data, &e) != nil || e.Version != cacheVersion || e.Key != key || e.Program != p.name {
		return nil, false
	}
	return &e, true
}

// record writes the pass of p at key, whole or not at all (a rename), and
// keeps the program's newest cacheKeep entries.
func (c *verdictCache) record(p *program, key string, output []byte, cases int) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cacheEntry{Version: cacheVersion, Program: p.name, Key: key, Output: string(output), Cases: cases})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, ".entry-")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), c.entryPath(p.name, key)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return c.prune(p.name)
}

// prune removes all but the newest cacheKeep entries of a program.
func (c *verdictCache) prune(name string) error {
	matches, err := filepath.Glob(filepath.Join(c.dir, strings.ReplaceAll(name, "/", "_")+"-*.json"))
	if err != nil || len(matches) <= cacheKeep {
		return err
	}
	type aged struct {
		path string
		mod  int64
	}
	var entries []aged
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil {
			entries = append(entries, aged{m, info.ModTime().UnixNano()})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod > entries[j].mod })
	var errs []error
	for _, e := range entries[min(cacheKeep, len(entries)):] {
		errs = append(errs, os.Remove(e.path))
	}
	return errors.Join(errs...)
}

// runCached runs p through the cache: a recorded pass is replayed, and a
// pass of a run is recorded.
func (r *runner) runCached(p *program) {
	c := r.cache
	if c == nil {
		r.runProgram(p)
		return
	}
	key, why := c.programKey(r, p)
	if key == "" {
		fmt.Fprintf(r.out, "examples: %s: not cached: %s\n", p.name, why)
		c.count(false)
		r.runProgram(p)
		return
	}
	if e, ok := c.lookup(p, key); ok {
		io.WriteString(r.out, e.Output)
		r.cases += e.Cases
		c.count(true)
		fmt.Fprintf(r.out, "ok %s: cached (inputs %s)\n", p.name, key[:12])
		return
	}
	c.count(false)
	var captured bytes.Buffer
	out, failures, cases := r.out, len(r.failures), r.cases
	r.out = io.MultiWriter(out, &captured)
	r.runProgram(p)
	r.out = out
	if len(r.failures) == failures {
		if err := c.record(p, key, captured.Bytes(), r.cases-cases); err != nil {
			fmt.Fprintf(r.out, "examples: %s: pass not recorded: %v\n", p.name, err)
		}
	}
}
