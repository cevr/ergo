// Package fix writes the clauses a program's signatures miss (q137,
// EXPLICIT §4): `ergo fix`. It never runs during check, build or test.
//
// The inference engine is the clause writer (EXPLICIT F9): one check with
// inference writes the inferred rows of the unexported functions. Then
// the loop checks under check.Options.Explicit, applies the fix of every
// clause diagnostic, and checks again until no clause diagnostic is left:
// writing a callee's clause can make its callers exceed theirs. Rows only
// grow and are finite, so the loop ends; Bound stops it past (functions +
// 1) × (row elements + 1) rounds. It only adds: it never removes an
// element and never adds a catch.
package fix

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Diagnostic is a diagnostic of one check of the program, in the file
// with the key File.
type Diagnostic struct {
	File string
	report.Diagnostic
}

// Outcome is one check of the program: its diagnostics and, under
// check.Options.Clauses, the inferred clauses.
type Outcome struct {
	Diagnostics []Diagnostic
	Clauses     []Diagnostic // Fix set, Code and Message empty
}

// Checker checks the texts of a program, by file key, in the modes opts
// selects.
type Checker func(texts map[string]string, opts check.Options) (Outcome, error)

// Change is one signature the fix widened, in its final form.
type Change struct {
	File      string
	Function  string
	Clause    string
	Signature string
	// Exported is a public contract: an exported function's.
	Exported bool
}

// Result is the program after the fix.
type Result struct {
	// Texts are the program's texts with every clause written; a file
	// the fix did not change keeps its text.
	Texts map[string]string
	// Changed lists the widened signatures, exported ones first, then by
	// file and function.
	Changed []Change
	// Remaining are the diagnostics of the fixed program's last check.
	Remaining []Diagnostic
	// Rounds counts the checks of the loop, the inference pass excluded.
	Rounds int
	// Linted lists the lint fixes RunLint applied, in round order.
	Linted []Linted
}

// Run writes the missing clauses of the program texts, which check reads.
func Run(texts map[string]string, check Checker) (Result, error) {
	return run(texts, check, true)
}

// run is Run; loop false stops after the first round (a test hook for the
// mutant that skips the re-check).
func run(texts map[string]string, chk Checker, loop bool) (Result, error) {
	cur := map[string]string{}
	for k, v := range texts {
		cur[k] = v
	}
	changes := map[[3]string]Change{}
	out, err := chk(cur, check.Options{Clauses: true})
	if err != nil {
		return Result{}, err
	}
	cur, err = apply(cur, out.Clauses, changes)
	if err != nil {
		return Result{}, err
	}
	funcs := 0
	for _, t := range cur {
		funcs += strings.Count(t, "func")
	}
	elems := map[string]bool{}
	res := Result{}
	for {
		res.Rounds++
		out, err := chk(cur, check.Options{Explicit: true})
		if err != nil {
			return Result{}, err
		}
		var fixes []Diagnostic
		for _, d := range out.Diagnostics {
			if d.Fix != nil && d.Fix.Text != "" {
				fixes = append(fixes, d)
				for _, e := range d.Fix.Row {
					elems[e] = true
				}
			}
		}
		if len(fixes) == 0 || !loop && res.Rounds > 1 {
			res.Remaining = out.Diagnostics
			break
		}
		if res.Rounds > Bound(funcs, len(elems)) {
			return Result{}, fmt.Errorf("fix: no fixed point after %d rounds; stopped on function %s in %s", res.Rounds-1, fixes[0].Fix.Function, fixes[0].File)
		}
		cur, err = apply(cur, fixes, changes)
		if err != nil {
			return Result{}, err
		}
	}
	res.Texts = cur
	for _, c := range changes {
		res.Changed = append(res.Changed, c)
	}
	slices.SortFunc(res.Changed, func(a, b Change) int {
		if a.Exported != b.Exported {
			if a.Exported {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Function, b.Function), cmp.Compare(a.Clause, b.Clause))
	})
	return res, nil
}

// Bound is the most rounds the loop takes on a program of funcs
// functions whose clauses hold elems distinct elements: each round but
// the last adds an element to a clause.
func Bound(funcs, elems int) int { return (funcs + 1) * (elems + 1) }

// edit is one insertion into a file's text.
type edit struct {
	at     int
	text   string
	clause string
}

// apply inserts every fix of ds into its file, and the imports each
// needs, and records the widened signatures.
func apply(texts map[string]string, ds []Diagnostic, changes map[[3]string]Change) (map[string]string, error) {
	edits := map[string][]edit{}
	imports := map[string][]string{}
	seen := map[string]bool{}
	for _, d := range ds {
		f := d.Fix
		key := fmt.Sprintf("%s\x00%d\x00%s\x00%s", d.File, f.InsertAt, f.Clause, f.Text)
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := texts[d.File]; !ok {
			return nil, fmt.Errorf("fix: a clause of %s is in %q, which is not a file of the program", f.Function, d.File)
		}
		edits[d.File] = append(edits[d.File], edit{at: f.InsertAt, text: f.Text, clause: f.Clause})
		for _, p := range f.Imports {
			if !slices.Contains(imports[d.File], p) {
				imports[d.File] = append(imports[d.File], p)
			}
		}
		r, _ := utf8.DecodeRuneInString(f.Function)
		changes[[3]string{d.File, f.Function, f.Clause}] = Change{File: d.File, Function: f.Function, Clause: f.Clause, Signature: f.Signature, Exported: unicode.IsUpper(r)}
	}
	out := map[string]string{}
	for k, v := range texts {
		out[k] = v
	}
	for file, es := range edits {
		src := texts[file]
		ims, err := importEdits(src, imports[file])
		if err != nil {
			return nil, fmt.Errorf("fix: %s: %w", file, err)
		}
		es = append(es, ims...)
		// From the end, so each offset holds; at one offset fails goes
		// before needs, so needs is inserted first.
		slices.SortStableFunc(es, func(a, b edit) int {
			return cmp.Or(cmp.Compare(b.at, a.at), cmp.Compare(rank(a.clause), rank(b.clause)))
		})
		for _, e := range es {
			src = src[:e.at] + e.text + src[e.at:]
		}
		out[file] = src
	}
	return out, nil
}

func rank(clause string) int {
	switch clause {
	case "needs":
		return 0
	case "fails":
		return 1
	}
	return 2 // an import goes in front of a clause at the same offset
}

// importEdits are the insertions that import each path of paths src does
// not import yet: after the last import, or before the first declaration.
func importEdits(src string, paths []string) ([]edit, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	f, err := syntax.Parse(src)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	var last *syntax.ImportDecl
	for _, d := range f.Decls {
		im, ok := d.(*syntax.ImportDecl)
		if !ok {
			continue
		}
		last = im
		for _, s := range im.Specs {
			have[strings.Trim(s.Path.Value, `"`)] = true
		}
	}
	var lines []string
	for _, p := range paths {
		if !have[p] {
			lines = append(lines, "import "+p)
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	if last != nil {
		return []edit{{at: int(last.End()), text: "\n" + strings.Join(lines, "\n"), clause: "import"}}, nil
	}
	at := int(f.Pos())
	if at >= len(src) {
		return []edit{{at: len(src), text: strings.Join(lines, "\n") + "\n", clause: "import"}}, nil
	}
	return []edit{{at: at, text: strings.Join(lines, "\n") + "\n\n", clause: "import"}}, nil
}
