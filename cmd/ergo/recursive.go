package main

import (
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

func recursiveTestCommand(opts fileOptions) error {
	module := moduleTestOptions{law: opts.law, receiptDir: opts.receipt}
	opts.law, opts.receipt = "", ""
	matched := module.law == ""
	err := recursiveCommand(opts, func(one fileOptions) error {
		l, err := load(one.file, one.target)
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
