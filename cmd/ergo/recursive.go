package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
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

// loadShared loads the packages below opts.file with one load of their
// module (sketchfront.CheckPackages): each package as loadFolder loads it
// alone, the same source, snapshot, analysis and result. ok is false when
// they cannot share a load or a package does not check; the caller then
// loads each package alone, which reports it as before.
func loadShared(opts fileOptions, platform gobind.Platform) ([]sketchfront.PackageTarget, []loaded, bool) {
	packages, err := sketchfront.DiscoverPackages(filepath.Dir(filepath.Clean(opts.file)))
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
	analyses, err := sketchfront.CheckPackages(dirs, platform)
	if err != nil {
		return nil, nil, false
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
	packages, loads, ok := loadShared(opts, opts.platform)
	if !ok {
		return false, nil
	}
	failed := false
	for i, l := range loads {
		one := opts
		one.file = packages[i].Dir
		stop, fail := packageOutcome(packages[i].Path, checkLoaded(one, l, nil))
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
	// A failed build is reported by the packages' own builds, each where
	// it belongs, so its output is kept back.
	var held bytes.Buffer
	if _, err := sketchfront.BuildTests(built, sketchfront.BuildRequest{
		Output:    binary,
		Cache:     build.Cache{Root: root},
		Toolchain: tc,
		Stderr:    &held,
	}); err != nil {
		return false, nil
	}
	failed := false
	for i, r := range runs {
		fmt.Fprintf(os.Stdout, "package %s\n", packages[i].Path)
		if r.index < 0 {
			continue
		}
		err := runTestBinary(r.one, r.l, binary, []string{lower.TestPackageFlag, strconv.Itoa(r.index)}, nil)
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
