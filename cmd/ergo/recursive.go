package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
				return nil
			}
			matched = true
			one.law = module.law
		}
		if module.receiptDir != "" {
			one.receipt = filepath.Join(module.receiptDir, filepath.FromSlash(checked.Path)+".json")
			if err := os.MkdirAll(filepath.Dir(one.receipt), 0o755); err != nil {
				return err
			}
		}
		kept = append(kept, moduleVerdict{Path: checked.Path})
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

// replayModuleVerdict prints a stored `ergo test ./...` verdict: each
// package's line and its replayed result.
func replayModuleVerdict(opts fileOptions, data []byte) error {
	var verdicts []moduleVerdict
	if err := json.Unmarshal(data, &verdicts); err != nil {
		return fmt.Errorf("test result cache: %w", err)
	}
	for _, v := range verdicts {
		fmt.Fprintf(os.Stdout, "package %s\n", v.Path)
		if v.Result == nil {
			if err := emptyTestRun(opts, "", os.Stdout, os.Stderr); err != nil {
				return err
			}
			continue
		}
		if err := replayTestResult(v.Result, os.Stdout, os.Stderr); err != nil {
			return err
		}
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
		if err := run(one); err != nil {
			code := 1
			switch e := err.(type) {
			case exitStatusError:
				code = e.code
				if code != 1 {
					return err
				}
				if !e.handled && e.message != "" {
					fmt.Fprintln(os.Stderr, e.message)
				}
			case usageError:
				return err
			default:
				fmt.Fprintf(os.Stderr, "%s: %v\n", pkg.Path, err)
			}
			failed = true
		}
	}
	if failed {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}
