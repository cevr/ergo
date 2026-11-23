package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
)

// The gate's core (CI-SELECT spec §7.3, GATE-CORE): one process runs the
// verdicts a module repeats before a push, check, fmt --check, lint, test
// and gen --check, over every package of the module. Each package is
// loaded once, and the five verdicts read that load. `ergo gate`
// (CS-GATE-USER) wraps gateModule; no command calls it yet.
//
// Every verdict that held is kept in the law-verdict store (lawcache),
// under the compiler's identity and a content key, so a verdict whose
// inputs are unchanged is replayed, not computed:
//
//   - the module's verdict: the bytes of every file of the module (the
//     folders discovery walks) and of every folder its go.mod replaces a
//     module with, transitively (a workspace member it builds against),
//     the law ledger as the run left it, the
//     toolchain, the options and the GO*, CGO_* and ERGO_* environment. A
//     hit replays every package's verdicts with no discovery, parse,
//     check, build or process.
//   - fmt --check: the formatter's identity and the file's bytes.
//   - lint: the program's sources (its revision) and the lint
//     configuration document.
//   - gen --check: the kind, the operation type, the contract package's
//     import path and program sources, and the bytes of every generated
//     package beside it (a cli or mcp package reads the rpc package's).
//   - check's law runs: their verdicts, as check stores them; test: the
//     test result cache (testcache.go).
//
// Only a verdict that held is kept: a failure runs again every time, as
// in go test's cache and the law-verdict store.
//
// Prior art: go test's cache replays a package's passing result keyed by
// its inputs; Bazel keys each action by its input digests; Turborepo
// hashes a task's input files into its cache key, which this module key
// does for the whole module.

// gateRun is what one run of the gate did: by verb (check, fmt, lint,
// test, gen), how many verdicts it computed and how many it replayed from
// a store, and each verdict that failed, as "VERB WHAT: message".
type gateRun struct {
	Computed map[string]int
	Replayed map[string]int
	Failed   []string
}

// gatePackage is one package's passing verdicts as the module's verdict
// keeps them: the check envelope and the lint report it printed, its
// stored test result (nil for a package with nothing to test, whose run
// emptyTestRun prints again from its revision), and how many fmt and gen
// verdicts it held.
type gatePackage struct {
	Path     string `json:"path"`
	Dir      string `json:"dir"`
	Revision string `json:"revision"`
	Files    int    `json:"files"`
	Gens     int    `json:"gens"`
	Check    []byte `json:"check"`
	Lint     []byte `json:"lint"`
	Test     []byte `json:"test,omitempty"`
}

// gateHeld starts each stored verdict text, so an empty report is a
// verdict too (the store keeps no empty text).
const gateHeld = "held\n"

// gateModule runs the gate over the module whose root is dir, printing
// what each verdict prints. A failed verdict is in the run's Failed; the
// error is a defect or a stop that ends the run.
func gateModule(dir string) (gateRun, error) {
	run := gateRun{Computed: map[string]int{}, Replayed: map[string]int{}}
	root, err := filepath.Abs(dir)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return run, err
	}
	opts, err := fileOptionsOf("test", []string{root})
	if err != nil {
		return run, err
	}
	store := lawStore()
	ledger := ledgerPath(root)
	tree := gateTreeKey(root, opts, ledger)
	if tree != "" {
		if text, ok := store.Get(lawcache.Key(tree, ledgerPart(ledger))); ok {
			var kept []gatePackage
			if json.Unmarshal([]byte(text), &kept) == nil {
				return run, run.replay(opts, kept)
			}
		}
	}
	packages, err := sketchfront.DiscoverPackages(root)
	if err != nil {
		return run, err
	}
	generated := gateGenerated(packages)
	config := gateLintConfig(root)
	kept, complete := []gatePackage{}, true
	for _, pkg := range packages {
		fmt.Fprintf(os.Stdout, "package %s\n", pkg.Path)
		p, held, err := run.pkg(opts, store, pkg, generated[pkg.Dir], config)
		if err != nil {
			return run, err
		}
		kept = append(kept, p)
		complete = complete && held
	}
	if tree != "" && complete && len(run.Failed) == 0 {
		if data, err := json.Marshal(kept); err == nil {
			store.Put(lawcache.Key(tree, ledgerPart(ledger)), string(data))
		}
	}
	boundLaws(store)
	return run, nil
}

// replay prints the module's kept verdicts as the run that kept them
// printed them.
func (r *gateRun) replay(opts fileOptions, kept []gatePackage) error {
	for _, p := range kept {
		fmt.Fprintf(os.Stdout, "package %s\n", p.Path)
		r.Replayed["fmt"] += p.Files
		r.Replayed["gen"] += p.Gens
		r.Replayed["lint"]++
		os.Stdout.Write(p.Lint)
		r.Replayed["check"]++
		os.Stdout.Write(p.Check)
		r.Replayed["test"]++
		if p.Test != nil {
			if err := replayTestResult(p.Test, p.Dir, os.Stdout, os.Stderr); err != nil {
				return err
			}
			continue
		}
		one := opts
		one.file = p.Dir
		if err := emptyTestRun(one, p.Revision, os.Stdout, os.Stderr); err != nil {
			return err
		}
	}
	return nil
}

// fail records a failed verdict and prints it.
func (r *gateRun) fail(verb, what, message string) {
	line := verb + " " + what + ": " + message
	r.Failed = append(r.Failed, line)
	fmt.Fprintln(os.Stderr, line)
}

// pkg runs the verdicts of one package from one load. held reports
// whether each verdict held and can be kept.
func (r *gateRun) pkg(opts fileOptions, store *lawcache.Store, pkg sketchfront.PackageTarget, gens []gateGen, config string) (kept gatePackage, held bool, err error) {
	kept = gatePackage{Path: pkg.Path, Dir: pkg.Dir}
	held = true
	// fmt reads only the files: it runs whether the package checks or not.
	files, err := gateSources(pkg.Dir)
	if err != nil {
		return kept, false, err
	}
	for _, file := range files {
		kept.Files++
		held = r.format(store, file) && held
	}
	l, err := loadPackageThrough(nil, pkg.Dir, opts.target)
	if err != nil {
		r.fail("check", pkg.Path, err.Error())
		return kept, false, nil
	}
	kept.Revision = l.result.Revision
	id := producer.Current()
	if err := l.result.Qualify(id); err != nil {
		return kept, false, err
	}
	r.Computed["check"]++
	if !l.result.Checked {
		if err := printProjectionJSON(l.result.CheckEnvelope()); err != nil {
			return kept, false, err
		}
		r.fail("check", pkg.Path, "check failed")
		return kept, false, nil
	}
	// lint reads the load before check's evaluation is set on it, as
	// `ergo lint` reads a fresh load.
	lintText, ok, err := r.lint(store, l, pkg.Path, config)
	if err != nil {
		return kept, false, err
	}
	kept.Lint, held = lintText, ok && held
	one := opts
	one.file = pkg.Dir
	checkOpts := one
	checkOpts.command = "check"
	out, exit, err := evaluatedEnvelope(checkOpts, l, id, nil)
	if err != nil {
		return kept, false, err
	}
	os.Stdout.Write(out)
	kept.Check = out
	if exit != nil {
		r.fail("check", pkg.Path, exit.Error())
		held = false
	}
	for _, g := range gens {
		kept.Gens++
		held = r.gen(store, l, pkg, g, gens) && held
	}
	r.Computed["test"]++
	stored := false
	err = testLoadedPackage(one, l, func(result []byte) { kept.Test, stored = result, true })
	if err != nil {
		stop, _ := packageOutcome(pkg.Path, err)
		if stop != nil {
			return kept, false, stop
		}
		r.fail("test", pkg.Path, "tests failed")
		return kept, false, nil
	}
	return kept, held && stored, nil
}

// format is fmt --check of one file.
func (r *gateRun) format(store *lawcache.Store, file string) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		r.fail("fmt", file, err.Error())
		return false
	}
	key := lawcache.Key("gate fmt/1", sketchfront.FormatterIdentity, string(data))
	if _, ok := store.Get(key); ok {
		r.Replayed["fmt"]++
		return true
	}
	r.Computed["fmt"]++
	result, err := sketchfront.Format(string(data), maxFormatOutputBytes)
	if err != nil {
		r.fail("fmt", file, err.Error())
		return false
	}
	if result.Changed {
		r.fail("fmt", file, "not formatted; run ergo fmt")
		return false
	}
	store.Put(key, gateHeld)
	return true
}

// lint is `ergo lint` of the loaded package with the module's
// configuration: its report text, and whether the policy passed.
func (r *gateRun) lint(store *lawcache.Store, l loaded, path, config string) ([]byte, bool, error) {
	key := lawcache.Key("gate lint/1", l.result.Revision, config)
	if text, ok := store.Get(key); ok {
		r.Replayed["lint"]++
		out := []byte(strings.TrimPrefix(text, gateHeld))
		os.Stdout.Write(out)
		return out, true, nil
	}
	r.Computed["lint"]++
	result, err := lintLoaded(l, nil, packSelection{})
	if err != nil {
		r.fail("lint", path, err.Error())
		return nil, false, nil
	}
	findings := l.result.ReportWithLint(l.snapshot, false, result.Advice())
	var b bytes.Buffer
	if err := findings.WriteText(&b, l.source.String()); err != nil {
		return nil, false, err
	}
	os.Stdout.Write(b.Bytes())
	if !findings.PolicyPassed {
		r.fail("lint", path, "lint failed policy")
		return b.Bytes(), false, nil
	}
	store.Put(key, gateHeld+b.String())
	return b.Bytes(), true, nil
}

// gateGen is a package ergo gen wrote: its kind (rpc, cli or mcp), the
// operation type it was generated from, and its folder, which sits in the
// contract package's folder.
type gateGen struct {
	kind, op, dir string
}

// gen is gen --check of the generated package g of the loaded contract
// package; all are the generated packages beside it.
func (r *gateRun) gen(store *lawcache.Store, l loaded, contract sketchfront.PackageTarget, g gateGen, all []gateGen) bool {
	path, err := importPath(contract.Dir)
	if err != nil {
		r.fail("gen", g.dir, err.Error())
		return false
	}
	parts := []string{"gate gen/1", g.kind, g.op, path, l.result.Revision}
	for _, sibling := range all {
		files, err := gateSources(sibling.dir)
		if err != nil {
			r.fail("gen", g.dir, err.Error())
			return false
		}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				r.fail("gen", g.dir, err.Error())
				return false
			}
			parts = append(parts, file, string(data))
		}
	}
	key := lawcache.Key(parts...)
	if _, ok := store.Get(key); ok {
		r.Replayed["gen"]++
		return true
	}
	r.Computed["gen"]++
	out, err := genFiles(g.kind, l.analysis.Root().Package, contract.Dir, path, g.op)
	if err != nil {
		r.fail("gen", g.dir, err.Error())
		return false
	}
	if stale := staleFiles(contract.Dir, out); len(stale) > 0 {
		r.fail("gen", g.dir, fmt.Sprintf("%s is stale; run ergo gen %s %s.%s", strings.Join(stale, ", "), g.kind, contract.Dir, g.op))
		return false
	}
	store.Put(key, gateHeld)
	return true
}

// gateGenerated finds the packages ergo gen wrote among packages, by the
// header each of their files starts with, and files them under their
// contract package's folder.
func gateGenerated(packages []sketchfront.PackageTarget) map[string][]gateGen {
	out := map[string][]gateGen{}
	for _, pkg := range packages {
		files, err := gateSources(pkg.Dir)
		if err != nil || len(files) == 0 {
			continue
		}
		data, err := os.ReadFile(files[0])
		if err != nil {
			continue
		}
		if g, ok := generatedHeader(string(data)); ok {
			g.dir = pkg.Dir
			out[filepath.Dir(pkg.Dir)] = append(out[filepath.Dir(pkg.Dir)], g)
		}
	}
	return out
}

// generatedHeader reads the header ergo gen starts a file with:
// "// Code generated by ergo gen KIND; DO NOT EDIT." then "// Contract
// PATH.OPTYPE, digest sha256:…".
func generatedHeader(text string) (gateGen, bool) {
	first, rest, _ := strings.Cut(text, "\n")
	kind, ok := strings.CutPrefix(first, "// Code generated by ergo gen ")
	if !ok {
		return gateGen{}, false
	}
	kind, ok = strings.CutSuffix(kind, "; DO NOT EDIT.")
	if !ok || (kind != "api" && kind != "rpc" && kind != "cli" && kind != "mcp") {
		return gateGen{}, false
	}
	second, _, _ := strings.Cut(rest, "\n")
	contract, ok := strings.CutPrefix(second, "// Contract ")
	if !ok {
		return gateGen{}, false
	}
	contract, _, _ = strings.Cut(contract, ",")
	i := strings.LastIndex(contract, ".")
	if i < 0 || i == len(contract)-1 {
		return gateGen{}, false
	}
	return gateGen{kind: kind, op: contract[i+1:]}, true
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
	return files, nil
}

// gateLintConfig is the module's lint configuration document as a key
// part.
func gateLintConfig(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "ergo.lint.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return "no lint config"
	}
	return "lint config " + string(data)
}

// gateTreeKey is the module's key (see the top of this file) but the law
// ledger, which the caller adds as the run leaves it; "" when the module
// has no key, which keeps nothing.
func gateTreeKey(root string, opts fileOptions, ledger string) string {
	tc := warmToolchain(opts)
	if tc == nil {
		return ""
	}
	goCommand, ok := goStamp(tc.Go)
	if !ok {
		return ""
	}
	parts := []string{"gate module/2", root, warmOptions(opts), goCommand, tc.Root, tc.GOOS + "/" + tc.GOARCH, tc.GOFLAGS,
		strings.Join(tc.Tags, ","), strconv.FormatBool(tc.GoBuild) + " " + strconv.FormatBool(tc.Strip)}
	parts = append(parts, lawcache.Environ(os.Environ(), sketchfront.StageReportVariable)...)
	files, err := gateTreeFiles(root, ledger, map[string]bool{})
	if err != nil {
		return ""
	}
	return lawcache.Key(append(parts, files...)...)
}

// gateTreeFiles are the key parts of the files below root (the folders
// discovery walks; skip, the law ledger, left out), then, for each folder
// root's go.mod replaces a module with, its own parts, transitively: a
// build reads the replacing folder's sources, so an edit there changes the
// key of every module that reaches it (GATE-KEY-REACHES). seen holds the
// folders already composed, so a cycle of replaces ends.
func gateTreeFiles(root, skip string, seen map[string]bool) ([]string, error) {
	seen[root] = true
	var parts []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			// The folders discovery skips: hidden, underscored, testdata
			// and nested modules.
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, sketchfront.ModFile)); err == nil {
				return filepath.SkipDir
			}
			parts = append(parts, "dir "+rel)
			return nil
		}
		if path == skip {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		stage.Add(stage.BytesHashed, int64(len(data)))
		sum := sha256.Sum256(data)
		parts = append(parts, "file "+rel+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		return nil, err
	}
	targets, err := sketchfront.LocalReplaces(root)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if seen[target] {
			continue
		}
		sub, err := gateTreeFiles(target, "", seen)
		if err != nil {
			return nil, err
		}
		parts = append(parts, "replace "+target)
		parts = append(parts, sub...)
	}
	return parts, nil
}
