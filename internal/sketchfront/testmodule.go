package sketchfront

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sync/atomic"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

// One load and one test binary for the packages of a module (TEST-one).
// `ergo test DIR/...` used to load, check, lower and link each package's
// test build alone: each package paid std's import, the check of every
// package it imports and a Go link. CheckPackages reads the module once and
// checks each imported package once, under its import path; each selected
// package is then checked once as its own test build's root, the package
// main, which is the identity its test report names (a typed failure's
// type, a law's title). BuildTests lowers every root's test build into one
// Go module (lower.LowerTestModule) and links it once.
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
// the module once, checks each package at most once per identity and
// links once (WorkDone).
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
	if len(dirs) == 0 {
		return nil, fmt.Errorf("sketchfront: no package to check")
	}
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			return nil, fmt.Errorf("sketchfront: the package folder %s is not absolute and clean", dir)
		}
	}
	l := &loader{fsys: hostFS{}, platform: platform, root: dirs[0], pkgs: map[string]*loadedPackage{}, missing: map[string]string{}, state: map[*loadedPackage]int{}, texts: map[string]string{}}
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
		if err != nil || !filepath.IsLocal(rel) && rel != "." || l.nestedModule(dir) != "" {
			return nil, ErrNotShared
		}
		path := l.module
		if rel != "." {
			path += "/" + filepath.ToSlash(rel)
		}
		p, err := l.read(path, dir)
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
	if len(l.diags) > 0 {
		return nil, ErrNotShared
	}
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
		root := &loadedPackage{path: p.path, dir: p.dir, imports: p.imports}
		for _, f := range p.files {
			parsed, err := syntax.Parse(f.text)
			if err != nil {
				return nil, ErrNotShared
			}
			parsed.Name = f.syntax.Name
			f.syntax = parsed
			root.files = append(root.files, f)
		}
		l.roots = append(l.roots, root)
		if !l.checkShared(root, "main") {
			return nil, ErrNotShared
		}
		imports := l.closure(p)
		texts := maps.Clone(moduleTexts)
		for _, q := range append(imports[:len(imports):len(imports)], root) {
			for _, f := range q.files {
				texts[f.rel] = f.text
			}
			for name, data := range q.assets {
				texts[l.rel(filepath.Join(q.dir, filepath.FromSlash(name)))] = string(data)
			}
		}
		prog := &program{root: root, imports: imports, texts: texts}
		if l.goMod != nil {
			prog.goModule = l.goMod.lowerModule(l.root, l.goSum)
		}
		out[i] = Analysis{program: prog, pkg: root.pkg}
	}
	if len(l.bindings) > 0 {
		return nil, ErrNotShared
	}
	return out, nil
}

// checkShared checks the package p as path, as checkAll checks it, and
// reports whether it has no diagnostic.
func (l *loader) checkShared(p *loadedPackage, path string) bool {
	files := make([]*syntax.File, len(p.files))
	for k, f := range p.files {
		files[k] = f.syntax
	}
	pkg, ds := check.Check(path, files, l)
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
// source, dependency and selected package. An analysis that did not check
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
	compiler, err := compilerIdentity()
	if err != nil {
		return build.Module{}, err
	}
	input.Compiler = compiler
	for _, f := range lower.Runtime() {
		input.Runtime = append(input.Runtime, build.File{Path: f.Path, Data: f.Data})
	}
	module, err := req.Cache.Module(input, func() ([]build.File, error) {
		files, err := lower.LowerTestModule(units)
		if err != nil {
			return nil, err
		}
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
