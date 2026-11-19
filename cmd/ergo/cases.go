package main

import (
	"fmt"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/cases"
	"ergo.local/prototype/internal/proclimit"
)

// casesFlag removes test's --cases DIR from args and returns DIR, or ""
// when args hold no --cases. A --cases with no folder is a usage error.
func casesFlag(args []string) ([]string, string, error) {
	out := make([]string, 0, len(args))
	dir := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--cases" && (i == 0 || !takesValue(args[i-1])) {
			if i+1 == len(args) || args[i+1] == "" {
				return nil, "", fmt.Errorf("ergo test: --cases takes a folder of cases")
			}
			if dir != "" {
				return nil, "", fmt.Errorf("ergo test: --cases is given twice")
			}
			dir = args[i+1]
			i++
			continue
		}
		out = append(out, a)
	}
	return out, dir, nil
}

// testCases is `ergo test PKG --cases DIR` (docs/tooling.md, Cases): it
// reads every case folder of DIR, builds the main package PKG into a
// private temporary folder, and runs each case against the built program
// (internal/cases), as the examples runner runs an example's cases. Its
// exit status is 0 when every case passes, 1 when one fails, when DIR's
// layout has a problem or when DIR holds no case, and 2 for an invalid
// invocation. Each case runs under ERGO_PROGRAM_MEMORY's bound (2 GiB by
// default, as `ergo test`'s programs), in its own process group.
func testCases(args []string, dir string) error {
	opts, err := parseFileOptions("build", args)
	if err != nil {
		return invalidInvocation(fmt.Errorf("ergo test --cases: %v", err))
	}
	switch {
	case recursivePattern(opts.file):
		return invalidInvocation(fmt.Errorf("ergo test --cases: takes one main package, not a pattern"))
	case opts.output != "" || opts.emitted || opts.goText != "":
		return invalidInvocation(fmt.Errorf("ergo test --cases: builds into a private folder; -o, --emitted and -go-text do not apply"))
	}
	memory, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		return invalidInvocation(err)
	}
	loaded, problems := cases.Load(dir, false)
	if cases.WriteProblems(os.Stdout, problems) != 0 {
		return exitStatusError{code: 1, handled: true}
	}
	l := cases.NewLive()
	defer l.HandleSignals(os.Stderr, "ergo test")()
	scratch, err := l.MakeDir("", "ergo-cases-")
	if err != nil {
		return err
	}
	defer l.RemoveDir(scratch)
	output, err := buildSource(opts, func(name, _ string) string { return filepath.Join(scratch, name) })
	if err != nil {
		return err
	}
	if l.Run(os.Stdout, output.Path(), loaded, memory) != 0 {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}
