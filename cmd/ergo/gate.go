package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/task"
)

// The built-in steps of `ergo task` (docs/spec/build.md, Tasks; MONOREPO
// WS2, TASK-GATE): check, fmt --check, lint and test run once per member
// of the workspace, gen --check once over it. Each states what it reads,
// its key (task.Key), and the runner keeps a passing verdict under it, so
// a step whose inputs are unchanged replays its verdict and starts no
// work:
//
//   - check, lint and test at member M: every file of M (the folders
//     discovery walks) and of every folder M's go.mod replaces a module
//     with, transitively, a member it builds against (GATE-KEY-REACHES);
//     the law ledger as the run left it; the toolchain, the options and
//     the GO*, CGO_* and ERGO_* environment; and the values of the
//     variables the task's env lines name.
//   - fmt --check at M: the formatter's identity and M's .ergo files, by
//     content, and the names of the .ergo files of each package folder.
//   - gen --check: the generators' own store (internal/task Gen), which
//     replays each generator whose inputs hold; the step keeps no verdict
//     of its own.
//
// Inside a step, lint and fmt keep their per-package and per-file
// verdicts too (lawcache), so a member whose key missed replays the
// packages that did not change; check keeps its law runs (lawcache) and
// test its results (testcache.go).
//
// Prior art: go test's cache replays a package's passing result keyed by
// its inputs; Bazel keys each action by its input digests; Turborepo
// hashes a task's input files into its cache key, and its
// affectedUsingTaskInputs selects by them, as --affected does here.

// gateHeld starts each stored verdict text, so an empty report is a
// verdict too (the store keeps no empty text).
const gateHeld = "held\n"

// gateWorkspace is one `ergo task` run's view of the workspace: its
// members, and the loads its steps share (a package is loaded once per
// run and read by check and test; lint reads a load before check
// evaluates it, or its own).
type gateWorkspace struct {
	root    string
	ws      sketchfront.Workspace
	store   *lawcache.Store
	opts    map[int]fileOptions
	pkgs    map[int][]sketchfront.PackageTarget
	loads   map[string]*gateLoad
	first   map[string]string
	generic fileOptions
}

// gateLoad is a package's load in this run, and whether check has set
// its evaluation on it.
type gateLoad struct {
	l         loaded
	err       error
	evaluated bool
}

func newGateWorkspace(root string, ws sketchfront.Workspace) (*gateWorkspace, error) {
	opts, err := fileOptionsOf("test", []string{root})
	if err != nil {
		return nil, err
	}
	return &gateWorkspace{
		root: root, ws: ws, store: lawStore(), generic: opts,
		opts: map[int]fileOptions{}, pkgs: map[int][]sketchfront.PackageTarget{},
		loads: map[string]*gateLoad{}, first: map[string]string{},
	}, nil
}

// memberOptions are the test options of member i.
func (w *gateWorkspace) memberOptions(i int) (fileOptions, error) {
	if o, ok := w.opts[i]; ok {
		return o, nil
	}
	o, err := fileOptionsOf("test", []string{w.ws.Members[i].Dir})
	if err != nil {
		return fileOptions{}, err
	}
	w.opts[i] = o
	return o, nil
}

// packages are the packages of member i, nested members left out.
func (w *gateWorkspace) packages(i int) ([]sketchfront.PackageTarget, error) {
	if p, ok := w.pkgs[i]; ok {
		return p, nil
	}
	p, err := sketchfront.DiscoverPackages(w.ws.Members[i].Dir)
	if err != nil {
		return nil, err
	}
	w.pkgs[i] = p
	return p, nil
}

// load is pkg's load, shared by the run's steps; fresh gives one check
// has not evaluated (lint's).
func (w *gateWorkspace) load(pkg sketchfront.PackageTarget, opts fileOptions, fresh bool) *gateLoad {
	if g, ok := w.loads[pkg.Dir]; ok && !(fresh && g.evaluated) {
		return g
	}
	l, err := loadPackageThrough(nil, pkg.Dir, opts.target)
	g := &gateLoad{l: l, err: err}
	if !fresh || w.loads[pkg.Dir] == nil {
		w.loads[pkg.Dir] = g
	}
	return g
}

// rel is p relative to the workspace root, slash-separated; ok is false
// outside it.
func (w *gateWorkspace) rel(p string) (string, bool) {
	r, err := filepath.Rel(w.root, p)
	if err != nil || !filepath.IsLocal(r) && r != "." {
		return "", false
	}
	return filepath.ToSlash(r), true
}

// moduleKey is check's, lint's and test's key at member i (the top of
// this file): its parts named by path below the workspace root, and the
// member folders it reads as its paths.
func (w *gateWorkspace) moduleKey(i int, env []string) (task.Key, error) {
	m := w.ws.Members[i]
	opts, err := w.memberOptions(i)
	if err != nil {
		return task.Key{}, err
	}
	var key task.Key
	add := func(name, value string) {
		key.Parts = append(key.Parts, task.Part{Name: name, Digest: lawcache.Key(value)})
	}
	tc := warmToolchain(opts)
	goCommand, ok := "", false
	if tc != nil {
		goCommand, ok = goStamp(tc.Go)
	}
	if !ok {
		// No toolchain to key on: the step keeps no verdict, by a part no
		// other run has.
		add("toolchain", "unavailable "+rand.Text())
	} else {
		add("toolchain", strings.Join([]string{"gate module/3", warmOptions(opts), goCommand, tc.Root, tc.GOOS + "/" + tc.GOARCH, tc.GOFLAGS,
			strings.Join(tc.Tags, ","), strconv.FormatBool(tc.GoBuild) + " " + strconv.FormatBool(tc.Strip)}, "\n"))
	}
	add("environment", strings.Join(lawcache.Environ(os.Environ(), sketchfront.StageReportVariable), "\n"))
	for _, name := range env {
		v, set := os.LookupEnv(name)
		add("env "+name, strconv.FormatBool(set)+" "+v)
	}
	ledger := ledgerPath(m.Dir)
	add("law ledger", ledgerPart(ledger))
	if err := w.treeParts(m.Dir, ledger, map[string]bool{}, &key); err != nil {
		return task.Key{}, err
	}
	return key, nil
}

// treeParts adds the parts of the files below dir (the folders discovery
// walks; skip, the law ledger, left out), then, for each folder dir's
// go.mod replaces a module with, its own, transitively: a build reads the
// replacing folder's sources, so an edit there changes the key of every
// member that reaches it (GATE-KEY-REACHES). seen holds the folders
// already walked, so a cycle of replaces ends. Each folder walked is one
// of the key's paths.
func (w *gateWorkspace) treeParts(dir, skip string, seen map[string]bool, key *task.Key) error {
	seen[dir] = true
	base, inside := w.rel(dir)
	name := func(rel string) string {
		if !inside {
			return filepath.ToSlash(filepath.Join(dir, rel))
		}
		return path.Join(base, filepath.ToSlash(rel))
	}
	if inside {
		key.Paths = append(key.Paths, base)
	}
	bind := filepath.Join(dir, sketchfront.BindFolder)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			inBind := p == bind || strings.HasPrefix(p, bind+string(filepath.Separator))
			if p == dir || inBind {
				return nil
			}
			// The root's bindings are inputs, but not package folders.
			if !sketchfront.AdmitFolder(d.Name()) {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, sketchfront.ModFile)); err == nil {
				return filepath.SkipDir
			}
			key.Parts = append(key.Parts, task.Part{Name: "folder " + name(rel), Digest: "-"})
			return nil
		}
		if p == skip {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		stage.Add(stage.BytesHashed, int64(len(data)))
		sum := sha256.Sum256(data)
		key.Parts = append(key.Parts, task.Part{Name: name(rel), Digest: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		return err
	}
	targets, err := sketchfront.LocalReplaces(dir)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if seen[target] {
			continue
		}
		key.Parts = append(key.Parts, task.Part{Name: "replace " + name(".") + " => " + target, Digest: "-"})
		if err := w.treeParts(target, "", seen, key); err != nil {
			return err
		}
	}
	return nil
}

// fmtKey is fmt --check's key at member i: the formatter, each .ergo
// file of its packages by content, and each package folder's .ergo
// names; its paths are those files and, per package folder, any .ergo
// file in it ("DIR/*.ergo"), so a new file selects it.
func (w *gateWorkspace) fmtKey(i int) (task.Key, error) {
	pkgs, err := w.packages(i)
	if err != nil {
		return task.Key{}, err
	}
	key := task.Key{Parts: []task.Part{{Name: "formatter", Digest: lawcache.Key(sketchfront.FormatterIdentity)}}}
	for _, pkg := range pkgs {
		files, err := gateSources(pkg.Dir)
		if err != nil {
			return task.Key{}, err
		}
		dir, _ := w.rel(pkg.Dir)
		var names []string
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				return task.Key{}, err
			}
			sum := sha256.Sum256(data)
			r, _ := w.rel(f)
			key.Parts = append(key.Parts, task.Part{Name: r, Digest: hex.EncodeToString(sum[:])})
			key.Paths = append(key.Paths, r)
			names = append(names, filepath.Base(f))
		}
		key.Parts = append(key.Parts, task.Part{Name: "sources of " + dir, Digest: lawcache.Key(names...)})
		key.Paths = append(key.Paths, path.Join(dir, "*"+sourceExtension))
	}
	return key, nil
}

// fail records a failed verdict of step id and prints it.
func (w *gateWorkspace) fail(id, verb, what, message string) {
	line := verb + " " + what + ": " + message
	if _, ok := w.first[id]; !ok {
		w.first[id] = line
	}
	fmt.Fprintln(os.Stderr, line)
}

// check is check at member i: every package checks and its laws hold.
func (w *gateWorkspace) check(id string, i int) (bool, error) {
	opts, err := w.memberOptions(i)
	if err != nil {
		return false, err
	}
	pkgs, err := w.packages(i)
	if err != nil {
		return false, err
	}
	held := true
	for _, pkg := range pkgs {
		fmt.Fprintf(os.Stdout, "package %s\n", pkg.Path)
		g := w.load(pkg, opts, false)
		if g.err != nil {
			w.fail(id, "check", pkg.Path, g.err.Error())
			held = false
			continue
		}
		id2 := producer.Current()
		if err := g.l.result.Qualify(id2); err != nil {
			return false, err
		}
		if !g.l.result.Checked {
			env := g.l.result.CheckEnvelope()
			if err := printProjectionJSON(env); err != nil {
				return false, err
			}
			msg := "check failed"
			if len(env.Diagnostics) > 0 {
				d := env.Diagnostics[0]
				msg = fmt.Sprintf("%s:%d:%d: %s %s", d.File, d.Span.Line, d.Span.Column, d.Code, d.Message)
			}
			w.fail(id, "check", pkg.Path, msg)
			held = false
			continue
		}
		one := opts
		one.file = pkg.Dir
		one.command = "check"
		out, exit, err := evaluatedEnvelope(one, g.l, id2, nil)
		if err != nil {
			return false, err
		}
		g.evaluated = true
		os.Stdout.Write(out)
		if exit != nil {
			w.fail(id, "check", pkg.Path, exit.Error())
			held = false
		}
	}
	boundLaws(w.store)
	return held, nil
}

// format is fmt --check at member i: every .ergo file is in ergo fmt's
// layout.
func (w *gateWorkspace) format(id string, i int) (bool, error) {
	pkgs, err := w.packages(i)
	if err != nil {
		return false, err
	}
	held := true
	for _, pkg := range pkgs {
		files, err := gateSources(pkg.Dir)
		if err != nil {
			return false, err
		}
		for _, file := range files {
			held = w.formatFile(id, file) && held
		}
	}
	return held, nil
}

func (w *gateWorkspace) formatFile(id, file string) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		w.fail(id, "fmt", file, err.Error())
		return false
	}
	key := lawcache.Key("gate fmt/1", sketchfront.FormatterIdentity, string(data))
	if _, ok := w.store.Get(key); ok {
		return true
	}
	result, err := sketchfront.Format(string(data), maxFormatOutputBytes)
	if err != nil {
		w.fail(id, "fmt", file, err.Error())
		return false
	}
	if result.Changed {
		w.fail(id, "fmt", file, "not formatted; run ergo fmt")
		return false
	}
	w.store.Put(key, gateHeld)
	return true
}

// lint is `ergo lint` of each package of member i with the member's
// configuration: every package passes its policy.
func (w *gateWorkspace) lint(id string, i int) (bool, error) {
	opts, err := w.memberOptions(i)
	if err != nil {
		return false, err
	}
	pkgs, err := w.packages(i)
	if err != nil {
		return false, err
	}
	config := gateLintConfig(w.ws.Members[i].Dir)
	held := true
	for _, pkg := range pkgs {
		fmt.Fprintf(os.Stdout, "package %s\n", pkg.Path)
		// lint reads the load before check's evaluation is set on it, as
		// `ergo lint` reads a fresh load.
		g := w.load(pkg, opts, true)
		if g.err != nil || !g.l.result.Checked {
			w.fail(id, "lint", pkg.Path, "the package does not check")
			held = false
			continue
		}
		key := lawcache.Key("gate lint/1", g.l.result.Revision, config)
		if text, ok := w.store.Get(key); ok {
			os.Stdout.WriteString(strings.TrimPrefix(text, gateHeld))
			continue
		}
		result, err := lintLoaded(g.l, nil, packSelection{})
		if err != nil {
			w.fail(id, "lint", pkg.Path, err.Error())
			held = false
			continue
		}
		findings := g.l.result.ReportWithLint(g.l.snapshot, false, result.Advice())
		var b bytes.Buffer
		if err := findings.WriteText(&b, g.l.source.String()); err != nil {
			return false, err
		}
		os.Stdout.Write(b.Bytes())
		if !findings.PolicyPassed {
			w.fail(id, "lint", pkg.Path, "lint failed policy")
			held = false
			continue
		}
		w.store.Put(key, gateHeld+b.String())
	}
	return held, nil
}

// test is `ergo test` of each package of member i.
func (w *gateWorkspace) test(id string, i int) (bool, error) {
	opts, err := w.memberOptions(i)
	if err != nil {
		return false, err
	}
	pkgs, err := w.packages(i)
	if err != nil {
		return false, err
	}
	held := true
	for _, pkg := range pkgs {
		fmt.Fprintf(os.Stdout, "package %s\n", pkg.Path)
		g := w.load(pkg, opts, false)
		if g.err != nil || !g.l.result.Checked {
			w.fail(id, "test", pkg.Path, "the package does not check")
			held = false
			continue
		}
		one := opts
		one.file = pkg.Dir
		if err := testLoadedPackage(one, g.l, nil); err != nil {
			stop, _ := packageOutcome(pkg.Path, err)
			if stop != nil {
				return false, stop
			}
			w.fail(id, "test", pkg.Path, "tests failed")
			held = false
		}
	}
	return held, nil
}

// gateSources are the source files of the package folder dir, sorted.
func gateSources(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == sourceExtension && !e.IsDir() {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	slices.Sort(files)
	return files, nil
}

// gateLintConfig is the member's lint configuration document as a key
// part.
func gateLintConfig(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "ergo.lint.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return "no lint config"
	}
	return "lint config " + string(data)
}

// captured runs run with the process's stdout and stderr going to one
// buffer, and answers what it wrote: a step's whole output, kept as its
// log. Steps run one at a time, so the swap is the run's alone.
func captured(run func() (bool, error)) (bool, []byte, error) {
	f, err := os.CreateTemp("", "ergo-task-step-*.log")
	if err != nil {
		return false, nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = f, f
	pass, runErr := func() (pass bool, err error) {
		defer func() { os.Stdout, os.Stderr = stdout, stderr }()
		return run()
	}()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		return false, nil, err
	}
	return pass, data, runErr
}
