// Command ergotests runs the Ergo tests of Ergo packages at check time, as
// `ergo check` evaluates them: in process, with no Go build (ST-0). It is
// the gate step for the compiler's own packages (compiler/...) and the
// command of the mutant runner's Ergo suites (scripts/mutants, a killer
// DIR:Test where DIR holds .ergo files and no Go test).
//
//	go run ./scripts/ergotests [-run REGEXP] DIR|DIR/...
//
// A test must pass: a skipped or incomplete test fails, since a skip is not
// a pass. A law fails when it is refuted or hits a defect; a law that is
// skipped or incomplete at check time (the step budget) is listed and does
// not fail, as `ergo check` exits. -run selects the obligations by name
// (a test's name, "law L" for a law); a -run that selects none fails.
//
// Exit codes: 0 every selected obligation holds; 1 one does not, or a
// package does not check; 2 a bad invocation.
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketchfront"
)

func main() {
	run := flag.String("run", "", "run only the obligations whose name matches this regular expression")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: ergotests [-run REGEXP] DIR|DIR/...")
		os.Exit(2)
	}
	var sel *regexp.Regexp
	if *run != "" {
		var err error
		if sel, err = regexp.Compile(*run); err != nil {
			fmt.Fprintln(os.Stderr, "ergotests: -run:", err)
			os.Exit(2)
		}
	}
	os.Exit(runAll(os.Stdout, flag.Args(), sel))
}

// runAll runs the obligations of every package the patterns name and
// returns the exit code.
func runAll(w io.Writer, patterns []string, sel *regexp.Regexp) int {
	var dirs []string
	for _, p := range patterns {
		ds, err := packages(p)
		if err != nil {
			fmt.Fprintln(w, "ergotests:", err)
			return 1
		}
		dirs = append(dirs, ds...)
	}
	code, selected := 0, 0
	for _, dir := range dirs {
		n, ok := runDir(w, dir, sel)
		selected += n
		if !ok {
			code = 1
		}
	}
	if sel != nil && selected == 0 {
		fmt.Fprintf(w, "ergotests: -run %q selects no test or law of %s\n", sel, strings.Join(patterns, " "))
		return 1
	}
	return code
}

// packages are the directories a pattern names: DIR itself, or for DIR/...
// every directory under it that holds a .ergo file, testdata excluded.
func packages(pattern string) ([]string, error) {
	root, all := strings.CutSuffix(filepath.ToSlash(pattern), "/...")
	if !all {
		if !hasErgo(root) {
			return nil, fmt.Errorf("%s holds no .ergo file", pattern)
		}
		return []string{root}, nil
	}
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if hasErgo(p) {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func hasErgo(dir string) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*.ergo"))
	return len(files) > 0
}

// runDir checks dir, evaluates it and reports each selected obligation. It
// returns how many it selected and whether they all hold.
func runDir(w io.Writer, dir string, sel *regexp.Regexp) (int, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(w, "FAIL %s: %v\n", dir, err)
		return 0, false
	}
	a, err := sketchfront.CheckDir(abs)
	if err != nil {
		fmt.Fprintf(w, "FAIL %s: %v\n", dir, err)
		return 0, false
	}
	if len(a.Diagnostics) != 0 {
		fmt.Fprintf(w, "FAIL %s does not check: %v\n", dir, a.Diagnostics)
		return 0, false
	}
	rep, err := a.Evaluate(eval.DefaultBudget())
	if err != nil {
		fmt.Fprintf(w, "FAIL %s: %v\n", dir, err)
		return 0, false
	}
	n, ok := 0, true
	for _, r := range rep.Results {
		if sel != nil && !sel.MatchString(r.Name) {
			continue
		}
		n++
		if holds(r) {
			fmt.Fprintf(w, "%s %s: %s\n", r.Status, dir, r.Name)
			continue
		}
		ok = false
		fmt.Fprintf(w, "FAIL %s: %s (%s:%d) is %s: %s\n", dir, r.Name, r.Site.File, r.Site.Line, r.Status, r.Reason)
	}
	return n, ok
}

// holds says whether an obligation's result passes the run: a test only
// when it passed; a law unless it was refuted or hit a defect.
func holds(r eval.Result) bool {
	if strings.HasPrefix(r.Name, "law ") {
		return !slices.Contains([]eval.Status{eval.Refuted, eval.Defect}, r.Status)
	}
	return r.Status == eval.Passed
}
