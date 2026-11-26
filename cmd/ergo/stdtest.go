package main

import (
	"fmt"
	"path"
	"strings"

	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/testplan"
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

// stdSkipsExit judges native std skips against the shipped list, not any
// expected-skips file from the checkout.
func stdSkipsExit(stdout []byte, root testplan.NativeRoot) error {
	listed, err := stdlib.TestReds()
	if err != nil {
		return err
	}
	unexpected := unlistedStdSkips(stdout, root, listed)
	if len(unexpected) == 0 {
		return nil
	}
	return exitStatusError{code: 1, message: fmt.Sprintf("FAIL: %d unexpected skips in std:%s; see %s\n    %s",
		len(unexpected), root.Path, stdlib.TestRedPath, strings.Join(unexpected, "\n    "))}
}

func unlistedStdSkips(stdout []byte, root testplan.NativeRoot, listed map[string]stdlib.TestRed) []string {
	passed := passedStdLaws(stdout)
	var unexpected []string
	for line := range strings.SplitSeq(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ": --skip-law") || isCoveredHostRowSkip(line, passed) {
			continue
		}
		name, missing := stdlib.NativeMissingLaw(line)
		if missing && (root.Required || strings.HasPrefix(line, "skip ")) {
			red := listed[root.Path+" "+name]
			if red.Class == stdlib.NativeSkipClass && red.Why == line {
				continue
			}
		} else if !strings.HasPrefix(line, "skip ") {
			continue
		}
		unexpected = append(unexpected, line)
	}
	return unexpected
}

func passedStdLaws(stdout []byte) map[string]bool {
	out := map[string]bool{}
	for line := range strings.SplitSeq(string(stdout), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ok law ")
		if !ok {
			continue
		}
		law, _, _ := strings.Cut(rest, " ")
		if law != "" {
			out[law] = true
		}
	}
	return out
}

func isCoveredHostRowSkip(line string, passed map[string]bool) bool {
	rest, ok := strings.CutPrefix(line, "skip law ")
	if !ok {
		return false
	}
	subject, reason, ok := strings.Cut(rest, ": ")
	if !ok {
		return false
	}
	law, _, hasFor := strings.Cut(subject, " for ")
	if !hasFor || !passed[law] {
		return false
	}
	return strings.HasPrefix(reason, "building ") &&
		strings.HasSuffix(reason, " reads the host and requires Ambient, which a law case does not hold (HOST-ROW)")
}

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
	analysis, plan, err := sketchfront.CheckStd(importPath)
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
		stdTest:  plan,
	}, nil
}
