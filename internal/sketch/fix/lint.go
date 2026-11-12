package fix

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// Linter lints the program texts and returns the findings `ergo fix
// --lint` applies (lint.Configuration.Fixable), each with its Fix, in the
// file with the key File.
type Linter func(texts map[string]string) ([]Diagnostic, error)

// Linted is one lint fix the run applied: its rule and where its finding
// was, in the text the round linted.
type Linted struct {
	Rule string `json:"rule"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// RunLint is Run, then the lint fixes (plan LINT1): when the clause loop
// leaves no diagnostic, it lints, applies every non-overlapping fixable
// finding, checks under Explicit, and repeats until no fixable finding is
// left. A round whose fixes leave a compiler diagnostic refuses the whole
// run. The rounds are bounded by the first round's findings + 1: a safe
// fix removes its finding and adds none.
func RunLint(texts map[string]string, chk Checker, lintr Linter) (Result, error) {
	res, err := run(texts, chk, true)
	if err != nil || lintr == nil || len(res.Remaining) > 0 {
		return res, err
	}
	cur := res.Texts
	bound := -1
	for round := 1; ; round++ {
		ds, err := lintr(cur)
		if err != nil {
			return Result{}, err
		}
		if len(ds) == 0 {
			break
		}
		if bound < 0 {
			bound = len(ds) + 1
		}
		if round > bound {
			return Result{}, fmt.Errorf("fix: the lint fixes reach no fixed point after %d rounds; stopped on %s in %s", round-1, ds[0].Fix.Rule, ds[0].File)
		}
		next, applied, unused, err := applyLint(cur, ds)
		if err != nil {
			return Result{}, err
		}
		out, err := chk(next, check.Options{Explicit: true})
		if err != nil {
			return Result{}, err
		}
		if dropped, ok := dropUnused(next, out.Diagnostics, unused); ok {
			next = dropped
			if out, err = chk(next, check.Options{Explicit: true}); err != nil {
				return Result{}, err
			}
		}
		if len(out.Diagnostics) > 0 {
			d := out.Diagnostics[0]
			return Result{}, fmt.Errorf("fix: refused the lint fixes of %s (%s:%d and %d more): the fixed program has %s at %s:%d: %s", applied[0].Rule, applied[0].File, applied[0].Line, len(applied)-1, d.Code, d.File, d.Span.Line, d.Message)
		}
		cur = next
		res.Linted = append(res.Linted, applied...)
	}
	res.Texts = cur
	return res, nil
}

// dropUnused removes each import that a diagnostic of the fixed program
// lands on when the applied fixes named its path as one they may leave
// unused (report.Fix.Unused), and reports whether it removed any.
func dropUnused(texts map[string]string, ds []Diagnostic, unused map[string][]string) (map[string]string, bool) {
	out := map[string]string{}
	for k, v := range texts {
		out[k] = v
	}
	dropped := false
	for file, paths := range unused {
		f, err := syntax.Parse(texts[file])
		if err != nil {
			continue
		}
		var cut [][2]int
		for _, d := range f.Decls {
			im, ok := d.(*syntax.ImportDecl)
			if !ok || !slices.Contains(paths, strings.Trim(im.Specs[0].Path.Value, `"`)) {
				continue
			}
			for _, diag := range ds {
				if diag.File == file && diag.Span.Offset >= int(im.Pos()) && diag.Span.Offset < int(im.End()) {
					src := texts[file]
					end := int(im.End())
					if end < len(src) && src[end] == '\n' {
						end++
					}
					cut = append(cut, [2]int{int(im.Pos()), end})
					break
				}
			}
		}
		src := texts[file]
		for i := len(cut) - 1; i >= 0; i-- {
			src = src[:cut[i][0]] + src[cut[i][1]:]
			dropped = true
		}
		out[file] = src
	}
	return out, dropped
}

// applyLint applies the fixes of ds that do not overlap an earlier one in
// their file (a later round takes the rest), and the imports they need. It
// returns, by file, the imports the applied fixes may leave unused.
func applyLint(texts map[string]string, ds []Diagnostic) (map[string]string, []Linted, map[string][]string, error) {
	byFile := map[string][]Diagnostic{}
	for _, d := range ds {
		if _, ok := texts[d.File]; !ok {
			return nil, nil, nil, fmt.Errorf("fix: a %s fix is in %q, which is not a file of the program", d.Fix.Rule, d.File)
		}
		byFile[d.File] = append(byFile[d.File], d)
	}
	unused := map[string][]string{}
	out := map[string]string{}
	for k, v := range texts {
		out[k] = v
	}
	var applied []Linted
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	slices.Sort(files)
	for _, file := range files {
		fs := byFile[file]
		slices.SortStableFunc(fs, func(a, b Diagnostic) int { return cmp.Compare(a.Fix.InsertAt, b.Fix.InsertAt) })
		src := texts[file]
		var keep []Diagnostic
		end := -1
		var imports []string
		for _, d := range fs {
			f := d.Fix
			if f.InsertAt < end || f.InsertAt < 0 || f.InsertAt+f.Replace > len(src) {
				continue
			}
			end = f.InsertAt + f.Replace
			keep = append(keep, d)
			for _, p := range f.Imports {
				if !slices.Contains(imports, p) {
					imports = append(imports, p)
				}
			}
			for _, p := range f.Unused {
				if !slices.Contains(unused[file], p) {
					unused[file] = append(unused[file], p)
				}
			}
			applied = append(applied, Linted{Rule: f.Rule, File: file, Line: d.Span.Line})
		}
		ims, err := importEdits(src, imports)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fix: %s: %w", file, err)
		}
		// From the end, so each offset holds. An import edit is before the
		// first declaration, so before every fix.
		for i := len(keep) - 1; i >= 0; i-- {
			f := keep[i].Fix
			src = src[:f.InsertAt] + f.Text + src[f.InsertAt+f.Replace:]
		}
		for i := len(ims) - 1; i >= 0; i-- {
			src = src[:ims[i].at] + ims[i].text + src[ims[i].at:]
		}
		out[file] = src
	}
	return out, applied, unused, nil
}

// LintOf is the Linter of the program at path, a source file or a package
// folder, as Load reads it, under the configuration conf.
func LintOf(path string, conf *lint.Configuration) (Linter, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return func(texts map[string]string) ([]Diagnostic, error) {
			text, ok := texts[abs]
			if !ok {
				return nil, fmt.Errorf("fix: no text for %s", abs)
			}
			return fixable(conf, sketchfront.Check(text), func(string) string { return abs }), nil
		}, nil
	}
	return func(texts map[string]string) ([]Diagnostic, error) {
		a, err := sketchfront.CheckDirOptions(abs, texts, check.Options{})
		if err != nil {
			return nil, err
		}
		paths := a.SourcePaths()
		return fixable(conf, a, func(rel string) string {
			if p, ok := paths[rel]; ok {
				return p
			}
			return rel
		}), nil
	}, nil
}

// fixable lints the analysis and keeps the findings the run applies.
func fixable(conf *lint.Configuration, a sketchfront.Analysis, key func(rel string) string) []Diagnostic {
	var out []Diagnostic
	for _, f := range conf.Run(lint.InputOf(a)).Findings {
		if conf.Fixable(f) {
			out = append(out, Diagnostic{File: key(f.File), Diagnostic: report.Diagnostic{
				Code: f.Code, Message: f.Message, Fix: f.Fix,
				Span: report.Span{Offset: f.Offset, Length: f.Length, Line: f.Line, Column: f.Column},
			}})
		}
	}
	return out
}
