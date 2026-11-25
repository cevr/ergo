package fix

import (
	"fmt"
	"os"
	"path"
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

// GlobalFuncRewrite records one universe/prelude global function call rewritten
// to its qualified std-package home (NO-PRELUDE §3.2, Unit 5).
type GlobalFuncRewrite struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// GlobalFuncsReport is RunGlobalFuncs's result: the new text of each changed
// file, every rewritten call site, and any package/file errors encountered.
type GlobalFuncsReport struct {
	Files    map[string]string
	Rewrites []GlobalFuncRewrite
	Errs     []string
}

// Text formats one summary line per changed file, followed by any errors.
func (r GlobalFuncsReport) Text() string {
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
		fmt.Fprintf(&b, "%s: rewrote %d global function calls\n", f, count[f])
	}
	for _, e := range r.Errs {
		fmt.Fprintf(&b, "%s\n", e)
	}
	return b.String()
}

type globalFuncSpec struct {
	pkg         string
	target      string
	builtinKind check.CallKind // non-zero for universe builtins; zero for prelude funcs
}

func globalFuncFor(name string) (globalFuncSpec, bool) {
	switch name {
	case "hash":
		return globalFuncSpec{pkg: "maps", target: "Hash", builtinKind: check.CallHash}, true
	case "encode":
		return globalFuncSpec{pkg: "codec", target: "Encode", builtinKind: check.CallEncode}, true
	case "decode":
		return globalFuncSpec{pkg: "codec", target: "Decode", builtinKind: check.CallDecode}, true
	case "embedded":
		return globalFuncSpec{pkg: "embed", target: "File", builtinKind: check.CallEmbed}, true
	case "MakeSlice":
		return globalFuncSpec{pkg: "slices", target: "WithCapacity"}, true
	case "MakeMap":
		return globalFuncSpec{pkg: "maps", target: "Using"}, true
	case "InForce":
		return globalFuncSpec{pkg: "scope", target: "InForce"}, true
	case "AsWritten":
		return globalFuncSpec{pkg: "proof", target: "AsWritten"}, true
	case "TestFolder":
		return globalFuncSpec{pkg: "fstest", target: "TestFolder"}, true
	case "TestFolderHandle":
		return globalFuncSpec{pkg: "fstest", target: "TestFolderHandle"}, true
	case "NewLiveScheduler":
		return globalFuncSpec{pkg: "time", target: "NewLiveScheduler"}, true
	}
	return globalFuncSpec{}, false
}

// GlobalFuncsSource rewrites every universe/prelude global function call in the
// standalone Ergo source src to its qualified std-package form, adding any
// missing imports in sorted order. Calls resolved to user-declared functions or
// variables (PRELUDE SHADOW) are left untouched. When src is already formatted,
// the returned text is formatted to a fixpoint.
func GlobalFuncsSource(src, name string) (string, []GlobalFuncRewrite, error) {
	pkg, _ := check.CheckSource(src)
	if pkg == nil || pkg.Info == nil {
		return src, nil, nil
	}
	files := pkg.Files()
	if len(files) != 1 {
		return src, nil, nil
	}
	out, rewrites, err := rewriteGlobalFuncFiles(map[string]string{name: src}, []string{name}, files, pkg.Info, "")
	if err != nil {
		return src, nil, err
	}
	return out[name], rewrites, nil
}

// GlobalFuncsPackage rewrites every universe/prelude global function call in
// the package at dir over texts (keyed by absolute file path). Rewrites are
// kept only when the rewritten package checks with the same diagnostics.
func GlobalFuncsPackage(dir string, texts map[string]string) (map[string]string, []GlobalFuncRewrite, error) {
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
	paths := matchGlobalFuncFiles(abs, texts, files)
	if len(paths) != len(files) {
		return texts, nil, fmt.Errorf("%s: checked file count %d does not match texts", abs, len(files))
	}
	out, rewrites, err := rewriteGlobalFuncFiles(texts, paths, files, pkg.Info, "")
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
		return texts, nil, fmt.Errorf("%s: rewriting global function calls would change the check verdict (before %v, after %v)", abs, before, after)
	}
	return out, rewrites, nil
}

// RunGlobalFuncs rewrites universe/prelude global function calls across
// opts.Paths (module packages and standalone .ergo files) and opts.StdRoot (if
// set). Generated folders, gostd, and unadmitted folders (e.g. testdata) are
// skipped when walking a parent folder. Files are written only when opts.Write.
func RunGlobalFuncs(opts Options) (GlobalFuncsReport, error) {
	rep := GlobalFuncsReport{Files: map[string]string{}}
	stdRoot := ""
	if opts.StdRoot != "" {
		abs, err := filepath.Abs(opts.StdRoot)
		if err != nil {
			return rep, err
		}
		stdRoot = abs
		if err := runGlobalFuncsStd(&rep, stdRoot); err != nil {
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
		out, rewrites, err := GlobalFuncsPackage(abs, texts)
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
		out, rewrites, err := GlobalFuncsSource(src, abs)
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
			rep.Errs = append(rep.Errs, fmt.Sprintf("%s: rewriting global function calls would change the check verdict (before %v, after %v)", abs, before, after))
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
		if err := walkGlobalFuncs(abs, oneFile, func(mod string) error {
			if stdRoot != "" && mod == stdRoot {
				return nil
			}
			if stdRoot == "" && isStdTreeRoot(mod) {
				stdRoot = mod
				doneDir[stdRoot] = true
				for _, p := range stdlib.Packages() {
					doneDir[filepath.Join(stdRoot, filepath.FromSlash(p))] = true
				}
				return runGlobalFuncsStd(&rep, stdRoot)
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

func isStdTreeRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "prelude.ergo")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "builtin.ergo")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "core", "core.ergo"))
	return err == nil
}

func runGlobalFuncsStd(rep *GlobalFuncsReport, stdRoot string) error {
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
		if pkgPath == "core" || pkgPath == "option" || pkgPath == "collections" {
			a, err := sketchfront.CheckDirOptions(dir, texts, check.Options{})
			if err != nil {
				rep.Errs = append(rep.Errs, dir+": "+err.Error())
				continue
			}
			_, pkg, cfiles, ok := a.RootPackage()
			if !ok || pkg == nil || pkg.Info == nil {
				continue
			}
			matched := matchGlobalFuncFiles(dir, texts, cfiles)
			out, rewrites, err := rewriteGlobalFuncFiles(texts, matched, cfiles, pkg.Info, pkgPath)
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
				rep.Errs = append(rep.Errs, fmt.Sprintf("%s: rewriting global function calls would change the check verdict (before %v, after %v)", dir, before, after))
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
		pkg, beforeDiags := check.StdFixture(pkgPath, parsed)
		if pkg == nil || pkg.Info == nil {
			continue
		}
		out, rewrites, err := rewriteGlobalFuncFiles(texts, files, parsed, pkg.Info, pkgPath)
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
		if !slices.Equal(globalFuncDiagKeys(beforeDiags), globalFuncDiagKeys(afterDiags)) {
			rep.Errs = append(rep.Errs, fmt.Sprintf("%s: rewriting global function calls would change the check verdict (before %v, after %v)", dir, globalFuncDiagKeys(beforeDiags), globalFuncDiagKeys(afterDiags)))
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

func globalFuncDiagKeys(ds []check.Diagnostic) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Code+" "+d.Message)
	}
	slices.Sort(out)
	return out
}

func walkGlobalFuncs(root string, file func(string) error, module func(string) error) error {
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

func matchGlobalFuncFiles(dir string, texts map[string]string, files []*syntax.File) []string {
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

type neededImport struct {
	path  string
	alias string
}

func (n neededImport) line() string {
	if n.alias != "" {
		return "import " + n.alias + " " + n.path
	}
	return "import " + n.path
}

func packageDeclaredNames(files []*syntax.File) map[string]bool {
	names := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.FuncDecl:
				if d.Name != nil && d.Name.Name != "_" {
					names[d.Name.Name] = true
				}
			case *syntax.TypeDecl:
				if d.Name != nil && d.Name.Name != "_" {
					names[d.Name.Name] = true
				}
			case *syntax.VarDecl:
				for _, sp := range d.Specs {
					for _, id := range sp.Names {
						if id != nil && id.Name != "_" {
							names[id.Name] = true
						}
					}
				}
			case *syntax.ContextDecl:
				if d.Name != nil && d.Name.Name != "_" {
					names[d.Name.Name] = true
				}
			}
		}
	}
	return names
}

func pickImportAlias(base string, used map[string]bool) string {
	cand := "std" + base
	if !used[cand] {
		return cand
	}
	for i := 2; ; i++ {
		next := fmt.Sprintf("std%s%d", base, i)
		if !used[next] {
			return next
		}
	}
}

func rewriteGlobalFuncFiles(texts map[string]string, filePaths []string, files []*syntax.File, info *check.Info, currentStdPkg string) (map[string]string, []GlobalFuncRewrite, error) {
	out := make(map[string]string, len(texts))
	for k, v := range texts {
		out[k] = v
	}
	pkgNames := packageDeclaredNames(files)
	var allRewrites []GlobalFuncRewrite
	for i, f := range files {
		p := filePaths[i]
		src := texts[p]
		eds, needImports, rewrites := collectGlobalFuncEdits(src, p, f, info, currentStdPkg, pkgNames)
		if len(eds) == 0 {
			continue
		}
		impEds := sortedImportEdits(src, f, needImports)
		eds = append(eds, impEds...)
		next := applyGlobalFuncEdits(src, eds)
		if _, err := syntax.Parse(next); err != nil {
			return texts, nil, fmt.Errorf("%s: rewritten global-func source does not parse: %w", p, err)
		}
		if formatted, err := hostsyntax.Source(src); err == nil && formatted == src {
			if next, err = hostsyntax.Source(next); err != nil {
				return texts, nil, fmt.Errorf("%s: rewritten global-func source does not format: %w", p, err)
			}
		}
		out[p] = next
		allRewrites = append(allRewrites, rewrites...)
	}
	return out, allRewrites, nil
}

// collectGlobalFuncEdits finds every bare call to one of the 10 universe/prelude
// global functions in f, replacing only the callee identifier span (preserving
// any `[T]` type arguments untouched) and recording which std packages must be
// imported. When a newly imported package's default name clashes with a
// package-level declaration or an existing import in f, it picks a non-clashing
// alias (`import std<pkg> <pkg>`).
func collectGlobalFuncEdits(src, filePath string, f *syntax.File, info *check.Info, currentStdPkg string, pkgNames map[string]bool) ([]edit, []neededImport, []GlobalFuncRewrite) {
	imported := fileImports(f)
	usedQuals := map[string]bool{}
	for name := range pkgNames {
		usedQuals[name] = true
	}
	for _, qual := range imported {
		usedQuals[qual] = true
	}
	chosen := map[string]neededImport{}
	var eds []edit
	var rewrites []GlobalFuncRewrite
	seen := map[[2]int]bool{}

	// Track enclosing FuncDecl name so wrapper definitions like
	// slices.WithCapacity -> MakeSlice and maps.Using -> MakeMap never rewrite
	// their own forwarding body into self-recursion.
	for _, decl := range f.Decls {
		enclosingFn := ""
		if fd, ok := decl.(*syntax.FuncDecl); ok && fd.Name != nil {
			enclosingFn = fd.Name.Name
		}
		syntax.Inspect(decl, func(n syntax.Node) bool {
			call, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			id, spec, ok := resolvedGlobalFuncCall(call, info)
			if !ok {
				return true
			}
			if currentStdPkg == spec.pkg && enclosingFn == spec.target {
				return true
			}
			start, end := int(id.Pos()), int(id.End())
			if start < 0 || end > len(src) || start >= end || src[start:end] != id.Name {
				return true
			}
			key := [2]int{start, end}
			if seen[key] {
				return true
			}
			seen[key] = true

			var replacement string
			switch {
			case currentStdPkg == spec.pkg:
				if spec.target == id.Name {
					return true
				}
				replacement = spec.target
			default:
				if qual, have := imported[spec.pkg]; have {
					replacement = qual + "." + spec.target
				} else {
					ni, ok := chosen[spec.pkg]
					if !ok {
						base := path.Base(spec.pkg)
						if usedQuals[base] {
							alias := pickImportAlias(base, usedQuals)
							ni = neededImport{path: spec.pkg, alias: alias}
							usedQuals[alias] = true
						} else {
							ni = neededImport{path: spec.pkg}
							usedQuals[base] = true
						}
						chosen[spec.pkg] = ni
					}
					qual := path.Base(ni.path)
					if ni.alias != "" {
						qual = ni.alias
					}
					replacement = qual + "." + spec.target
				}
			}

			eds = append(eds, edit{from: start, to: end, text: replacement})
			line, col := f.Position(id.Pos())
			rewrites = append(rewrites, GlobalFuncRewrite{
				File:   filePath,
				Line:   line,
				Column: col,
				Before: src[start:end],
				After:  replacement,
			})
			return true
		})
	}

	var need []neededImport
	for _, ni := range chosen {
		need = append(need, ni)
	}
	slices.SortFunc(need, func(a, b neededImport) int {
		return strings.Compare(a.path, b.path)
	})
	return eds, need, rewrites
}

// resolvedGlobalFuncCall returns the callee identifier and target spec if call
// is an unqualified call resolving to one of the universe/prelude global
// functions (never a user shadow or an already-qualified call).
func resolvedGlobalFuncCall(call *syntax.CallExpr, info *check.Info) (*syntax.Ident, globalFuncSpec, bool) {
	if call == nil || info == nil {
		return nil, globalFuncSpec{}, false
	}
	fun := unparenGF(call.Fun)
	if ix, ok := fun.(*syntax.IndexExpr); ok {
		fun = unparenGF(ix.X)
	}
	id, ok := fun.(*syntax.Ident)
	if !ok {
		return nil, globalFuncSpec{}, false
	}
	spec, ok := globalFuncFor(id.Name)
	if !ok {
		return nil, globalFuncSpec{}, false
	}
	c := info.Calls[call]
	if c == nil {
		return nil, globalFuncSpec{}, false
	}
	if spec.builtinKind != 0 {
		if c.Kind != spec.builtinKind {
			return nil, globalFuncSpec{}, false
		}
		b, ok := info.Uses[id].(*check.Builtin)
		if !ok || b.Name != id.Name {
			return nil, globalFuncSpec{}, false
		}
		return id, spec, true
	}
	if c.Kind != check.CallFunc || c.Func == nil || c.Func.Name != id.Name || c.Func.Pkg == nil || c.Func.Pkg.Path != stdlib.PreludePath {
		return nil, globalFuncSpec{}, false
	}
	u, ok := info.Uses[id].(*check.Func)
	if !ok || u != c.Func {
		return nil, globalFuncSpec{}, false
	}
	return id, spec, true
}

// fileImports maps each imported non-go package path in f to its local
// qualifier name (alias when ImportNamed, else path.Base).
func fileImports(f *syntax.File) map[string]string {
	out := map[string]string{}
	for _, d := range f.Decls {
		im, ok := d.(*syntax.ImportDecl)
		if !ok {
			continue
		}
		for _, s := range im.Specs {
			if s.Kind == syntax.ImportGo || s.Kind == syntax.ImportDot || s.Path == nil {
				continue
			}
			p := strings.Trim(s.Path.Value, `"`)
			qual := path.Base(p)
			if s.Kind == syntax.ImportNamed && s.Name != nil && s.Name.Name != "_" {
				qual = s.Name.Name
			}
			out[p] = qual
		}
	}
	return out
}

// sortedImportEdits builds minimal insertions adding each missing import in
// needed to f's import block in sorted order without duplicating existing
// imports.
func sortedImportEdits(src string, f *syntax.File, needed []neededImport) []edit {
	if len(needed) == 0 {
		return nil
	}
	have := fileImports(f)
	seen := map[string]bool{}
	var missing []neededImport
	for _, n := range needed {
		if _, ok := have[n.path]; !ok && !seen[n.path] {
			seen[n.path] = true
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.SortFunc(missing, func(a, b neededImport) int {
		return strings.Compare(a.path, b.path)
	})

	type impEntry struct {
		path string
		decl *syntax.ImportDecl
	}
	var ergoImps []impEntry
	var firstGoImp *syntax.ImportDecl
	var firstNonImp syntax.Decl
	for _, d := range f.Decls {
		im, ok := d.(*syntax.ImportDecl)
		if !ok {
			if firstNonImp == nil {
				firstNonImp = d
			}
			continue
		}
		if len(im.Specs) > 0 && im.Specs[0].Kind == syntax.ImportGo {
			if firstGoImp == nil {
				firstGoImp = im
			}
			continue
		}
		if len(im.Specs) > 0 && im.Specs[0].Path != nil {
			ergoImps = append(ergoImps, impEntry{
				path: strings.Trim(im.Specs[0].Path.Value, `"`),
				decl: im,
			})
		}
	}

	if len(ergoImps) > 0 {
		// Group insertions by insertion offset so multiple imports going at the
		// same slot stay in sorted order.
		before := map[int][]string{}
		var afterLast []string
		for _, n := range missing {
			placed := false
			for _, e := range ergoImps {
				if n.path < e.path {
					at := int(e.decl.Pos())
					before[at] = append(before[at], n.line()+"\n")
					placed = true
					break
				}
			}
			if !placed {
				afterLast = append(afterLast, "\n"+n.line())
			}
		}
		var eds []edit
		for at, lines := range before {
			eds = append(eds, edit{from: at, to: at, text: strings.Join(lines, "")})
		}
		if len(afterLast) > 0 {
			end := int(ergoImps[len(ergoImps)-1].decl.End())
			eds = append(eds, edit{from: end, to: end, text: strings.Join(afterLast, "")})
		}
		return eds
	}

	var lines []string
	for _, n := range missing {
		lines = append(lines, n.line())
	}
	if firstGoImp != nil {
		at := int(firstGoImp.Pos())
		return []edit{{from: at, to: at, text: strings.Join(lines, "\n") + "\n"}}
	}
	if firstNonImp != nil {
		at := declStartWithDoc(firstNonImp)
		return []edit{{from: at, to: at, text: strings.Join(lines, "\n") + "\n\n"}}
	}
	return []edit{{from: len(src), to: len(src), text: strings.Join(lines, "\n") + "\n"}}
}

func declStartWithDoc(d syntax.Decl) int {
	var doc *syntax.CommentGroup
	switch d := d.(type) {
	case *syntax.FuncDecl:
		doc = d.Doc
	case *syntax.TypeDecl:
		doc = d.Doc
	case *syntax.VarDecl:
		doc = d.Doc
	case *syntax.ContextDecl:
		doc = d.Doc
	case *syntax.TestDecl:
		doc = d.Doc
	case *syntax.LawDecl:
		doc = d.Doc
	case *syntax.ImportDecl:
		doc = d.Doc
	}
	if doc != nil && doc.Pos().IsValid() {
		return int(doc.Pos())
	}
	return int(d.Pos())
}

func unparenGF(x syntax.Expr) syntax.Expr {
	for {
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}

func applyGlobalFuncEdits(src string, eds []edit) string {
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
