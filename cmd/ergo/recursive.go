package main

import (
	"fmt"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/sketchfront"
)

func recursivePattern(path string) bool { return filepath.Base(filepath.Clean(path)) == "..." }

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
