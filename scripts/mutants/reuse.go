package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"ergo.local/prototype/scripts/gate/replay"
	"ergo.local/prototype/scripts/mutants/covfile"
)

// Verdict reuse (GATE2 GT2): a mutant pass judges a row again only when what
// its verdict rests on changed. A row's key (rowKey) holds the row (its
// file, edit and killers), the runner, the toolchain, and the content of
// every file its killers read: for a killer in a Go test package of the
// module, the package's closure (the test binary's sources and embeds, its
// testdata, its declared inputs, as internal/closure declares them) with
// the closures of the tools its suite runs; for any other killer (an
// example program, a tagged build, a nested module) every tracked and
// untracked file. The mutated file is always in it. A killed verdict whose
// killer is one of the row's killers is stored under the key; while the
// key is unchanged the pass reports it again and judges nothing. A
// survivor, an uncovered, stale or invalid row is judged on every pass.
// -fresh judges every row (the reference), and -all, which names every
// killer, reuses nothing.

// reuseVersion names the store's format and the meaning of its keys. v2
// (GT-COMPILE): a v1 killed verdict may rest on a compile error.
const reuseVersion = "ergo-mutants-reuse-v2"

// reuseCacheVariable names the store's directory.
const reuseCacheVariable = covfile.CacheVariable

// reuseInputs are what the row keys of one checkout read.
type reuseInputs struct {
	// toolchain names the runner and the go command's settings.
	toolchain string
	// files are the tracked and untracked files, by path, with their
	// content digests; ignored gives an ignored file's digest, or false.
	files   map[string]string
	ignored func(string) (string, bool)
	ignores []string
	// closures are what each suite's tests read, by suite name; a nil
	// closure, or a suite not here, reads every file.
	closures map[string]*replay.Closure
}

// rowKey is the key of a row's verdict over in.
func rowKey(in *reuseInputs, m *Mutant) string {
	h := sha256.New()
	put := func(parts ...string) {
		for _, p := range parts {
			fmt.Fprintf(h, "%d:%s", len(p), p)
		}
		h.Write([]byte{'\n'})
	}
	put("version", reuseVersion)
	put("toolchain", in.toolchain)
	put("row", m.Name, m.File, m.Decl, m.From, m.To)
	killers := killerIDs(m)
	slices.Sort(killers)
	whole := false
	var read []*replay.Closure
	for _, id := range killers {
		put("killer", id)
		suite, _ := splitID(id)
		if c := in.closures[suite]; c != nil {
			read = append(read, c)
		} else {
			whole = true
		}
	}
	covered := func(p string) bool {
		return p == m.File || slices.ContainsFunc(read, func(c *replay.Closure) bool { return c.Covers(p) })
	}
	for _, p := range slices.Sorted(maps.Keys(in.files)) {
		if whole || covered(p) {
			put("file", p, in.files[p])
		}
	}
	for _, p := range in.ignores {
		if covered(p) {
			if digest, ok := in.ignored(p); ok {
				put("ignored", p, digest)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// reuseStore holds killed verdicts by row key, one file each.
type reuseStore struct{ dir string }

func openReuseStore() (reuseStore, error) {
	dir, err := covfile.CacheDir()
	if err != nil {
		return reuseStore{}, err
	}
	return reuseStore{filepath.Join(dir, reuseVersion)}, nil
}

func (s reuseStore) path(key string) string { return filepath.Join(s.dir, key[:2], key+".json") }

// lookup is the killed verdict stored at key.
func (s reuseStore) lookup(key string) (Result, bool) {
	data, err := os.ReadFile(s.path(key))
	if err != nil {
		return Result{}, false
	}
	var r Result
	if json.Unmarshal(data, &r) != nil || r.Status != Killed {
		return Result{}, false
	}
	return r, true
}

// record stores a killed verdict at key: a new file, renamed into place.
func (s reuseStore) record(key string, r Result) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	file := s.path(key)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), key+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err = errors.Join(err, f.Close()); err == nil {
		err = os.Rename(f.Name(), file)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// storable says whether a verdict may be stored for m: killed by one of
// the row's own killers, whose closures its key holds.
func storable(m *Mutant, r Result) bool {
	return r.Status == Killed && slices.Contains(killerIDs(m), r.Killer)
}

// partitionReuse splits the selected rows of jobs into the verdicts the
// store holds at their keys (reported again, with a note) and the jobs that
// judge the rest (each selecting exactly its rows still to judge; a job
// with none is left out). keys are the selected rows' keys, by ledger and
// name.
func partitionReuse(jobs []Job, in *reuseInputs, lookup func(string) (Result, bool)) (reused []Result, judge []Job, keys map[string]string) {
	keys = map[string]string{}
	for _, j := range jobs {
		var rest []string
		for _, m := range j.Ledger.Mutants {
			if j.Select != nil && !j.Select.MatchString(m.Name) {
				continue
			}
			key := rowKey(in, m)
			keys[j.Path+"\x00"+m.Name] = key
			if r, ok := lookup(key); ok && storable(m, r) {
				// The row's own killers, so that -write leaves the row
				// as it is (record).
				r.Ledger, r.Name, r.Killers = j.Path, m.Name, slices.Clone(m.Killers)
				r.Detail = fmt.Sprintf("reused: the verdict at key %s", key[:12])
				reused = append(reused, r)
				continue
			}
			rest = append(rest, regexp.QuoteMeta(m.Name))
		}
		if len(rest) > 0 {
			j.Select = regexp.MustCompile("^(" + strings.Join(rest, "|") + ")$")
			judge = append(judge, j)
		}
	}
	return reused, judge, keys
}

// inLedgerOrder orders results as the jobs' rows are.
func inLedgerOrder(jobs []Job, results []Result) []Result {
	at := map[string]int{}
	for _, j := range jobs {
		for _, m := range j.Ledger.Mutants {
			at[j.Path+"\x00"+m.Name] = len(at)
		}
	}
	slices.SortStableFunc(results, func(a, b Result) int { return at[a.Ledger+"\x00"+a.Name] - at[b.Ledger+"\x00"+b.Name] })
	return results
}

// gatherReuse reads the inputs of the row keys of the selected rows of jobs
// in the checkout at root.
func gatherReuse(root string, jobs []Job) (*reuseInputs, error) {
	in := &reuseInputs{closures: map[string]*replay.Closure{}}
	toolchain, err := toolchainOf(root)
	if err != nil {
		return nil, err
	}
	in.toolchain = toolchain
	if in.files, err = digestFiles(root, "-c", "-o", "--exclude-standard"); err != nil {
		return nil, err
	}
	if in.ignores, err = listFiles(root, "-o", "-i", "--exclude-standard"); err != nil {
		return nil, err
	}
	digests := map[string]string{}
	in.ignored = func(p string) (string, bool) {
		if d, ok := digests[p]; ok {
			return d, d != ""
		}
		d, err := fileDigest(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			d = ""
		}
		digests[p] = d
		return d, d != ""
	}
	suites := map[string]bool{}
	for _, j := range jobs {
		for _, m := range j.Ledger.Mutants {
			if j.Select == nil || j.Select.MatchString(m.Name) {
				for _, id := range killerIDs(m) {
					suite, _ := splitID(id)
					suites[suite] = true
				}
			}
		}
	}
	listed, err := listModule(root)
	if err != nil {
		return nil, err
	}
	_, tools, _ := ergoSuites()
	for _, name := range slices.Sorted(maps.Keys(suites)) {
		s, _, ok := namedSuite(root, name)
		if !ok || s.Command != nil || s.Module != "" || s.Tags != "" {
			continue // every file
		}
		c := listed.testClosure(strings.TrimPrefix(s.Package, "./"))
		for _, t := range tools {
			if slices.Contains(toolsOf(&s), t.Name) && c != nil {
				c = unionClosure(c, listed.programClosure(strings.TrimPrefix(t.Package, "./")))
			}
		}
		if c != nil {
			in.closures[name] = c
		}
	}
	return in, nil
}

// toolchainOf names the runner's executable and the go command's settings.
func toolchainOf(root string) (string, error) {
	out, err := covfile.GoEnv(goCommand(), root, goEnviron())
	if err != nil {
		return "", err
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	digest, err := fileDigest(self)
	if err != nil {
		return "", err
	}
	return "runner " + digest + "\n" + out, nil
}

func fileDigest(name string) (string, error) {
	f, err := os.Open(name)
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

// listFiles is git ls-files with args, as slash paths.
func listFiles(root string, args ...string) ([]string, error) {
	list := exec.Command("git", append([]string{"--no-optional-locks", "ls-files", "-z"}, args...)...)
	list.Dir, list.Env = root, goEnviron()
	out, err := list.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// digestFiles digests the files git ls-files lists with args; a listed
// file that is gone (deleted, not staged) has no digest.
func digestFiles(root string, args ...string) (map[string]string, error) {
	files, err := listFiles(root, args...)
	if err != nil {
		return nil, err
	}
	digests := map[string]string{}
	for _, f := range files {
		d, err := fileDigest(filepath.Join(root, filepath.FromSlash(f)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		digests[f] = d
	}
	return digests, nil
}

// listedModule is `go list -e -deps -test` of the module, as the gate
// reads it (scripts/gate testPackages, internal/closure modulePackages).
type listedModule struct {
	root   string
	byPath map[string]listedPkg
	byDir  map[string]listedPkg
	table  map[string][]replay.Allowance
	// problem is set when the closure table cannot be read: every package
	// then reads every file.
	problem bool
}

type listedPkg struct {
	ImportPath, Dir, ForTest                    string
	Deps, TestGoFiles, XTestGoFiles             []string
	EmbedFiles, TestEmbedFiles, XTestEmbedFiles []string
}

func listModule(root string) (*listedModule, error) {
	list := exec.Command(goCommand(), "list", "-e", "-deps", "-test", "-json=ImportPath,Dir,ForTest,Deps,TestGoFiles,XTestGoFiles,EmbedFiles,TestEmbedFiles,XTestEmbedFiles", "./...")
	list.Dir, list.Env = root, goEnviron()
	out, err := list.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps -test ./...: %w", err)
	}
	// go list names absolute directories: a relative root (the default
	// -root ".") is made absolute first, or no package is inside it.
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	l := &listedModule{root: resolved, byPath: map[string]listedPkg{}, byDir: map[string]listedPkg{}}
	for decoder := json.NewDecoder(bytes.NewReader(out)); ; {
		var p listedPkg
		if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		l.byPath[p.ImportPath] = p
		if dir, inside := l.relative(p.Dir); inside && p.ForTest == "" && !strings.Contains(p.ImportPath, " ") && !strings.HasSuffix(p.ImportPath, ".test") {
			l.byDir[dir] = p
		}
	}
	source, err := os.ReadFile(filepath.Join(root, replay.DeclaredFile))
	if err == nil {
		l.table, err = replay.ParseDeclared(source)
	}
	l.problem = err != nil
	return l, nil
}

func (l *listedModule) relative(dir string) (string, bool) {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	rel, err := filepath.Rel(l.root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (l *listedModule) closureOf(dir string, imports []string) *replay.Closure {
	c := &replay.Closure{Dir: dir}
	for _, imported := range imports {
		p, ok := l.byPath[imported]
		if !ok {
			continue
		}
		pdir, inside := l.relative(p.Dir)
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

// testClosure is what the tests of the package in dir read, or nil when
// it is no test package of the module (every file then).
func (l *listedModule) testClosure(dir string) *replay.Closure {
	p, ok := l.byDir[dir]
	if !ok || len(p.TestGoFiles)+len(p.XTestGoFiles) == 0 || l.problem {
		return nil
	}
	main, ok := l.byPath[p.ImportPath+".test"]
	if !ok {
		return nil
	}
	c := l.closureOf(dir, append([]string{p.ImportPath + " [" + p.ImportPath + ".test]", p.ImportPath + "_test [" + p.ImportPath + ".test]"}, main.Deps...))
	c.Declared = l.table[dir]
	for _, a := range c.Declared {
		if a.Grant != replay.GrantProgram {
			continue
		}
		if program := l.programClosure(a.Path); program != nil {
			if c.Programs == nil {
				c.Programs = map[string]*replay.Closure{}
			}
			c.Programs[a.Path] = program
		}
	}
	return c
}

// programClosure is what building the package in dir reads, or nil.
func (l *listedModule) programClosure(dir string) *replay.Closure {
	p, ok := l.byDir[dir]
	if !ok {
		return nil
	}
	return l.closureOf(dir, append([]string{p.ImportPath}, p.Deps...))
}

// unionClosure reads what a and b read; with b nil, every file (nil).
func unionClosure(a, b *replay.Closure) *replay.Closure {
	if a == nil || b == nil {
		return nil
	}
	u := &replay.Closure{
		Dir:      a.Dir,
		Deps:     append(append([]string{b.Dir}, a.Deps...), b.Deps...),
		Embeds:   append(slices.Clone(a.Embeds), b.Embeds...),
		Declared: append(slices.Clone(a.Declared), b.Declared...),
	}
	if len(a.Programs)+len(b.Programs) > 0 {
		u.Programs = map[string]*replay.Closure{}
		maps.Copy(u.Programs, b.Programs)
		maps.Copy(u.Programs, a.Programs)
	}
	return u
}

// reusePass is run's part around the runner: the verdicts the store holds
// for the selected rows, the jobs left to judge, and after the run the
// store of the new killed verdicts whose keys held through it.
type reusePass struct {
	root  string
	store reuseStore
	keys  map[string]string
	log   io.Writer
}

// begin partitions jobs (partitionReuse) over the checkout's inputs. On
// any error it reuses nothing and says why.
func (p *reusePass) begin(jobs []Job) ([]Result, []Job) {
	in, err := gatherReuse(p.root, jobs)
	if err != nil {
		fmt.Fprintf(p.log, "mutants: no verdict reused: %v\n", err)
		return nil, jobs
	}
	reused, judge, keys := partitionReuse(jobs, in, p.store.lookup)
	p.keys = keys
	fmt.Fprintf(p.log, "mutants: %d verdicts reused, %d rows to judge (%s)\n", len(reused), len(keys)-len(reused), p.store.dir)
	return reused, judge
}

// end stores each killed verdict of results whose row's key is unchanged
// at the end of the run.
func (p *reusePass) end(jobs []Job, results []Result) {
	if p.keys == nil {
		return
	}
	in, err := gatherReuse(p.root, jobs)
	if err != nil {
		fmt.Fprintf(p.log, "mutants: no verdict stored: %v\n", err)
		return
	}
	stored := storeVerdicts(jobs, results, p.keys, in, func(key string, r Result) error {
		err := p.store.record(key, r)
		if err != nil {
			fmt.Fprintf(p.log, "mutants: %s: not stored: %v\n", r.Name, err)
		}
		return err
	})
	fmt.Fprintf(p.log, "mutants: %d killed verdicts stored at %s\n", stored, time.Now().UTC().Format(time.RFC3339))
}

// storeVerdicts records each judged killed verdict of results that is
// storable at its row's key from the start of the run (keys), when in, the
// inputs at its end, give the row the same key: a row whose inputs changed
// during the run stores nothing. It returns how many it recorded.
func storeVerdicts(jobs []Job, results []Result, keys map[string]string, in *reuseInputs, record func(string, Result) error) int {
	rows := map[string]*Mutant{}
	for _, j := range jobs {
		for _, m := range j.Ledger.Mutants {
			rows[j.Path+"\x00"+m.Name] = m
		}
	}
	stored := 0
	for _, r := range results {
		id := r.Ledger + "\x00" + r.Name
		m, key := rows[id], keys[id]
		if m == nil || key == "" || strings.HasPrefix(r.Detail, "reused:") || !storable(m, r) || rowKey(in, m) != key {
			continue
		}
		if record(key, r) == nil {
			stored++
		}
	}
	return stored
}
