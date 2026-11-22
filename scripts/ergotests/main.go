// Command ergotests runs the Ergo tests of Ergo packages at check time, as
// `ergo check` evaluates them: in process, with no Go build (ST-0). It is
// the gate step for the compiler's own packages (compiler/...) and the
// command of the mutant runner's Ergo suites (scripts/mutants, a killer
// DIR:Test where DIR holds .ergo files and no Go test).
//
//	go run ./scripts/ergotests [-run REGEXP] DIR|DIR/...
//	go run ./scripts/ergotests -std
//
// -std runs every std package on its own source and holds that each std
// law no Go test runs passed there (stdlaws.go).
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
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketchfront"
)

func main() {
	run := flag.String("run", "", "run only the obligations whose name matches this regular expression")
	killer := flag.String("killer", "", "run one test, or law:L for a law, as the mutant runner names a killer; it must pass")
	ergo := flag.String("ergo", "", "the ergo binary that decides a -killer law check time leaves undecided (ergo test --law)")
	std := flag.Bool("std", false, "run every std package's laws and tests on its own source and hold that each law with no Go runner passed (the gate's step \"std laws in place\")")
	flag.Parse()
	if *std {
		if flag.NArg() != 0 || *run != "" || *killer != "" {
			fmt.Fprintln(os.Stderr, "usage: ergotests -std (from the module root; no other argument)")
			os.Exit(2)
		}
		root, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ergotests:", err)
			os.Exit(2)
		}
		os.Exit(runStd(os.Stdout, root))
	}
	if flag.NArg() == 0 || *run != "" && *killer != "" {
		fmt.Fprintln(os.Stderr, "usage: ergotests [-run REGEXP | -killer NAME] DIR|DIR/...")
		os.Exit(2)
	}
	if *killer != "" {
		os.Exit(runKiller(os.Stdout, flag.Args(), *killer, *ergo))
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
	if sel == nil {
		return runPicked(w, patterns, nil, false, "")
	}
	return runPicked(w, patterns, sel.MatchString, false, fmt.Sprintf("-run %q", sel))
}

// runKiller runs the obligations a mutant runner's killer name picks: the
// test NAME, or for law:L every run of the law L ("law L", and "law L for
// P" for each provider it is checked for). Each must pass: a law skipped
// or incomplete at check time kills nothing, so it is no killer
// (LAW-KILLER).
//
// A law check time leaves undecided (skipped at a host call, or out of
// steps) is decided by `ergo test . --law L` in its package's directory
// when ergo names the binary: the build that decides laws, so a host
// module's law (hosts/mygo/desktop's FitIsBroken) kills from the
// repository root (LAW-KILLER). Its emission cache is a directory of the
// run's own unless ERGO_CACHE is set. A std package's law runs in-package
// (evaluate) and is decided at check time or not at all (STD-LAW-RUN).
func runKiller(w io.Writer, patterns []string, name, ergo string) int {
	law, isLaw := strings.CutPrefix(name, "law:")
	if !isLaw {
		return runPicked(w, patterns, func(r string) bool { return r == name && !strings.HasPrefix(r, "law ") }, true, "-killer "+name)
	}
	match := func(r string) bool { return r == "law "+law || strings.HasPrefix(r, "law "+law+" for ") }
	if ergo == "" {
		return runPicked(w, patterns, match, true, "-killer "+name)
	}
	var undecided []string
	code := runPickedThen(w, patterns, match, func(dir string, r eval.Result) bool {
		// A std law ergo test cannot reach (a std folder does not check
		// as a folder): undecided at check time, it is no killer.
		if (r.Status == eval.Skipped || r.Status == eval.Incomplete || r.Status == eval.Native) && !isStd(dir) {
			if !slices.Contains(undecided, dir) {
				undecided = append(undecided, dir)
			}
			return true
		}
		return r.Status == eval.Passed
	}, "-killer "+name)
	for _, dir := range undecided {
		if !ergoTestLaw(w, ergo, dir, law) {
			code = 1
		}
	}
	return code
}

// ergoTestLaw runs `ergo test . --law law` in dir and reports whether it
// passed with no skip.
func ergoTestLaw(w io.Writer, ergo, dir, law string) bool {
	cmd := exec.Command(ergo, "test", ".", "--law", law)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if os.Getenv("ERGO_CACHE") == "" {
		cache, err := os.MkdirTemp("", "ergotests-cache-")
		if err != nil {
			fmt.Fprintf(w, "FAIL %s: law %s: %v\n", dir, law, err)
			return false
		}
		defer os.RemoveAll(cache)
		cmd.Env = append(cmd.Env, "ERGO_CACHE="+cache)
	}
	out, err := cmd.CombinedOutput()
	w.Write(out)
	if err != nil {
		fmt.Fprintf(w, "FAIL %s: ergo test --law %s: %v\n", dir, law, err)
		return false
	}
	if regexp.MustCompile(`(?m)^\s*skip`).Match(out) {
		fmt.Fprintf(w, "FAIL %s: ergo test --law %s skipped it: a skip kills nothing\n", dir, law)
		return false
	}
	return true
}

// runPicked runs the obligations match picks (all, when nil); strict makes
// a law pass only when it passed. A selection that picks none fails.
func runPicked(w io.Writer, patterns []string, match func(string) bool, strict bool, desc string) int {
	return runPickedThen(w, patterns, match, func(_ string, r eval.Result) bool { return holds(r, strict) }, desc)
}

// runPickedThen is runPicked with ok judging each picked result.
func runPickedThen(w io.Writer, patterns []string, match func(string) bool, ok func(dir string, r eval.Result) bool, desc string) int {
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
		n, ok := runDir(w, dir, match, ok)
		selected += n
		if !ok {
			code = 1
		}
	}
	if match != nil && selected == 0 {
		fmt.Fprintf(w, "ergotests: %s selects no test or law of %s\n", desc, strings.Join(patterns, " "))
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
func runDir(w io.Writer, dir string, match func(string) bool, judge func(dir string, r eval.Result) bool) (int, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(w, "FAIL %s: %v\n", dir, err)
		return 0, false
	}
	rep, err := evaluate(abs)
	if err != nil {
		fmt.Fprintf(w, "FAIL %s: %v\n", dir, err)
		return 0, false
	}
	n, ok := 0, true
	for _, r := range rep.Results {
		if match != nil && !match(r.Name) {
			continue
		}
		n++
		if judge(dir, r) {
			fmt.Fprintf(w, "%s %s: %s\n", r.Status, dir, r.Name)
			continue
		}
		ok = false
		fmt.Fprintf(w, "FAIL %s: %s (%s:%d) is %s: %s\n", dir, r.Name, r.Site.File, r.Site.Line, r.Status, r.Reason)
	}
	return n, ok
}

// stdRoot is the directory of the standard library's sources, from the
// repository root.
const stdRoot = "internal/sketch/std"

// evaluate checks the package in dir and runs its obligations at check
// time. A std package (under stdRoot) is checked as std, in-package: the
// standard library this binary embeds, so its laws may call the
// package's private functions and its trusted Go (STD-LAW-RUN);
// checked as a folder it has diagnostics of its own (S108). The mutant
// runner builds this binary in the mutated copy, so it embeds the mutant.
// Any other package is checked as `ergo check` checks a folder.
func evaluate(abs string) (eval.Report, error) {
	if rel, ok := stdPath(abs); ok {
		pkg, err := check.Std.Import(rel)
		if err != nil {
			return eval.Report{}, err
		}
		return eval.Run(pkg, eval.DefaultBudget())
	}
	a, err := sketchfront.CheckDir(abs)
	if err != nil {
		return eval.Report{}, err
	}
	if len(a.Diagnostics) != 0 {
		return eval.Report{}, fmt.Errorf("does not check: %v", a.Diagnostics)
	}
	return a.Evaluate(eval.DefaultBudget())
}

// stdPath is the std import path of the directory abs, when it is one.
func stdPath(abs string) (string, bool) {
	_, rel, ok := strings.Cut(filepath.ToSlash(abs), "/"+stdRoot+"/")
	return rel, ok
}

// isStd says whether dir is a std package's directory.
func isStd(dir string) bool {
	abs, err := filepath.Abs(dir)
	_, ok := stdPath(abs)
	return err == nil && ok
}

// holds says whether an obligation's result passes the run: a test only
// when it passed; a law unless it was refuted or hit a defect, or, strict,
// only when it passed.
func holds(r eval.Result, strict bool) bool {
	if !strict && strings.HasPrefix(r.Name, "law ") {
		return !slices.Contains([]eval.Status{eval.Refuted, eval.Defect}, r.Status)
	}
	return r.Status == eval.Passed
}
