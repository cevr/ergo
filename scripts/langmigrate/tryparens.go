package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// tryParensTree rewrites every package folder under root.
func tryParensTree(root string, out io.Writer) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir // fixtures keep the forms they test
		}
		return tryParensFolder(p, out)
	})
}

// tryParensFolder rewrites the .ergo files of one package folder, each
// checked and lowered with its package (sketchfront.CheckDirOverlay).
func tryParensFolder(dir string, out io.Writer) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ergo") {
			continue
		}
		file := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		text := string(data)
		if len(tryParens(text)) == 0 {
			continue
		}
		lowered := func(src string) (string, bool) {
			a, err := sketchfront.CheckDirOverlay(dir, map[string]string{file: src})
			if err != nil {
				return "", false
			}
			return loweredText(a, nil)
		}
		next, n := rewriteTryParens(text, lowered)
		if n == 0 {
			if _, ok := lowered(text); !ok {
				fmt.Fprintf(out, "%s: kept: the package does not check or lower here\n", file)
			}
			continue
		}
		if err := os.WriteFile(file, []byte(next), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %d removed\n", file, n)
	}
	return nil
}

// parenSpan is the offsets of the parentheses of one (try …).
type parenSpan struct{ l, r int }

// tryParens lists the parenthesized trys of text, a ParenExpr whose
// operand is a TryExpr; nil when text does not parse.
func tryParens(text string) []parenSpan {
	f, err := syntax.Parse(text)
	if err != nil {
		return nil
	}
	var spans []parenSpan
	syntax.Inspect(f, func(n syntax.Node) bool {
		if p, ok := n.(*syntax.ParenExpr); ok {
			if _, try := p.X.(*syntax.TryExpr); try && p.Lparen.IsValid() && p.Rparen.IsValid() &&
				text[p.Lparen] == '(' && text[p.Rparen] == ')' {
				spans = append(spans, parenSpan{int(p.Lparen), int(p.Rparen)})
			}
		}
		return true
	})
	return spans
}

// rewriteTryParens removes each (try …) of text whose removal leaves
// lowered(text) the same, right to left. lowered gives the package's
// lowered Go with the file's text replaced, and false when the package
// does not check or lower; a text it refuses keeps every parenthesis.
// A removal moves no parenthesis to its left, so each one is decided
// once, in the text that holds every decision to its right.
func rewriteTryParens(text string, lowered func(string) (string, bool)) (string, int) {
	want, ok := lowered(text)
	if !ok {
		return text, 0
	}
	removed, cursor := 0, len(text)
	for {
		spans := slices.DeleteFunc(tryParens(text), func(s parenSpan) bool { return s.l >= cursor })
		if len(spans) == 0 {
			return text, removed
		}
		s := slices.MaxFunc(spans, func(a, b parenSpan) int { return a.l - b.l })
		cursor = s.l
		cand := text[:s.l] + text[s.l+1:s.r] + text[s.r+1:]
		if got, ok := lowered(cand); ok && got == want {
			text, removed = cand, removed+1
		}
	}
}

// lineDirective matches the //line and /*line */ position comments of the
// lowered Go, which move with any edit of a line; column and columnText
// match the columns of the call-site tables and of position texts
// (file.ergo:line:column), which move with an edit before them on their
// line. A removal on one line keeps every line number, which stays
// compared. assertText matches the source text an assert's report
// quotes (lower: assert), the one place where the spelling of the source,
// not its meaning, reaches the Go; the rest of the report (the operands
// it shows) stays compared.
var (
	lineDirective = regexp.MustCompile(`(?m)^//line .*\n|/\*line [^*]*\*/`)
	column        = regexp.MustCompile(`Column: \d+`)
	columnText    = regexp.MustCompile(`(\.ergo:\d+):\d+`)
	assertText    = regexp.MustCompile(`(ergoAssertFail\("[^"]*", )"(?:[^"\\]|\\.)*"`)
)

// positionless is lowered Go without what an edit of a line's columns
// moves, and without the source text an assert quotes. The call-site
// table (lower: callTable) lists the calls in source order, by offset,
// so a removal that moves a call's start past another's on its line
// reorders two rows; each run of rows is compared as a set.
func positionless(goText string) string {
	goText = lineDirective.ReplaceAllString(goText, "")
	goText = column.ReplaceAllString(goText, "Column: _")
	goText = assertText.ReplaceAllString(goText, `$1"_"`)
	goText = columnText.ReplaceAllString(goText, "$1:_")
	lines := strings.Split(goText, "\n")
	for i := 0; i < len(lines); {
		j := i
		for j < len(lines) && strings.HasPrefix(lines[j], "\t{Func: ") {
			j++
		}
		slices.Sort(lines[i:j])
		i = max(j, i+1)
	}
	return strings.Join(lines, "\n")
}

// loweredText is the lowered Go of a checked program, as Lower and
// LowerTests write it, without position comments; false when it has
// diagnostics or neither lowering accepts it. source is the text of a
// program of one source text (nil for a folder).
func loweredText(a sketchfront.Analysis, source *string) (string, bool) {
	if len(a.Diagnostics) > 0 {
		return "", false
	}
	var unit lower.Unit
	if source != nil {
		root := a.Root()
		if root.Package == nil {
			return "", false
		}
		unit = lower.Unit{Package: root.Package, Files: []*syntax.File{root.Files[0].Syntax}, Sources: []lower.Source{{Path: "/langmigrate/main.ergo", Text: *source}}}
	} else {
		pkgs := a.Packages()
		if len(pkgs) == 0 {
			return "", false
		}
		texts := a.Files()
		unitOf := func(p sketchfront.CheckedPackage) lower.Unit {
			u := lower.Unit{Package: p.Package}
			for _, f := range p.Files {
				u.Files = append(u.Files, f.Syntax)
				u.Sources = append(u.Sources, lower.Source{Path: filepath.Join(p.Dir, path.Base(f.Rel)), Text: texts[f.Rel]})
			}
			return u
		}
		for _, p := range pkgs {
			if p.Package == nil {
				return "", false
			}
		}
		unit = unitOf(pkgs[len(pkgs)-1])
		for _, p := range pkgs[:len(pkgs)-1] {
			unit.Imports = append(unit.Imports, unitOf(p))
		}
	}
	var b strings.Builder
	lowered := false
	for _, mode := range []func(lower.Unit) ([]lower.File, error){lower.Lower, lower.LowerTests} {
		files, err := lowerSafely(mode, unit)
		if err != nil {
			fmt.Fprintf(&b, "error: %v\n", err)
			continue
		}
		lowered = true
		for _, f := range files {
			fmt.Fprintf(&b, "== %s\n%s\n", f.Path, positionless(string(f.Data)))
		}
	}
	return b.String(), lowered
}

// lowerSafely turns a lowering's internal compiler error into an error, so
// a package the lowering cannot take keeps its text.
func lowerSafely(mode func(lower.Unit) ([]lower.File, error), u lower.Unit) (files []lower.File, err error) {
	defer func() {
		if p := recover(); p != nil {
			files, err = nil, fmt.Errorf("lowering panicked: %v", p)
		}
	}()
	return mode(u)
}
