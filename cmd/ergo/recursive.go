package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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

func recursiveTestCommand(opts fileOptions) error {
	module := moduleTestOptions{law: opts.law, receiptDir: opts.receipt}
	opts.law, opts.receipt = "", ""
	if shared, err := sharedTestCommand(opts, module); shared {
		return err
	}
	matched := module.law == ""
	err := recursiveCommand(opts, func(one fileOptions) error {
		l, err := load(one.file, one.target)
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
		return testLoadedPackage(one, l)
	})
	if err != nil {
		return err
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
	packages, err := sketchfront.DiscoverPackages(filepath.Dir(filepath.Clean(opts.file)))
	if err != nil {
		return false, nil
	}
	platform, err := platformOf("", false, os.Getenv("GOOS"), os.Getenv("GOARCH"))
	if err != nil {
		return false, nil
	}
	sources := make([]receipt.Location, len(packages))
	dirs := make([]string, len(packages))
	for i, pkg := range packages {
		if sources[i], err = receipt.LocateSource(pkg.Dir); err != nil {
			return false, nil
		}
		dirs[i] = sources[i].Admitted()
	}
	analyses, err := sketchfront.CheckPackages(dirs, platform)
	if err != nil {
		return false, nil
	}
	type selected struct {
		one   fileOptions
		l     loaded
		index int // in the test module; -1 when the package does not run
	}
	runs := make([]selected, len(packages))
	var built []sketchfront.Analysis
	for i, a := range analyses {
		uri, err := report.FileURI(dirs[i])
		if err != nil {
			return false, nil
		}
		l := loaded{
			source:   sources[i],
			name:     filepath.Base(dirs[i]),
			snapshot: report.SourceSnapshot{URI: uri, Origin: "disk", Files: a.Files()},
			analysis: a,
			result:   a.Result(a.Text(), opts.target),
		}
		if !l.result.Checked {
			return false, nil
		}
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
	// A failed build is reported by the packages' own builds, each where
	// it belongs, so its output is kept back.
	var held bytes.Buffer
	if _, err := sketchfront.BuildTests(built, sketchfront.BuildRequest{
		Output:    binary,
		Cache:     build.Cache{Root: root},
		Toolchain: toolchain(opts),
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
		err := runTestBinary(r.one, r.l, binary, []string{lower.TestPackageFlag, strconv.Itoa(r.index)})
		if stop, fail := packageOutcome(packages[i].Path, err); stop != nil {
			return true, stop
		} else if fail {
			failed = true
		}
	}
	if failed {
		return true, exitStatusError{code: 1, handled: true}
	}
	return true, nil
}

// recursiveCommand retains each package command's flags and result. A normal
// test/check failure does not hide later packages. A defect or signal stops
// the run: the existing test command's stronger exit status is preserved.
func recursiveCommand(opts fileOptions, run func(fileOptions) error) error {
	packages, err := sketchfront.DiscoverPackages(filepath.Dir(filepath.Clean(opts.file)))
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
		failed = failed || fail
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
