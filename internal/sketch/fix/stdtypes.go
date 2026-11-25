package fix

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"ergo.local/prototype/internal/genmark"
	"ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// StdTypeRewrite records one prelude type reference rewritten to its qualified
// std-package home (NO-PRELUDE §5.2, Unit 8).
type StdTypeRewrite struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// StdTypesReport is RunStdTypes's result: the new text of each changed file,
// every rewritten type reference, and any package/file errors encountered.
type StdTypesReport struct {
	Files    map[string]string
	Rewrites []StdTypeRewrite
	Errs     []string
}

// Text formats one summary line per changed file, followed by any errors.
func (r StdTypesReport) Text() string {
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
		fmt.Fprintf(&b, "%s: rewrote %d std type references\n", f, count[f])
	}
	for _, e := range r.Errs {
		fmt.Fprintf(&b, "%s\n", e)
	}
	return b.String()
}

type stdTypeSpec struct {
	pkg    string
	target string
}

// stdTypeFor maps each Unit-8 prelude type name to its destination std package
// and exported name (NO-PRELUDE §5.2, §6 steps 6a–6b). Machines (Unit 9) and
// Option (Unit 10) are intentionally excluded from this pass.
func stdTypeFor(name string) (stdTypeSpec, bool) {
	switch name {
	case "Hashing", "Structural":
		return stdTypeSpec{pkg: "maps", target: name}, true
	case "Task":
		return stdTypeSpec{pkg: "task", target: name}, true
	case "ordered":
		return stdTypeSpec{pkg: "cmp", target: "Ordered"}, true
	case "number":
		return stdTypeSpec{pkg: "cmp", target: "Number"}, true
	case "Ambient":
		return stdTypeSpec{pkg: "os", target: name}, true
	case "TestAmbient", "TestDirectory", "TestLoopback":
		return stdTypeSpec{pkg: "fstest", target: name}, true
	case "Random":
		return stdTypeSpec{pkg: "rand", target: name}, true
	case "Scheduler", "AlarmClock", "LiveScheduler":
		return stdTypeSpec{pkg: "time", target: name}, true
	case "Writer", "Reader",
		"WireWrongKind", "WireNull", "WireBool", "WireNumber", "WireString",
		"WireList", "WireObject", "WireEnd", "WireKind",
		"WireMissingField", "WireUnknownTag",
		"WireOmittable", "WireNullable", "WireBare", "WireNumberText", "WireMembers":
		return stdTypeSpec{pkg: "codec", target: name}, true
	case "Console":
		return stdTypeSpec{pkg: "io", target: name}, true
	case "Range", "Seq", "Never", "Context", "CallSite", "Closer", "Keeping", "Unsatisfied", "NoMatch":
		return stdTypeSpec{pkg: "core", target: name}, true
	}
	return stdTypeSpec{}, false
}

// StdTypesSource rewrites every Unit-8 prelude type reference in standalone
// Ergo source src to its qualified std-package form, adding any missing
// imports in sorted order. References resolved to user-declared types, type
// parameters, or locals (PRELUDE SHADOW) are left untouched. When src is
// already formatted, the returned text is formatted to a fixpoint.
func StdTypesSource(src, name string) (string, []StdTypeRewrite, error) {
	pkg, _ := check.CheckSource(src)
	if pkg == nil || pkg.Info == nil {
		return src, nil, nil
	}
	files := pkg.Files()
	if len(files) != 1 {
		return src, nil, nil
	}
	out, rewrites, err := rewriteStdTypeFiles(map[string]string{name: src}, []string{name}, files, pkg.Info, "")
	if err != nil {
		return src, nil, err
	}
	return out[name], rewrites, nil
}

// StdTypesPackage rewrites every Unit-8 prelude type reference in the package
// at dir over texts (keyed by absolute file path). Rewrites are kept only when
// the rewritten package checks with the same diagnostics.
func StdTypesPackage(dir string, texts map[string]string) (map[string]string, []StdTypeRewrite, error) {
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
	out, rewrites, err := rewriteStdTypeFiles(texts, paths, files, pkg.Info, "")
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
		return texts, nil, fmt.Errorf("%s: rewriting std type references would change the check verdict (before %v, after %v)", abs, before, after)
	}
	return out, rewrites, nil
}

// RunStdTypes rewrites Unit-8 prelude type references across opts.Paths
// (module packages and standalone .ergo files) and opts.StdRoot (if set).
// Generated folders, gostd, and unadmitted folders (e.g. testdata) are skipped
// when walking a parent folder. Files are written only when opts.Write.
func RunStdTypes(opts Options) (StdTypesReport, error) {
	rep := StdTypesReport{Files: map[string]string{}}
	stdRoot := ""
	if opts.StdRoot != "" {
		abs, err := filepath.Abs(opts.StdRoot)
		if err != nil {
			return rep, err
		}
		stdRoot = abs
		if err := runStdTypesStd(&rep, stdRoot); err != nil {
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
		out, rewrites, err := StdTypesPackage(abs, texts)
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
		out, rewrites, err := StdTypesSource(src, abs)
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
			rep.Errs = append(rep.Errs, fmt.Sprintf("%s: rewriting std type references would change the check verdict (before %v, after %v)", abs, before, after))
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
				return runStdTypesStd(&rep, stdRoot)
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

func runStdTypesStd(rep *StdTypesReport, stdRoot string) error {
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
		if isBootstrapStdPath(pkgPath) {
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
			out, rewrites, err := rewriteStdTypeFiles(texts, matched, cfiles, pkg.Info, pkgPath)
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
				rep.Errs = append(rep.Errs, fmt.Sprintf("%s: rewriting std type references would change the check verdict (before %v, after %v)", dir, before, after))
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
		out, rewrites, err := rewriteStdTypeFiles(texts, files, parsed, pkg.Info, pkgPath)
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
			rep.Errs = append(rep.Errs, fmt.Sprintf("%s: rewriting std type references would change the check verdict (before %v, after %v)", dir, globalFuncDiagKeys(beforeDiags), globalFuncDiagKeys(afterDiags)))
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

func isBootstrapStdPath(pkgPath string) bool {
	return pkgPath == "core" || pkgPath == "option" || pkgPath == "collections"
}

// stdTransitiveDeps returns, for every std package p, the set of std packages
// reachable from p's imports. Used inside std packages to skip rewrites whose
// added import would introduce a std import cycle during the two-spelling
// window (for example, time referencing Task while task imports time).
var stdTransitiveDeps = sync.OnceValue(func() map[string]map[string]bool {
	direct := map[string][]string{}
	for _, p := range stdlib.Packages() {
		files, ok := stdlib.Package(p)
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, src := range files {
			f, err := syntax.Parse(src)
			if err != nil {
				continue
			}
			for imp := range fileImports(f) {
				if !seen[imp] {
					seen[imp] = true
					direct[p] = append(direct[p], imp)
				}
			}
		}
	}
	reach := map[string]map[string]bool{}
	for _, p := range stdlib.Packages() {
		vis := map[string]bool{}
		stack := slices.Clone(direct[p])
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if vis[cur] {
				continue
			}
			vis[cur] = true
			stack = append(stack, direct[cur]...)
		}
		reach[p] = vis
	}
	return reach
})

func wouldCycleStd(currentStdPkg, targetPkg string) bool {
	if currentStdPkg == "" || currentStdPkg == targetPkg {
		return false
	}
	if isBootstrapStdPath(currentStdPkg) {
		return true
	}
	return stdTransitiveDeps()[targetPkg][currentStdPkg]
}

func rewriteStdTypeFiles(texts map[string]string, filePaths []string, files []*syntax.File, info *check.Info, currentStdPkg string) (map[string]string, []StdTypeRewrite, error) {
	out := make(map[string]string, len(texts))
	for k, v := range texts {
		out[k] = v
	}
	pkgNames := packageDeclaredNames(files)
	var allRewrites []StdTypeRewrite
	for i, f := range files {
		p := filePaths[i]
		src := texts[p]
		eds, needImports, rewrites := collectStdTypeEdits(src, p, f, info, currentStdPkg, pkgNames)
		if len(eds) == 0 {
			continue
		}
		impEds := sortedImportEdits(src, f, needImports)
		eds = append(eds, impEds...)
		next := applyGlobalFuncEdits(src, eds)
		if _, err := syntax.Parse(next); err != nil {
			return texts, nil, fmt.Errorf("%s: rewritten std-type source does not parse: %w", p, err)
		}
		if formatted, err := hostsyntax.Source(src); err == nil && formatted == src {
			if next, err = hostsyntax.Source(next); err != nil {
				return texts, nil, fmt.Errorf("%s: rewritten std-type source does not format: %w", p, err)
			}
		}
		out[p] = next
		allRewrites = append(allRewrites, rewrites...)
	}
	return out, allRewrites, nil
}

// collectStdTypeEdits finds every unqualified reference to a Unit-8 prelude
// type in f (type positions, rows, capability calls, context literals,
// implements clauses, composite literals, patterns, and generic constraints),
// replacing only the type identifier span (preserving `[T]` type arguments and
// `.Method` selectors) and recording missing std imports.
func collectStdTypeEdits(src, filePath string, f *syntax.File, info *check.Info, currentStdPkg string, pkgNames map[string]bool) ([]edit, []neededImport, []StdTypeRewrite) {
	imported := fileImports(f)
	usedQuals := map[string]bool{}
	for name := range pkgNames {
		usedQuals[name] = true
	}
	for _, qual := range imported {
		usedQuals[qual] = true
	}

	// Mark every SelectorExpr.Sel identifier so already-qualified names
	// (io.Console, codec.WireWrongKind, collections.Seq) and field/method
	// selectors are never touched.
	selectorSels := map[*syntax.Ident]bool{}
	syntax.Inspect(f, func(n syntax.Node) bool {
		if sel, ok := n.(*syntax.SelectorExpr); ok && sel.Sel != nil {
			selectorSels[sel.Sel] = true
		}
		return true
	})

	// Track local variables/parameters per declaration so if a declaration has a
	// local named the same as a candidate import qualifier (e.g. `time int` in a
	// function calling `Scheduler.Sleep`), we skip rewriting inside that scope
	// instead of shadowing the newly-added import.
	chosen := map[string]neededImport{}
	var eds []edit
	var rewrites []StdTypeRewrite
	seen := map[[2]int]bool{}

	for _, decl := range f.Decls {
		if _, isImp := decl.(*syntax.ImportDecl); isImp {
			continue
		}
		localShadow := declLocalNames(decl, info)
		syntax.Inspect(decl, func(n syntax.Node) bool {
			id, ok := n.(*syntax.Ident)
			if !ok || selectorSels[id] {
				return true
			}
			spec, ok := resolvedStdTypeIdent(id, info)
			if !ok {
				return true
			}
			if wouldCycleStd(currentStdPkg, spec.pkg) {
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

			var replacement string
			switch {
			case currentStdPkg == spec.pkg:
				if spec.target == id.Name {
					return true
				}
				replacement = spec.target
			default:
				qual, have := imported[spec.pkg]
				if have {
					if localShadow[qual] {
						return true
					}
					replacement = qual + "." + spec.target
				} else {
					ni, ok := chosen[spec.pkg]
					if !ok {
						base := path.Base(spec.pkg)
						if usedQuals[base] {
							alias := pickImportAlias(base, usedQuals)
							if localShadow[alias] {
								return true
							}
							ni = neededImport{path: spec.pkg, alias: alias}
							usedQuals[alias] = true
						} else {
							if localShadow[base] {
								return true
							}
							ni = neededImport{path: spec.pkg}
							usedQuals[base] = true
						}
						chosen[spec.pkg] = ni
					}
					qual = path.Base(ni.path)
					if ni.alias != "" {
						qual = ni.alias
					}
					if localShadow[qual] {
						return true
					}
					replacement = qual + "." + spec.target
				}
			}

			seen[key] = true
			eds = append(eds, edit{from: start, to: end, text: replacement})
			line, col := f.Position(id.Pos())
			rewrites = append(rewrites, StdTypeRewrite{
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

// declLocalNames collects local variable, parameter, and type-parameter names
// defined inside decl (excluding decl's own top-level name) so a newly-added
// package qualifier never collides with a local binding inside decl.
func declLocalNames(decl syntax.Decl, info *check.Info) map[string]bool {
	out := map[string]bool{}
	var declName *syntax.Ident
	switch d := decl.(type) {
	case *syntax.FuncDecl:
		declName = d.Name
	case *syntax.TypeDecl:
		declName = d.Name
	case *syntax.ContextDecl:
		declName = d.Name
	case *syntax.TestDecl:
		declName = d.Name
	case *syntax.LawDecl:
		declName = d.Name
	}
	syntax.Inspect(decl, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Ident)
		if !ok || id == declName || id.Name == "_" {
			return true
		}
		if obj := info.Defs[id]; obj != nil {
			switch obj.(type) {
			case *check.Var, *check.TypeParam:
				out[id.Name] = true
			}
		}
		return true
	})
	return out
}

// resolvedStdTypeIdent returns the target std-package spec when id resolves to
// a canonical Unit-8 prelude type (or collections.Seq re-exported in prelude),
// never a user-declared type, local variable, or type parameter.
func resolvedStdTypeIdent(id *syntax.Ident, info *check.Info) (stdTypeSpec, bool) {
	if id == nil || info == nil {
		return stdTypeSpec{}, false
	}
	spec, ok := stdTypeFor(id.Name)
	if !ok {
		return stdTypeSpec{}, false
	}
	tn := check.TypeNameOf(info.Uses[id])
	if tn == nil || tn.Name != id.Name || tn.Pkg == nil {
		return stdTypeSpec{}, false
	}
	if tn.Pkg.Path == stdlib.PreludePath {
		return spec, true
	}
	if tn.Name == "Seq" && tn.Pkg.Path == "collections" {
		return spec, true
	}
	return stdTypeSpec{}, false
}
