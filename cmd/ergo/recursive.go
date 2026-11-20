package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
)

func recursivePattern(path string) bool { return filepath.Base(filepath.Clean(path)) == "..." }

// moduleTestOptions belong to the whole selection, not to any child runner.
// A package gets a law only when its checked run list contains it, and a
// unique canonical receipt path under receiptDir, never the module's path.
type moduleTestOptions struct {
	law, receiptDir string
}

// moduleVerdict is the warm answer of `ergo test ./...` (compile-speed
// CS8): each selected package's import path and its stored passing
// result, in the order the run printed them; a nil result is a package
// with nothing to test.
type moduleVerdict struct {
	Path   string `json:"path"`
	Result []byte `json:"result,omitempty"`
}

// recursiveTestCommand tests every package below a folder. A request whose
// every package passed, with no --law or --receipt, is a warm answer: the
// next identical request whose inputs (every folder discovery listed and
// every file the packages' checks read) and the law ledger are unchanged
// replays each package's stored result, as a run in which every package
// replays its test result cache prints it, with no discovery, check,
// build or process.
func recursiveTestCommand(opts fileOptions) error {
	module := moduleTestOptions{law: opts.law, receiptDir: opts.receipt}
	opts.law, opts.receipt = "", ""
	var w *warm
	ledger := ledgerPath(opts.file)
	if testCacheable(opts) && module.law == "" && module.receiptDir == "" {
		w = startWarm(opts, warmToolchain(opts), ledgerPart(ledger))
	}
	if w != nil && w.hit {
		return replayModuleVerdict(opts, w.answer.Test)
	}
	// A request with no warm answer to keep (--uncached, --law, --receipt)
	// checks the module once and links one test binary (TEST-one). A
	// cacheable one tests each package through the warm recorder, so the
	// warm answer it keeps names every file the packages' checks read:
	// the shared load reads around it (CheckPackages takes no FS).
	if w == nil {
		if shared, err := sharedTestCommand(opts, module); shared {
			return err
		}
	}
	kept, complete := []moduleVerdict{}, true
	matched := module.law == ""
	err := recursiveCommand(opts, w.fsys(), func(one fileOptions) error {
		l, err := loadPackageThrough(w.fsys(), one.file, one.target)
		if err != nil {
			return err
		}
		if !l.result.Checked {
			return refuseUnchecked(l, one)
		}
		run, err := module.select_(&one, l)
		if err != nil || !run {
			return err
		}
		matched = true
		kept = append(kept, moduleVerdict{Path: l.analysis.Root().Path})
		n, stored := len(kept)-1, false
		err = testLoadedPackage(one, l, func(result []byte) {
			kept[n].Result, stored = result, true
		})
		complete = complete && stored
		return err
	})
	if err != nil {
		return err
	}
	if w != nil && complete {
		if data, err := json.Marshal(kept); err == nil {
			w.recordAs(sketchfront.WarmAnswer{Test: data}, ledgerPart(ledger))
		}
	}
	if !matched {
		return fmt.Errorf("no law %s in selected packages (--law)", module.law)
	}
	return nil
}

// select_ reports whether the checked package l runs under the module's
// options, and sets its law and receipt in one.
func (module moduleTestOptions) select_(one *fileOptions, l loaded) (bool, error) {
	checked := l.analysis.Root()
	pkg := checked.Package
	if module.law != "" {
		found := false
		for _, run := range pkg.LawRuns {
			if run.Law.Title(pkg) == module.law {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
		one.law = module.law
	}
	if module.receiptDir != "" {
		one.receipt = filepath.Join(module.receiptDir, filepath.FromSlash(checked.Path)+".json")
		if err := os.MkdirAll(filepath.Dir(one.receipt), 0o755); err != nil {
			return false, err
		}
	}
	return true, nil
}

// loadShared loads the packages below opts.file with one load of each
// module (sketchfront.CheckPackages; at a workspace root, one per member,
// in selection order): each package as loadFolder loads it alone, the
// same source, snapshot, analysis and result. ok is false when
// they cannot share a load or a package does not check; the caller then
// loads each package alone, which reports it as before.
func loadShared(opts fileOptions, platform gobind.Platform) ([]sketchfront.PackageTarget, []loaded, bool) {
	return loadSharedFS(opts, platform, nil)
}

// loadSharedFS is loadShared reading discovery and the load through fsys
// (a warm request's Recorder); nil reads the host.
func loadSharedFS(opts fileOptions, platform gobind.Platform, fsys sketchfront.FS) ([]sketchfront.PackageTarget, []loaded, bool) {
	packages, err := sketchfront.DiscoverPackagesThrough(fsys, filepath.Dir(filepath.Clean(opts.file)))
	if err != nil {
		return nil, nil, false
	}
	sources := make([]receipt.Location, len(packages))
	dirs := make([]string, len(packages))
	for i, pkg := range packages {
		if sources[i], err = receipt.LocateSource(pkg.Dir); err != nil {
			return nil, nil, false
		}
		dirs[i] = sources[i].Admitted()
	}
	var analyses []sketchfront.Analysis
	for start := 0; start < len(dirs); {
		end := start + 1
		for end < len(dirs) && packages[end].Root == packages[start].Root {
			end++
		}
		var as []sketchfront.Analysis
		var err error
		if fsys == nil {
			as, err = sketchfront.CheckPackages(dirs[start:end], platform)
		} else {
			as, err = sketchfront.CheckPackagesFS(fsys, dirs[start:end], platform)
		}
		if err != nil {
			return nil, nil, false
		}
		analyses = append(analyses, as...)
		start = end
	}
	loads := make([]loaded, len(analyses))
	for i, a := range analyses {
		uri, err := report.FileURI(dirs[i])
		if err != nil {
			return nil, nil, false
		}
		loads[i] = loaded{
			source:   sources[i],
			name:     filepath.Base(dirs[i]),
			snapshot: report.SourceSnapshot{URI: uri, Origin: "disk", Files: a.Files()},
			analysis: a,
			result:   a.Result(a.Text(), opts.target),
		}
		if !loads[i].result.Checked {
			return nil, nil, false
		}
	}
	return packages, loads, true
}

// sharedCheckCommand runs `ergo check DIR/...` with one load of the module
// (loadShared): each package's envelope and evaluation are what `ergo
// check` of that package alone prints, in the order and with the exit
// status rules of recursiveCommand. shared is false when the packages
// cannot share a load; then each package is checked alone.
func sharedCheckCommand(opts fileOptions) (shared bool, err error) {
	tree := startTree(opts)
	if outs, ok := tree.lookup(); ok {
		for _, out := range outs {
			if _, err := os.Stdout.Write(out); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	packages, loads, ok := loadSharedFS(opts, opts.platform, tree.fsys())
	if !ok {
		return false, nil
	}
	failed := false
	var sources map[string]bool
	if tree != nil {
		sources = loadSources(loads)
	}
	for i, l := range loads {
		one := opts
		one.file = packages[i].Dir
		stop, fail := packageOutcome(packages[i].Path, checkShared(one, l, tree, sources))
		if stop != nil {
			return true, stop
		}
		failed = failed || fail
	}
	if opts.affected.why && tree != nil && tree.cutFallbacks > 0 {
		// The gap is counted, not silent (GB-WS0C, q14).
		fmt.Fprintf(os.Stderr, "check: %d cached law verdicts kept no reads (a test binary's): their packages are kept by every import's implementation\n", tree.cutFallbacks)
	}
	if failed {
		return true, exitStatusError{code: 1, handled: true}
	}
	tree.record()
	return true, nil
}

// checkShared is checkLoaded of one package of a shared load, keeping
// its answer in tree. Its evaluation is taken from the cutoff's store
// when nothing it observes changed (checkcut.go); sources are the load's
// source files and package folders.
func checkShared(opts fileOptions, l loaded, tree *checkTree, sources map[string]bool) error {
	if out, ok := tree.replay(l.analysis); ok {
		if _, err := os.Stdout.Write(out); err != nil {
			return err
		}
		tree.add(l.analysis, out, nil)
		return nil
	}
	if treeRerun != nil {
		treeRerun(l.analysis.Root().Path)
	}
	puts := tree.storePuts()
	out, exit, err := checkEnvelope(opts, l, tree.fsys())
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(out); err != nil {
		return err
	}
	tree.add(l.analysis, out, exit)
	tree.keepCut(l.analysis, sources, out, exit, puts, l.result.Evaluation())
	return exit
}

// sharedTestCommand runs `ergo test DIR/...` with one load of the module
// and one test binary (TEST-one; sketchfront.CheckPackages, BuildTests):
// the binary runs once per selected package, so each package prints what
// its own test binary prints, under the same package line, with the same
// exit status rules as recursiveCommand. It does not run (shared false)
// when the packages cannot share a load or a build, when a package does not
// check or a lowering refuses it, or for -work, --explore and a schedule
// search; then each package is tested alone, which reports it as before.
func sharedTestCommand(opts fileOptions, module moduleTestOptions) (shared bool, err error) {
	if opts.work || opts.explore || opts.search.mode != "" {
		return false, nil
	}
	platform, err := platformOf("", false, os.Getenv("GOOS"), os.Getenv("GOARCH"))
	if err != nil {
		return false, nil
	}
	packages, loads, ok := loadShared(opts, platform)
	if !ok {
		return false, nil
	}
	type selected struct {
		one   fileOptions
		l     loaded
		index int // in the test module; -1 when the package does not run
	}
	runs := make([]selected, len(packages))
	var built []sketchfront.Analysis
	for i, l := range loads {
		a := l.analysis
		one := opts
		one.file = packages[i].Dir
		run, err := module.select_(&one, l)
		if err != nil {
			return true, err
		}
		runs[i] = selected{one: one, l: l, index: -1}
		if run {
			runs[i].index = len(built)
			built = append(built, a)
		}
	}
	if len(built) == 0 {
		return true, fmt.Errorf("no law %s in selected packages (--law)", module.law)
	}
	dir, err := os.MkdirTemp("", "ergo-test-")
	if err != nil {
		return true, err
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, "module.test")
	root, err := build.DefaultRoot()
	if err != nil {
		return true, err
	}
	tc, err := buildAction(opts)
	if err != nil {
		return true, err
	}
	// When the store holds every package's own test binary (an earlier
	// `ergo test ./...` linked them), each package runs its own and
	// nothing is built (TEQ); otherwise the module's one binary is built.
	own := make([]string, len(runs))
	stored := true
	for i, r := range runs {
		if stored && r.index >= 0 {
			own[i], stored = storedTestBinary(r.l, filepath.Join(dir, strconv.Itoa(i)+".test"), root, tc)
		}
	}
	if !stored {
		// A failed build is reported by the packages' own builds, each
		// where it belongs, so its output is kept back but for one line
		// saying the packages are tested alone (sharedFallback).
		var held bytes.Buffer
		if _, err := sketchfront.BuildTests(built, sketchfront.BuildRequest{
			Output:    binary,
			Cache:     build.Cache{Root: root},
			Toolchain: tc,
			IR:        true,
			Stderr:    &held,
		}); err != nil {
			return sharedFallback(os.Stderr, os.Getenv(SharedFallbackVariable) == "fail", err, held.String())
		}
	}
	stage.Add(stage.SharedTestModules, 1)
	failed := false
	for i, r := range runs {
		fmt.Fprintf(os.Stdout, "package %s\n", packages[i].Path)
		if r.index < 0 {
			continue
		}
		var err error
		switch {
		case !stored:
			err = runTestBinary(r.one, r.l, tc, binary, []string{lower.TestPackageFlag, strconv.Itoa(r.index)}, nil)
		case own[i] == "":
			err = emptyTestRun(r.one, r.l.result.Revision, os.Stdout, os.Stderr)
		default:
			err = runTestBinary(r.one, r.l, tc, own[i], nil, nil)
		}
		stop, fail := packageOutcome(packages[i].Path, err)
		if stop != nil {
			return true, stop
		}
		failed = failed || fail
	}
	if failed {
		return true, exitStatusError{code: 1, handled: true}
	}
	return true, nil
}

// storedTestBinary places the package l's own test binary at output from
// the store, built as its `ergo test` builds it (testLoadedPackage) but
// with Toolchain.Stored: no lowering, compile, link or go command runs.
// It reports false when the store lacks any part; a package with nothing
// to test has no binary, and its path is "" (emptyTestRun runs it).
func storedTestBinary(l loaded, output, root string, tc build.Toolchain) (string, bool) {
	if nothingToTest(l.analysis.Root().Package) {
		return "", true
	}
	tc.Stored = true
	_, err := sketchfront.Build(l.analysis, sketchfront.BuildRequest{
		Mode:      build.ModeTest,
		Source:    l.source.Admitted(),
		Output:    output,
		Cache:     build.Cache{Root: root},
		Toolchain: tc,
		IR:        true,
		Stderr:    io.Discard,
	})
	return output, err == nil
}

// SharedFallbackVariable, set to "fail", makes a fallback of `ergo test
// DIR/...` from its module test binary to each package alone an error. The
// tests (internal/testinputs) and the gate set it: there a fallback is a
// bug of the shared build, which per-package builds would hide.
const SharedFallbackVariable = "ERGO_SHARED_FALLBACK"

// sharedFallback reports that the module test binary did not build: once
// on stderr, with the go error's first line, and in the stage report
// (shared_fallbacks). Each package is then tested alone (shared false),
// unless strict (SharedFallbackVariable is "fail"): then the run fails.
func sharedFallback(stderr io.Writer, strict bool, err error, output string) (shared bool, _ error) {
	stage.Add(stage.SharedFallbacks, 1)
	first := err.Error()
	for line := range strings.Lines(output) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			first = line
			break
		}
	}
	if strict {
		return true, fmt.Errorf("the module's test binary did not build (%s=fail): %s", SharedFallbackVariable, first)
	}
	fmt.Fprintf(stderr, "ergo test: the module's test binary did not build, so each package is tested alone: %s\n", first)
	return false, nil
}

// replayModuleVerdict prints a stored `ergo test ./...` verdict: each
// package's line and its replayed result.
func replayModuleVerdict(opts fileOptions, data []byte) error {
	var verdicts []moduleVerdict
	if err := json.Unmarshal(data, &verdicts); err != nil {
		return fmt.Errorf("test result cache: %w", err)
	}
	failed := false
	for _, v := range verdicts {
		fmt.Fprintf(os.Stdout, "package %s\n", v.Path)
		if v.Result == nil {
			if err := emptyTestRun(opts, "", os.Stdout, os.Stderr); err != nil {
				return err
			}
			continue
		}
		// An unexpected skip fails its package and the run, as a live
		// run's does (packageOutcome); the other packages still replay.
		stop, fail := packageOutcome(v.Path, replayTestResult(v.Result, opts.file, os.Stdout, os.Stderr))
		if stop != nil {
			return stop
		}
		failed = failed || fail
	}
	if failed {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}

// loadPackageThrough is load reading through fsys (a warm request's
// Recorder); nil reads the host.
func loadPackageThrough(fsys sketchfront.FS, path, target string) (loaded, error) {
	platform, err := platformOf("", false, os.Getenv("GOOS"), os.Getenv("GOARCH"))
	if err != nil {
		return loaded{}, err
	}
	return loadThrough(fsys, path, target, platform)
}

// recursiveCommand retains each package command's flags and result. A normal
// test/check failure does not hide later packages. A defect or signal stops
// the run: the existing test command's stronger exit status is preserved.
// fsys, when not nil, is the file system discovery reads through (a warm
// request's Recorder).
func recursiveCommand(opts fileOptions, fsys sketchfront.FS, run func(fileOptions) error) error {
	packages, err := sketchfront.DiscoverPackagesThrough(fsys, filepath.Dir(filepath.Clean(opts.file)))
	if err != nil {
		return invalidInvocation(err)
	}
	failed := false
	for _, pkg := range packages {
		one := opts
		one.file = pkg.Dir
		if opts.command == "test" {
			fmt.Fprintf(os.Stdout, "package %s\n", pkg.Path)
		}
		stop, fail := packageOutcome(pkg.Path, run(one))
		if stop != nil {
			return stop
		}
		if fail {
			failed = true
		}
	}
	if failed {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}

// packageOutcome reads one package command's error: stop is the error that
// ends the whole run (a defect, a signal, a usage error), and failed a
// failure that the run reports at its end, printed here.
func packageOutcome(path string, err error) (stop error, failed bool) {
	if err == nil {
		return nil, false
	}
	switch e := err.(type) {
	case exitStatusError:
		if e.code != 1 {
			return err, false
		}
		if !e.handled && e.message != "" {
			fmt.Fprintln(os.Stderr, e.message)
		}
	case usageError:
		return err, false
	default:
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
	}
	return nil, true
}
