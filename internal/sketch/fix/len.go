package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/genmark"
	"ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// LenRewrite records one universe `len(e)` call rewritten to `e.Len()` (or
// `(e).Len()`).
type LenRewrite struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// LenReport is RunLen's result: the new text of each file changed, every
// rewritten call site, and any package/file errors encountered.
type LenReport struct {
	Files    map[string]string
	Rewrites []LenRewrite
	Errs     []string
}

// Text formats one summary line per changed file, followed by any errors.
func (r LenReport) Text() string {
	var b strings.Builder
	files := make([]string, 0, len(r.Files))
	for f := range r.Files {
		files = append(files, f)
	}
	slices.Sort(files)
	count := map[string]int{}
	for _, x := range r.Rewrites {
		count[x.File]++
	}
	for _, f := range files {
		fmt.Fprintf(&b, "%s: rewrote %d len calls\n", f, count[f])
	}
	for _, e := range r.Errs {
		fmt.Fprintf(&b, "%s\n", e)
	}
	return b.String()
}

// LenSource rewrites every universe `len(e)` call in the standalone Ergo source
// src to `e.Len()`, parenthesizing e when required by postfix precedence. Calls
// to a user-declared `len` (PRELUDE SHADOW) are left untouched. When src is
// already formatted, the returned text is formatted to a fixpoint.
func LenSource(src, name string) (string, []LenRewrite, error) {
	pkg, _ := check.CheckSource(src)
	if pkg == nil || pkg.Info == nil {
		return src, nil, nil
	}
	files := pkg.Files()
	if len(files) != 1 {
		return src, nil, nil
	}
	out, rewrites, err := rewriteLenFiles(map[string]string{name: src}, []string{name}, files, pkg.Info)
	if err != nil {
		return src, nil, err
	}
	return out[name], rewrites, nil
}

// LenPackage rewrites every universe `len(e)` call in the package at dir over
// texts (keyed by absolute file path). Rewrites are kept only when the
// rewritten package checks with the same diagnostics as before.
func LenPackage(dir string, texts map[string]string) (map[string]string, []LenRewrite, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return texts, nil, err
	}
	a, err := sketchfront.CheckDirOptions(abs, texts, check.Options{})
	if err != nil {
		return texts, nil, err
	}
	_, pkg, files, ok := a.RootPackage()
	if !ok || pkg == nil || pkg.Info == nil {
		return texts, nil, nil
	}
	paths := matchCheckedFiles(abs, texts, files)
	if len(paths) != len(files) {
		return texts, nil, fmt.Errorf("%s: checked file count %d does not match texts", abs, len(files))
	}
	out, rewrites, err := rewriteLenFiles(texts, paths, files, pkg.Info)
	if err != nil || len(rewrites) == 0 {
		return out, rewrites, err
	}
	before, err := flatVerdict(abs, texts)
	if err != nil {
		return texts, nil, err
	}
	after, err := flatVerdict(abs, out)
	if err != nil {
		return texts, nil, err
	}
	if !slices.Equal(before, after) {
		return texts, nil, fmt.Errorf("%s: rewriting len calls would change the check verdict (before %v, after %v)", abs, before, after)
	}
	return out, rewrites, nil
}

// RunLen rewrites universe `len(e)` calls across opts.Paths (module packages
// and standalone .ergo files) and opts.StdRoot (if set). Generated folders,
// gostd, and unadmitted folders (e.g. testdata) are skipped when walking a
// parent folder. Files are written only when opts.Write is true.
func RunLen(opts Options) (LenReport, error) {
	rep := LenReport{Files: map[string]string{}}
	stdRoot := ""
	if opts.StdRoot != "" {
		abs, err := filepath.Abs(opts.StdRoot)
		if err != nil {
			return rep, err
		}
		stdRoot = abs
		if err := runLenStd(&rep, stdRoot); err != nil {
			return rep, err
		}
	}
	doneDir := map[string]bool{}
	doneFile := map[string]bool{}
	if stdRoot != "" {
		doneDir[stdRoot] = true
		for _, p := range stdlib.Packages() {
			doneDir[filepath.Join(stdRoot, filepath.FromSlash(p))] = true
		}
	}
	oneDir := func(dir string) error {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if doneDir[abs] || genmark.Held(abs) {
			return nil
		}
		doneDir[abs] = true
		files := ergoFiles(abs)
		if len(files) == 0 {
			return nil
		}
		texts := map[string]string{}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			texts[f] = string(b)
		}
		out, rewrites, err := LenPackage(abs, texts)
		if err != nil {
			rep.Errs = append(rep.Errs, err.Error())
			return nil
		}
		if len(rewrites) == 0 {
			return nil
		}
		rep.Rewrites = append(rep.Rewrites, rewrites...)
		for f, text := range out {
			if text != texts[f] {
				rep.Files[f] = text
			}
		}
		return nil
	}
	oneFile := func(file string) error {
		abs, err := filepath.Abs(file)
		if err != nil {
			return err
		}
		if doneFile[abs] || doneDir[filepath.Dir(abs)] {
			return nil
		}
		if stdRoot != "" && stdFile(stdRoot, abs) {
			return nil
		}
		doneFile[abs] = true
		b, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		src := string(b)
		out, rewrites, err := LenSource(src, abs)
		if err != nil {
			rep.Errs = append(rep.Errs, err.Error())
			return nil
		}
		if len(rewrites) == 0 {
			return nil
		}
		before, err := flatVerdict("", map[string]string{abs: src})
		if err != nil {
			return err
		}
		after, err := flatVerdict("", map[string]string{abs: out})
		if err != nil {
			return err
		}
		if !slices.Equal(before, after) {
			rep.Errs = append(rep.Errs, fmt.Sprintf("%s: rewriting len calls would change the check verdict (before %v, after %v)", abs, before, after))
			return nil
		}
		rep.Rewrites = append(rep.Rewrites, rewrites...)
		if out != src {
			rep.Files[abs] = out
		}
		return nil
	}
	for _, root := range opts.Paths {
		abs, err := filepath.Abs(root)
		if err != nil {
			return rep, err
		}
		if err := walkLen(abs, oneFile, func(mod string) error {
			if stdRoot != "" && mod == stdRoot {
				return nil
			}
			// If walking internal/sketch/std directly without --std, treat it as std.
			if stdRoot == "" && isStdRoot(mod) {
				stdRoot = mod
				doneDir[stdRoot] = true
				for _, p := range stdlib.Packages() {
					doneDir[filepath.Join(stdRoot, filepath.FromSlash(p))] = true
				}
				return runLenStd(&rep, stdRoot)
			}
			targets, err := sketchfront.DiscoverPackages(mod)
			if err != nil {
				return err
			}
			for _, t := range targets {
				if err := oneDir(t.Dir); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return rep, err
		}
	}
	if opts.Write {
		for file, text := range rep.Files {
			info, err := os.Stat(file)
			if err != nil {
				return rep, err
			}
			if err := os.WriteFile(file, []byte(text), info.Mode().Perm()); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}

// isStdRoot reports whether dir is the repository's internal/sketch/std root.
func isStdRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "prelude.ergo")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "builtin.ergo")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "core", "core.ergo"))
	return err == nil
}

// runLenStd rewrites universe len(e) calls in the standard library under
// stdRoot (prelude.ergo plus every stdlib package folder).
func runLenStd(rep *LenReport, stdRoot string) error {
	// Prelude files at stdRoot (prelude.ergo, builtin.ergo).
	if pre := check.PreludePackage(); pre != nil && pre.Info != nil {
		preFiles := pre.Files()
		texts := map[string]string{}
		paths := make([]string, 0, len(preFiles))
		matched := true
		for _, f := range preFiles {
			p := filepath.Join(stdRoot, f.Name)
			b, err := os.ReadFile(p)
			if err != nil {
				matched = false
				break
			}
			if string(b) != stdlib.Prelude()[f.Name] {
				matched = false
				break
			}
			texts[p] = string(b)
			paths = append(paths, p)
		}
		if matched && len(paths) == len(preFiles) {
			out, rewrites, err := rewriteLenFiles(texts, paths, preFiles, pre.Info)
			if err != nil {
				rep.Errs = append(rep.Errs, err.Error())
			} else if len(rewrites) > 0 {
				rep.Rewrites = append(rep.Rewrites, rewrites...)
				for f, text := range out {
					if text != texts[f] {
						rep.Files[f] = text
					}
				}
			}
		}
	}
	for _, pkgPath := range stdlib.Packages() {
		dir := filepath.Join(stdRoot, filepath.FromSlash(pkgPath))
		if genmark.Held(dir) {
			continue
		}
		files := ergoFiles(dir)
		if len(files) == 0 {
			continue
		}
		texts := map[string]string{}
		parsed := make([]*syntax.File, 0, len(files))
		parseOK := true
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			src := string(b)
			texts[f] = src
			pf, err := syntax.Parse(src)
			if err != nil {
				parseOK = false
				break
			}
			pf.Name = filepath.Base(f)
			parsed = append(parsed, pf)
		}
		if !parseOK {
			continue
		}
		var pkg *check.Package
		var beforeDiags []check.Diagnostic
		if pkgPath == "core" || pkgPath == "option" || pkgPath == "collections" {
			a, err := sketchfront.CheckDirOptions(dir, texts, check.Options{})
			if err != nil {
				rep.Errs = append(rep.Errs, dir+": "+err.Error())
				continue
			}
			var ok bool
			_, pkg, parsed, ok = a.RootPackage()
			if !ok || pkg == nil || pkg.Info == nil {
				continue
			}
			files = matchCheckedFiles(dir, texts, parsed)
			out, rewrites, err := rewriteLenFiles(texts, files, parsed, pkg.Info)
			if err != nil {
				rep.Errs = append(rep.Errs, err.Error())
				continue
			}
			if len(rewrites) == 0 {
				continue
			}
			before, err := flatVerdict(dir, texts)
			if err != nil {
				return err
			}
			after, err := flatVerdict(dir, out)
			if err != nil {
				return err
			}
			if !slices.Equal(before, after) {
				rep.Errs = append(rep.Errs, fmt.Sprintf("%s: rewriting len calls would change the check verdict (before %v, after %v)", dir, before, after))
				continue
			}
			rep.Rewrites = append(rep.Rewrites, rewrites...)
			for f, text := range out {
				if text != texts[f] {
					rep.Files[f] = text
				}
			}
			continue
		}
		pkg, beforeDiags = check.StdFixture(pkgPath, parsed)
		if pkg == nil || pkg.Info == nil {
			continue
		}
		out, rewrites, err := rewriteLenFiles(texts, files, parsed, pkg.Info)
		if err != nil {
			rep.Errs = append(rep.Errs, err.Error())
			continue
		}
		if len(rewrites) == 0 {
			continue
		}
		afterParsed := make([]*syntax.File, 0, len(files))
		for _, f := range files {
			pf, err := syntax.Parse(out[f])
			if err != nil {
				rep.Errs = append(rep.Errs, f+": rewritten source does not parse: "+err.Error())
				afterParsed = nil
				break
			}
			pf.Name = filepath.Base(f)
			afterParsed = append(afterParsed, pf)
		}
		if afterParsed == nil {
			continue
		}
		_, afterDiags := check.StdFixture(pkgPath, afterParsed)
		if !slices.Equal(diagKeys(beforeDiags), diagKeys(afterDiags)) {
			rep.Errs = append(rep.Errs, fmt.Sprintf("%s: rewriting len calls would change the check verdict (before %v, after %v)", dir, diagKeys(beforeDiags), diagKeys(afterDiags)))
			continue
		}
		rep.Rewrites = append(rep.Rewrites, rewrites...)
		for f, text := range out {
			if text != texts[f] {
				rep.Files[f] = text
			}
		}
	}
	return nil
}

func diagKeys(ds []check.Diagnostic) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Code+" "+d.Message)
	}
	slices.Sort(out)
	return out
}

// walkLen walks root like walk in fix.go, additionally skipping unadmitted
// folders (e.g. testdata) and generated folders when descending from a parent.
func walkLen(root string, file func(string) error, module func(string) error) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if strings.HasSuffix(root, ".ergo") {
			return file(root)
		}
		return nil
	}
	packaged := map[string]bool{}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (!sketchfront.AdmitFolder(name) || name == "gostd") {
				return filepath.SkipDir
			}
			if genmark.Held(path) {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, sketchfront.ModFile)); err == nil {
				if err := module(path); err != nil {
					return err
				}
				if targets, err := sketchfront.DiscoverPackages(path); err == nil {
					for _, t := range targets {
						packaged[t.Dir] = true
					}
				}
			}
			return nil
		}
		if strings.HasSuffix(path, ".ergo") && !packaged[filepath.Dir(path)] && !packaged[evalDir(filepath.Dir(path))] {
			return file(path)
		}
		return nil
	})
}

func matchCheckedFiles(dir string, texts map[string]string, files []*syntax.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f.Name != "" {
			if _, ok := texts[f.Name]; ok {
				out = append(out, f.Name)
				continue
			}
			joined := filepath.Join(dir, filepath.Base(f.Name))
			if _, ok := texts[joined]; ok {
				out = append(out, joined)
				continue
			}
		}
		// Fallback: match by exact source text when Name is unset.
		matched := ""
		for p, src := range texts {
			if src == f.Src {
				matched = p
				break
			}
		}
		if matched == "" {
			return nil
		}
		out = append(out, matched)
	}
	return out
}

func rewriteLenFiles(texts map[string]string, filePaths []string, files []*syntax.File, info *check.Info) (map[string]string, []LenRewrite, error) {
	out := make(map[string]string, len(texts))
	for k, v := range texts {
		out[k] = v
	}
	var allRewrites []LenRewrite
	for i, f := range files {
		path := filePaths[i]
		src := texts[path]
		eds, rewrites := collectLenEdits(src, path, f, info)
		if len(eds) == 0 {
			continue
		}
		next := applyLenEdits(src, eds)
		if _, err := syntax.Parse(next); err != nil {
			return texts, nil, fmt.Errorf("%s: rewritten len source does not parse: %w", path, err)
		}
		if formatted, err := hostsyntax.Source(src); err == nil && formatted == src {
			if next, err = hostsyntax.Source(next); err != nil {
				return texts, nil, fmt.Errorf("%s: rewritten len source does not format: %w", path, err)
			}
		}
		out[path] = next
		allRewrites = append(allRewrites, rewrites...)
	}
	return out, allRewrites, nil
}

// collectLenEdits finds every universe len(e) call in f and builds strictly
// non-overlapping boundary edits (replacing `len(` and `)`) so nested calls
// such as len(xs[0:len(xs)-1]) and interpolations such as "\(len(f(x)))"
// rewrite in one pass without span collisions.
func collectLenEdits(src, filePath string, f *syntax.File, info *check.Info) ([]edit, []LenRewrite) {
	var eds []edit
	var rewrites []LenRewrite
	seen := map[[2]int]bool{}
	syntax.Inspect(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if !isUniverseLenCall(call, info) {
			return true
		}
		arg := call.Args[0]
		start, end := int(call.Pos()), int(call.End())
		argStart, argEnd := int(arg.Pos()), int(arg.End())
		lparen, rparen := int(call.Lparen), int(call.Rparen)
		if start < 0 || end > len(src) || start >= end || argStart <= lparen || argEnd > rparen || lparen < start || rparen >= end {
			return true
		}
		if !strings.HasPrefix(strings.TrimLeft(src[start:lparen], "("), "len") {
			return true
		}
		key := [2]int{start, end}
		if seen[key] {
			return true
		}
		seen[key] = true

		parens := needsLenParens(arg, info)
		lead := src[lparen+1 : argStart]
		trail := src[argEnd:rparen]
		leadComment := hasLenComment(lead)
		trailComment := hasLenComment(trail)

		switch {
		case leadComment:
			suffix := ").Len()"
			if trailComment {
				suffix += stripLeadingComma(trail)
			}
			eds = append(eds,
				edit{from: start, to: lparen + 1, text: "("},
				edit{from: argEnd, to: end, text: suffix},
			)
		case trailComment:
			openText, closeText := "", ".Len()"
			if parens {
				openText, closeText = "(", ").Len()"
			}
			eds = append(eds,
				edit{from: start, to: argStart, text: openText},
				edit{from: argEnd, to: end, text: closeText + stripLeadingComma(trail)},
			)
		case parens:
			eds = append(eds,
				edit{from: start, to: argStart, text: "("},
				edit{from: argEnd, to: end, text: ").Len()"},
			)
		default:
			eds = append(eds,
				edit{from: start, to: argStart, text: ""},
				edit{from: argEnd, to: end, text: ".Len()"},
			)
		}

		line, col := f.Position(call.Pos())
		before := src[start:end]
		after := src[argStart:argEnd] + ".Len()"
		if parens || leadComment {
			after = "(" + src[argStart:argEnd] + ").Len()"
		}
		rewrites = append(rewrites, LenRewrite{
			File:   filePath,
			Line:   line,
			Column: col,
			Before: before,
			After:  after,
		})
		return true
	})
	return eds, rewrites
}

func isUniverseLenCall(call *syntax.CallExpr, info *check.Info) bool {
	if call == nil || info == nil || len(call.Args) != 1 || call.Trailing != nil || call.TrailingFunc {
		return false
	}
	c := info.Calls[call]
	if c == nil || c.Kind != check.CallLen {
		return false
	}
	id, ok := unparenExpr(call.Fun).(*syntax.Ident)
	if !ok || id.Name != "len" {
		return false
	}
	b, ok := info.Uses[id].(*check.Builtin)
	return ok && b.Name == "len"
}

// needsLenParens reports whether arg must be parenthesised as `(arg).Len()`
// rather than `arg.Len()` so postfix selection binds to the whole operand
// (NO-PRELUDE §6: unary, binary, conversion, *p, if/match, try/catch, or
// composite literals). Simple identifiers, selectors, index/slice expressions,
// non-conversion calls, literals, interpolated strings, and already-parenthesised
// expressions need no extra parentheses.
func needsLenParens(arg syntax.Expr, info *check.Info) bool {
	switch e := arg.(type) {
	case *syntax.Ident, *syntax.BasicLit, *syntax.InterpolatedString,
		*syntax.ParenExpr, *syntax.SelectorExpr, *syntax.IndexExpr, *syntax.SliceExpr:
		return false
	case *syntax.CallExpr:
		if e.Trailing != nil || e.TrailingFunc {
			return true
		}
		if info != nil {
			if c := info.Calls[e]; c != nil && c.Kind == check.CallConversion {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func unparenExpr(x syntax.Expr) syntax.Expr {
	for {
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}

func hasLenComment(s string) bool {
	return strings.Contains(s, "//") || strings.Contains(s, "/*")
}

// stripLeadingComma removes a trailing argument comma immediately following
// the operand in trail, preserving any trailing comments after it.
func stripLeadingComma(trail string) string {
	for i := 0; i < len(trail); i++ {
		switch trail[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case ',':
			return trail[:i] + trail[i+1:]
		default:
			return trail
		}
	}
	return trail
}

func applyLenEdits(src string, eds []edit) string {
	ordered := slices.Clone(eds)
	slices.SortStableFunc(ordered, func(a, b edit) int {
		if a.from != b.from {
			return b.from - a.from
		}
		return b.to - a.to
	})
	out := src
	for _, e := range ordered {
		out = out[:e.from] + e.text + out[e.to:]
	}
	return out
}
