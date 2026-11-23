package main

import (
	"fmt"
	"path"
	"strings"

	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketchfront"
)

// stdTargetPrefix spells a std package as ergo test's target: `ergo test
// std:rand` builds std rand's own laws and tests, the package checked as
// std is (sketchfront.CheckStd), into a test binary and runs it, as `ergo
// test DIR` runs a user package's (NATIVE-LAWS slice 2). A std folder does
// not check as a folder: its declarations need std's authority (S108).
const stdTargetPrefix = "std:"

// isStdTarget reports whether target spells a std package (stdTargetPrefix).
func isStdTarget(target string) bool { return strings.HasPrefix(target, stdTargetPrefix) }

// stdTestPackage is `ergo test std:PKG`. The run is always native and
// never cached: std's verdicts are the compiler's own, so no evaluated
// answer and no stored result stands in for a run. It writes no ledger:
// std's ledger is compiled in (stdlib.Ledger) and no folder holds one.
func stdTestPackage(opts fileOptions) error {
	if opts.explore || opts.search.mode != "" || opts.emitted || recursivePattern(opts.file) || len(opts.more) > 0 {
		return invalidInvocation(fmt.Errorf("ergo test %s: a std package takes no --explore, --emitted, pattern or second target", opts.file))
	}
	opts.native, opts.uncached = true, true
	l, err := loadStd(opts.file, opts.target)
	if err != nil {
		return err
	}
	if !l.result.Checked {
		return refuseUnchecked(l, opts)
	}
	return testLoadedPackage(opts, l, nil)
}

// loadStd is the loaded std package spelled std:PKG. Its source is named
// /std/PKG, which no file is: diagnostics and //line directives name the
// package's files under it.
func loadStd(spelled, target string) (loaded, error) {
	importPath := strings.TrimPrefix(spelled, stdTargetPrefix)
	analysis, err := sketchfront.CheckStd(importPath)
	if err != nil {
		return loaded{}, fmt.Errorf("%s: %w", spelled, err)
	}
	admitted := path.Join("/std", importPath)
	uri, err := report.FileURI(admitted)
	if err != nil {
		return loaded{}, err
	}
	return loaded{
		source:   receipt.Unfiled(spelled, admitted),
		name:     path.Base(importPath),
		snapshot: report.SourceSnapshot{URI: uri, Origin: "std", Files: analysis.Files()},
		analysis: analysis,
		result:   analysis.Result(analysis.Text(), target),
	}, nil
}
