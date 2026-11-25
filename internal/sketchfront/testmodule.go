package sketchfront

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync/atomic"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

// One load and one test binary for the packages of a module (TEST-one).
// `ergo test DIR/...` used to load, check, lower and link each package's
// test build alone: each package paid std's import, the check of every
// package it imports and a Go link. CheckPackages reads the module once and
// checks each package once. A package that another package imports is
// checked under its import path, and that one check is also its own test
// build's root. Its name is a label, not an input of the check: the test
// report prints the root's types and laws bare, and where a text qualifies
// a root type (a Script's defect, an evaluation's reason) the lowering
// (lower.LowerTestModule) and the evaluation (Analysis.Evaluate) name the
// root main while they run, as a load of it alone names it. A package that
// no package imports is checked as main, the root of its own program.
// BuildTests lowers every root's test build into one Go module
// (lower.LowerTestModule) and links it once.
//
// The builder rule (check/builders.go, rule 2) makes the package whose path
// is P+"test" a test package of P. The loader gives a package that role
// from its import path whether it is checked as main or under that path
// (check.CheckTest, fu-audit), so each package is checked once.
//
// Each root's Analysis is the one CheckDir gives for that folder: the same
// root, the same imports in the same order, and the same texts, so its
// revision, its receipts and its report do not change. A load that cannot
// promise that is refused with ErrNotShared, and the caller tests each
// package alone, as before: a diagnostic anywhere, a module binding
// (ergo.bind; the binding a check reads is recorded per program), or a
// folder outside the module.

// ErrNotShared refuses a shared load; the caller loads each package alone.
var ErrNotShared = errors.New("sketchfront: the packages do not share one load")

// work counts the front end's work, for the guard that a shared run loads
// the module once, checks each package once and links once (WorkDone).
var work struct{ loads, checks, links atomic.Int64 }

// Work is a count of the front end's work since the process started.
type Work struct {
	// Loads are the module loads: CheckDir and CheckPackages calls.
	Loads int64
	// Checks are the checker's runs over one package.
	Checks int64
	// Links are the go command's builds.
	Links int64
}

// WorkDone is the work counted so far.
func WorkDone() Work {
	return Work{Loads: work.loads.Load(), Checks: work.checks.Load(), Links: work.links.Load()}
}

// CheckPackages checks the package folders dirs, absolute folders of one
// module, with one load, and returns the Analysis of each as its own
// program, in the order of dirs.
func CheckPackages(dirs []string, platform gobind.Platform) ([]Analysis, error) {
	return CheckPackagesFS(hostFS{}, dirs, platform)
}

// CheckPackagesFS is CheckPackages reading through fsys (a warm request's
// Recorder, which lists every input the load read).
func CheckPackagesFS(fsys FS, dirs []string, platform gobind.Platform) ([]Analysis, error) {
	if len(dirs) == 0 {
		return nil, fmt.Errorf("sketchfront: no package to check")
	}
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			return nil, fmt.Errorf("sketchfront: the package folder %s is not absolute and clean", dir)
		}
	}
	l := newLoader(fsys, platform, dirs[0], moduleCache())
	work.loads.Add(1)
	found, err := l.findModule(dirs[0])
	if err != nil {
		return nil, err
	}
	if !found || l.module == "" || len(l.diags) > 0 {
		return nil, ErrNotShared
	}
	// ergo.mod, go.mod and go.sum: every program of the module reads them.
	moduleTexts := maps.Clone(l.texts)
	selected := make([]*loadedPackage, len(dirs))
	for i, dir := range dirs {
		rel, err := filepath.Rel(l.root, dir)
		if err != nil || !filepath.IsLocal(rel) && rel != "." || l.nestedModule(dir, nil) != "" {
			return nil, ErrNotShared
		}
		path := l.module
		if rel != "." {
			path += "/" + filepath.ToSlash(rel)
		}
		p, err := l.read(path, dir, nil)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, ErrNotShared
		}
		selected[i] = p
	}
	for _, p := range selected {
		if l.state[p] == unvisited {
			if err := l.visit(p, nil); err != nil {
				return nil, err
			}
		}
	}
	if len(l.diags) > 0 || l.dependencyPin() != nil {
		return nil, ErrNotShared
	}
	// The std snapshot, as a load of one package installs it before its
	// check (checkDirFull): without it the shared check read std from
	// source, and a later load of one package in the same process
	// exported the snapshot: 3.2 CPU-s for `ergo test --emitted --json`
	// of examples/sudoku, 1.5 with it (gate-speed GS31b).
	prepareStd(nil)
	imported := map[*loadedPackage]bool{}
	for _, p := range l.order {
		for _, imp := range p.imports {
			imported[l.pkgs[imp.path]] = true
		}
	}
	for _, p := range l.order {
		if imported[p] && !l.checkShared(p, p.path) {
			return nil, ErrNotShared
		}
	}
	out := make([]Analysis, len(dirs))
	for i, p := range selected {
		root := p
		switch {
		case !imported[p]:
			// Its one check: no package imports it, so it is main.
			if !l.checkShared(p, "main") {
				return nil, ErrNotShared
			}
		}
		imports := l.closure(p)
		texts := maps.Clone(moduleTexts)
		for _, q := range append(imports[:len(imports):len(imports)], root) {
			for _, f := range q.files {
				texts[f.rel] = f.text
			}
			for name, data := range q.assets {
				texts[l.relIn(q.mod, filepath.Join(q.dir, filepath.FromSlash(name)))] = string(data)
			}
		}
		prog := &program{root: root, imports: imports, texts: texts, module: l.module, modRoot: l.root, dir: l.root}
		if l.goMod != nil {
			// go.mod merged with the runtime's requirement and replacement,
			// as a package loaded alone emits it.
			prog.goModule = l.goModule
		}
		out[i] = Analysis{program: prog, pkg: root.pkg}
	}
	if len(l.bindings) > 0 {
		return nil, ErrNotShared
	}
	if l.coversModule(dirs) {
		// Only a load of the whole module judges (check.ReportUnfed); a
		// load of part of it decides nothing, and the bindings it leaves
		// are neither run nor skipped.
		l.reportUnfed(selected, out)
	}
	return out, nil
}

// coversModule reports whether dirs are every package of the module, so
// that each package that imports one of them is checked in this load.
func (l *loader) coversModule(dirs []string) bool {
	all, err := DiscoverPackages(l.root)
	if err != nil {
		return false
	}
	for _, p := range all {
		if !slices.Contains(dirs, p.Dir) {
			return false
		}
	}
	return true
}

// reportUnfed judges, over the packages of the module's own that this
// load checked, the law bindings their checks left to the module
// (check.ReportUnfed; types.md, Laws 10): a binding no named context of
// the module feeds is a diagnostic of the package that holds it, the
// root of its analysis in out. A package of a required module is no
// input: its contexts feed nothing here.
func (l *loader) reportUnfed(selected []*loadedPackage, out []Analysis) {
	var own []*check.Package
	for _, p := range l.order {
		if p.mod == nil && p.pkg != nil {
			own = append(own, p.pkg)
		}
	}
	ds := check.ReportUnfed(own)
	for i, p := range selected {
		for _, d := range ds[p.pkg] {
			out[i].Diagnostics = append(out[i].Diagnostics, report.Diagnostic{
				Code:    d.Code,
				File:    p.files[d.File].rel,
				Message: d.Message,
				Span:    report.Span{Offset: d.Offset, Length: d.Length, Line: d.Line, Column: d.Column},
			})
		}
	}
}

// checkShared checks the package p as path, as checkAll checks it, and
// reports whether it has no diagnostic.
func (l *loader) checkShared(p *loadedPackage, path string) bool {
	files := make([]*syntax.File, len(p.files))
	for k, f := range p.files {
		files[k] = f.syntax
	}
	pkg, ds := check.CheckTestOptions(path, files, l, l.testOf(p), l.opts)
	work.checks.Add(1)
	p.pkg = pkg
	return len(ds) == 0
}

// closure is what visit gives a load of root alone: the local packages
// root reaches, depth first in import order, each after its imports.
func (l *loader) closure(root *loadedPackage) []*loadedPackage {
	seen := map[*loadedPackage]bool{}
	var order []*loadedPackage
	var walk func(*loadedPackage)
	walk = func(p *loadedPackage) {
		seen[p] = true
		for _, imp := range p.imports {
			if q := l.pkgs[imp.path]; q != nil && !seen[q] {
				walk(q)
			}
		}
		order = append(order, p)
	}
	walk(root)
	return order[:len(order)-1]
}

// BuildTests links one test binary to req.Output for the analyses of one
// shared load (CheckPackages): run with lower.TestPackageFlag K first, it
// runs the tests of analyses[K] as that package's own test binary does.
// req.Mode and req.Source are not read; the module is keyed by every
// source, dependency and selected package. req.IR lowers it with the IR
// bodies (lower.LowerIRTestModule), as Build lowers a package's own. An analysis that did not check
// is refused; a lowering's refusal is returned as an error, and the caller
// builds each package alone to report it where it belongs.
func BuildTests(analyses []Analysis, req BuildRequest) (build.Module, error) {
	if len(analyses) == 0 {
		return build.Module{}, fmt.Errorf("sketchfront: no test build")
	}
	input := build.Input{
		Emitter:  emitterRevision,
		Language: LanguageRevision,
		Mode:     build.ModeTest,
		Flags:    []string{"test-module"},
	}
	lowering := lower.LowerTestModule
	if req.IR {
		// The IR bodies, as every other build lowers them (TEQb).
		input.Emitter += "+ir"
		if req.NoFallback {
			input.Emitter += "+nofallback"
		}
		lowering = lower.LowerIRTestModule
	}
	var units []lower.Unit
	sources := map[string]bool{}
	deps := map[string]build.Dependency{}
	for _, d := range stdDependencies() {
		deps[d.Name] = d
	}
	for _, a := range analyses {
		if len(a.Diagnostics) > 0 || a.program == nil || a.pkg == nil {
			return build.Module{}, &BuildError{Diagnostics: a.Diagnostics}
		}
		unit := a.program.unit()
		unit.NoFallback = req.NoFallback
		if err := unit.Validate(); err != nil {
			return build.Module{}, err
		}
		units = append(units, unit)
		input.Flags = append(input.Flags, "package "+a.program.root.path)
		for _, u := range append([]lower.Unit{unit}, unit.Imports...) {
			for _, s := range u.Sources {
				if !sources[s.Path] {
					sources[s.Path] = true
					input.Sources = append(input.Sources, build.Source{Path: s.Path, Text: []byte(s.Text)})
				}
			}
		}
		for _, d := range a.program.dependencies() {
			if old, ok := deps[d.Name]; ok && old != d {
				return build.Module{}, fmt.Errorf("sketchfront: package %s is read twice with different bytes", d.Name)
			}
			deps[d.Name] = d
		}
	}
	for _, d := range deps {
		input.Dependencies = append(input.Dependencies, d)
	}
	compiler := preparedCompilerIdentity()
	if compiler == "" {
		var err error
		if compiler, err = compilerIdentity(); err != nil {
			return build.Module{}, err
		}
	}
	input.Compiler = compiler
	for _, f := range lower.Runtime() {
		input.Runtime = append(input.Runtime, build.File{Path: f.Path, Data: f.Data})
	}
	module, err := req.Cache.Module(input, func() ([]build.File, error) {
		files, err := lowering(units)
		if err != nil {
			return nil, err
		}
		countLowered(files)
		out := make([]build.File, len(files))
		for i, f := range files {
			out[i] = build.File{Path: f.Path, Data: f.Data}
		}
		return out, nil
	})
	if err != nil {
		return build.Module{}, err
	}
	return module, link(module, req)
}
