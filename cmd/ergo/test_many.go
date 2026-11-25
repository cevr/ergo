package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// severalTargets checks the targets of `ergo test A B ...` and `ergo check
// A B ...`: each is a source file, a package folder or a DIR/... pattern,
// none is named twice, and no option that belongs to one package (test's
// --law, --skip-law, --run, --receipt, --emitted, --explore) is given with
// them.
func severalTargets(command string, opts fileOptions, targets []string) error {
	switch {
	case opts.law != "" || opts.skipLaw != "":
		return fmt.Errorf("incorrect arguments for test: --law and --skip-law take one target")
	case opts.run != "" && !opts.mutants:
		return fmt.Errorf("incorrect arguments for test: --run takes one target")
	case opts.receipt != "":
		return fmt.Errorf("incorrect arguments for test: --receipt takes one target")
	case opts.emitted:
		return fmt.Errorf("incorrect arguments for test: --emitted takes one target")
	case opts.explore || opts.search.mode != "":
		return fmt.Errorf("incorrect arguments for test: --explore takes one target")
	}
	seen := map[string]bool{}
	for _, t := range targets {
		key := filepath.Clean(t)
		if seen[key] {
			return fmt.Errorf("incorrect arguments for %s: %s is named twice", command, t)
		}
		seen[key] = true
		if recursivePattern(t) || isPackageFolder(t) {
			continue
		}
		if err := requireSourceExtension(t); err != nil {
			return err
		}
	}
	return nil
}

// manyTestCommand is `ergo test A B ...`: each target in the order given,
// a file or folder under its own "package TARGET" line, a DIR/... pattern
// as `ergo test DIR/...` prints it. A failing target does not stop the
// later ones; a defect, a signal or a usage error does. The run exits 1
// when a target failed.
func manyTestCommand(opts fileOptions) error {
	targets := append([]string{opts.file}, opts.more...)
	failed := false
	for _, target := range targets {
		one := opts
		one.file, one.more = target, nil
		var err error
		if recursivePattern(target) {
			err = recursiveTestCommand(one)
		} else {
			fmt.Fprintf(os.Stdout, "package %s\n", target)
			err = testPackage(one)
		}
		stop, fail := packageOutcome(target, err)
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
