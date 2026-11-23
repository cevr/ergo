package main

import (
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketchfront"
)

// stdRedFile lists, from the module root, what does not pass on its own
// source yet, one line each, "KEY<TAB>CLASS: why<TAB>REGISTER-ID": a std
// law with no Go runner that no tier runs ("pkg Law"), an obligation that
// fails there ("pkg Law" for a law, "pkg Name" for a test), or a package
// whose evaluation stops ("pkg *"). CLASS is how it fails (redClass). It
// only shrinks: a listed key that passes or names nothing, and one that
// now fails in another class, fails the run until its line goes or is
// rewritten, so no line hides a second bug.
const stdRedFile = stdlib.TestRedPath

// stdRun is what -std saw: each law's verdicts (by "pkg Law"; one per
// provider or state it runs for) and the class of each key that failed.
type stdRun struct {
	verdicts map[string][]eval.Status
	failed   map[string]string
	// native is each law's verdicts in its package's native run
	// (runStdNative): true for ok, false for a skip or a failure.
	native map[string][]bool
	// nativeMissing records a skip on the prelude's own source, not
	// a verdict of the same law in another package with a provider.
	nativeMissing map[string]string
	// declared is the declaring package of each member law, by "q T.L",
	// q the last element of its path (memberLaws).
	declared map[string]string
	// unqualified maps member law report names available without an import
	// to their declaring package. A root's own law still comes first.
	unqualified map[string]string
	// bindings is where the evaluator saw a concrete provider or context
	// for each law, using the same key as verdicts.
	bindings map[string][]string
}

// unqualifiedMembers indexes the checked names written bare in a foreign
// test root, as the native runner names them.
func unqualifiedMembers(pkg *check.Package, declared map[string]string) map[string]string {
	out := map[string]string{}
	for _, law := range pkg.Laws {
		local := law.Title(pkg)
		if strings.Contains(local, ".") && law.Title(nil) == local && declared[path.Base(pkg.Path)+" "+local] == pkg.Path {
			out[local] = pkg.Path
		}
	}
	return out
}

// memberLaws maps each member law of the ledger, "q T.L" with q the last
// element of its package's path, to that package.
func memberLaws(rows []lawledger.Row) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		if strings.Contains(r.Law, ".") {
			out[path.Base(r.Pkg)+" "+r.Law] = r.Pkg
		}
	}
	return out
}

// key is the key a verdict of law, run in the package pkg, counts for. A
// member law of an operation type another package declares runs in its
// providers' packages under its qualified name, q.T.L ("law
// workflow.Store.X for Store" in workflow/memory), and counts for the
// declaring package's law, "workflow Store.X" (LAW-RUNS-CREDIT-PROVIDERS).
// Every other law counts for pkg's.
func (run stdRun) key(pkg, law string) string {
	if q, member, ok := strings.Cut(law, "."); ok && strings.Contains(member, ".") {
		if decl, ok := run.declared[q+" "+member]; ok {
			return decl + " " + member
		}
	}
	// A member law visible bare is reported as T.L in a provider
	// package. Credit it only when pkg declares no T.L of its own.
	if _, own := run.declared[path.Base(pkg)+" "+law]; !own && strings.Contains(law, ".") {
		if decl, ok := run.unqualified[law]; ok {
			return decl + " " + law
		}
	}
	return pkg + " " + law
}

func (run *stdRun) recordLaw(pkg, law string, r eval.Result) string {
	key := run.key(pkg, law)
	run.verdicts[key] = append(run.verdicts[key], r.Status)
	// A provider or context adds a binding suffix to the verdict name.
	// The bare declaration's placeholder skip has no such suffix.
	if r.Name != "law "+law && !slices.Contains(run.bindings[key], pkg) {
		run.bindings[key] = append(run.bindings[key], pkg)
	}
	return key
}

// redLine is a line of stdRedFile.
type redLine struct{ class, why, id string }

// runStd is -std: it runs the planned packages' laws and tests on
// their own source, in-package, prints each verdict, and
// holds that each obligation passed and that every law the ledger gives
// no Go runner ran there, unless stdRedFile lists it in the class it
// fails in (NATIVE-LAWS: a law no tier ran fails by name).
func runStd(w io.Writer, root, ergo string) int {
	rows, err := lawledger.Read()
	if err != nil {
		fmt.Fprintln(w, "FAIL the std law ledger:", err)
		return 1
	}
	declared := memberLaws(rows)
	run := stdRun{verdicts: map[string][]eval.Status{}, failed: map[string]string{}, native: map[string][]bool{}, nativeMissing: map[string]string{}, declared: declared, unqualified: unqualifiedMembers(check.PreludePackage(), declared), bindings: map[string][]string{}}
	inPlace, nativeOnly := sketchfront.StdTestPlan()
	for _, pkg := range inPlace {
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
				key = run.recordLaw(pkg, law, r)
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
	var bare []string
	for _, r := range rows {
		if r.Runner == lawledger.NoRunner {
			bare = append(bare, r.Key())
		}
	}
	if code := runStdNative(w, root, ergo, nativeOnly, bare, &run); code != 0 {
		return code
	}
	shipped, err := stdlib.TestReds()
	if err != nil {
		fmt.Fprintln(w, "FAIL", err)
		return 1
	}
	listed := make(map[string]redLine, len(shipped))
	for key, row := range shipped {
		listed[key] = redLine{row.Class, row.Why, row.ID}
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
	for key := range run.nativeMissing {
		if needs[key] == "" {
			needs[key] = stdlib.NativeSkipClass
		}
	}
	for _, key := range bare {
		ran := ranInPlace(run.verdicts[key]) || ranNatively(run.native[key])
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
		case line.class == stdlib.NativeSkipClass && line.why != run.nativeMissing[key]:
			out = append(out, fmt.Sprintf("%s skipped for a different reason: %s", key, run.nativeMissing[key]))
		}
	}
	for _, key := range sortedKeys(listed) {
		if _, ok := needs[key]; !ok {
			out = append(out, fmt.Sprintf("%s lists %s (%s), which passes on its own source or names nothing: delete its line (the list only shrinks)", stdRedFile, key, listed[key].id))
		}
	}
	return out
}

// ranInPlace reports whether a law's in-place verdicts say it ran: some
// verdict, and every one passed.
func ranInPlace(statuses []eval.Status) bool {
	return len(statuses) > 0 && !slices.ContainsFunc(statuses, func(s eval.Status) bool { return s != eval.Passed })
}

// ranNatively reports whether a law's native verdicts say it ran: some
// verdict, and every one passed (a skip runs nothing).
func ranNatively(passed []bool) bool {
	return len(passed) > 0 && !slices.Contains(passed, false)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
