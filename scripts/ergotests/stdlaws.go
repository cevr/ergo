package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/stdlib"
)

// stdRedFile lists, from the module root, what does not pass on its own
// source yet, one line each, "KEY<TAB>CLASS: why<TAB>REGISTER-ID": a std
// law with no Go runner that no tier runs ("pkg Law"), an obligation that
// fails there ("pkg Law" for a law, "pkg Name" for a test), or a package
// whose evaluation stops ("pkg *"). CLASS is how it fails (redClass). It
// only shrinks: a listed key that passes or names nothing, and one that
// now fails in another class, fails the run until its line goes or is
// rewritten, so no line hides a second bug.
const stdRedFile = "scripts/ergotests/testdata/std-red.txt"

// stdRun is what -std saw: each law's verdicts (by "pkg Law"; one per
// provider or state it runs for) and the class of each key that failed.
type stdRun struct {
	verdicts map[string][]eval.Status
	failed   map[string]string
}

// redLine is a line of stdRedFile.
type redLine struct{ class, why, id string }

// runStd is -std: it runs the laws and tests of every std package
// (stdlib.Packages: the prelude folder is none, and no folder that is not
// a package) on its own source, in-package, prints each verdict, and
// holds that each obligation passed and that every law the ledger gives
// no Go runner ran there, unless stdRedFile lists it in the class it
// fails in (NATIVE-LAWS: a law no tier ran fails by name).
func runStd(w io.Writer, root string) int {
	run := stdRun{verdicts: map[string][]eval.Status{}, failed: map[string]string{}}
	for _, pkg := range stdlib.Packages() {
		dir := filepath.Join(root, filepath.FromSlash(stdRoot), filepath.FromSlash(pkg))
		rep, err := evaluate(dir)
		if err != nil {
			fmt.Fprintf(w, "red %s: %v\n", pkg, err)
			run.failed[pkg+" *"] = errorClass(err.Error())
			continue
		}
		for _, r := range rep.Results {
			key := pkg + " " + r.Name
			if law, ok := lawOf(r.Name); ok {
				key = pkg + " " + law
				run.verdicts[key] = append(run.verdicts[key], r.Status)
			}
			if holds(r, false) {
				if r.Status == eval.Passed {
					fmt.Fprintf(w, "%s %s: %s\n", r.Status, pkg, r.Name)
				} else {
					fmt.Fprintf(w, "%s %s: %s: %s\n", r.Status, pkg, r.Name, r.Reason)
				}
				continue
			}
			fmt.Fprintf(w, "red %s: %s (%s:%d) is %s: %s\n", pkg, r.Name, r.Site.File, r.Site.Line, r.Status, r.Reason)
			run.failed[key] = string(r.Status)
		}
	}
	rows, err := lawledger.Read()
	if err != nil {
		fmt.Fprintln(w, "FAIL the std law ledger:", err)
		return 1
	}
	var bare []string
	for _, r := range rows {
		if r.Runner == lawledger.NoRunner {
			bare = append(bare, r.Key())
		}
	}
	listed, err := readStdRed(filepath.Join(root, filepath.FromSlash(stdRedFile)))
	if err != nil {
		fmt.Fprintln(w, "FAIL", err)
		return 1
	}
	problems := redProblems(bare, run, listed)
	for _, p := range problems {
		fmt.Fprintln(w, "FAIL", p)
	}
	fmt.Fprintf(w, "std laws with no Go runner: %d; lines in %s: %d\n", len(bare), stdRedFile, len(listed))
	if len(problems) > 0 {
		return 1
	}
	return 0
}

// errorClass is the class of a package whose evaluation stopped: an
// evaluator's internal error, which is never an accepted verdict, or
// another error (the package does not check, a host refusal).
func errorClass(text string) string {
	if strings.Contains(text, "internal error") {
		return "internal error"
	}
	return "error"
}

// lawOf is the law an obligation's name runs: "law L", "law L for P" or
// "law L under S" run L.
func lawOf(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "law ")
	if !ok {
		return "", false
	}
	law, _, _ := strings.Cut(rest, " ")
	return law, law != ""
}

// redProblems are the ways the run disagrees with the listed keys. A key
// needs a line, in the class it fails in, when it failed, or when it is a
// law with no Go runner (bare) that did not run: it ran when it has a
// verdict and every one passed, so a skip, an incomplete run or no
// verdict at all runs nothing (class "not run"). A key that needs a line
// and has none is red; a line whose key needs none goes; a line of
// another class than the run's is red.
func redProblems(bare []string, run stdRun, listed map[string]redLine) []string {
	needs := map[string]string{}
	for key, class := range run.failed {
		needs[key] = class
	}
	for _, key := range bare {
		statuses := run.verdicts[key]
		ran := len(statuses) > 0 && !slices.ContainsFunc(statuses, func(s eval.Status) bool { return s != eval.Passed })
		if _, failed := needs[key]; !ran && !failed {
			needs[key] = "not run"
		}
	}
	var out []string
	for _, key := range sortedKeys(needs) {
		line, ok := listed[key]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s is %s on its own source: make it pass, or list it in %s as \"%s\\t%s: why\\tREGISTER-ID\"", key, needs[key], stdRedFile, key, needs[key]))
		case line.class != needs[key]:
			out = append(out, fmt.Sprintf("%s is %s on its own source, and %s lists it as %s: a new failure, not the listed one; fix it, or rewrite the line with its register id", key, needs[key], stdRedFile, line.class))
		}
	}
	for _, key := range sortedKeys(listed) {
		if _, ok := needs[key]; !ok {
			out = append(out, fmt.Sprintf("%s lists %s (%s), which passes on its own source or names nothing: delete its line (the list only shrinks)", stdRedFile, key, listed[key].id))
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readStdRed reads stdRedFile; # starts a comment line.
func readStdRed(path string) (map[string]redLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]redLine{}
	scan := bufio.NewScanner(f)
	for n := 1; scan.Scan(); n++ {
		text := scan.Text()
		if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cells := strings.Split(text, "\t")
		var line redLine
		var ok bool
		if len(cells) == 3 {
			line.id = strings.TrimSpace(cells[2])
			line.class, line.why, ok = strings.Cut(cells[1], ": ")
		}
		if !ok || len(strings.Fields(cells[0])) < 2 || line.class == "" || strings.TrimSpace(line.why) == "" || line.id == "" {
			return nil, fmt.Errorf("%s:%d: want \"pkg Name<TAB>CLASS: why<TAB>REGISTER-ID\", got %q", stdRedFile, n, text)
		}
		if _, dup := out[cells[0]]; dup {
			return nil, fmt.Errorf("%s:%d: %s is listed twice", stdRedFile, n, cells[0])
		}
		out[cells[0]] = line
	}
	return out, scan.Err()
}
