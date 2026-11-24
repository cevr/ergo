package sketchfront

import (
	"errors"
	"path/filepath"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/graphrows"
)

// Graph is the graph rows of the checked program (CI-SELECT §12,
// GRAPH-TABLE): GraphOf of a alone.
func (a Analysis) Graph() *graphrows.Table { return GraphOf([]Analysis{a}) }

// GraphOf is the graph rows of the checked programs as, one table: their
// packages in the loader's order, a package two programs share once, each
// with its files, the checker's package (also a partial one, whose rows
// are what the checker resolved) and, when it and its imports check, its
// tasks. A package of a program's own module sits under that module (its
// folder when there is no ergo.mod); one of a required module under
// PATH@VERSION.
func GraphOf(as []Analysis) *graphrows.Table { return graphrows.Build(sourcePackages(as)) }

// sourcePackages is the packages of as for the graph rows, GraphOf's.
func sourcePackages(as []Analysis) []graphrows.SourcePackage {
	var pkgs []graphrows.SourcePackage
	seen := map[string]bool{}
	for _, a := range as {
		for _, p := range a.packagesOrRoot() {
			if seen[p.Path] {
				continue
			}
			seen[p.Path] = true
			sp := graphrows.SourcePackage{Path: p.Path, Module: a.ModuleOf(p), Info: p.Partial, Checked: p.Package}
			if sp.Info == nil {
				sp.Info = p.Package
			}
			for _, f := range p.Files {
				sp.Files = append(sp.Files, graphrows.SourceFile{Name: f.Rel, Syntax: f.Syntax})
			}
			pkgs = append(pkgs, sp)
		}
	}
	return pkgs
}

// ModuleOf is the module that holds p, as the graph rows name it: a
// required module's PATH@VERSION, else the program's own module, its
// folder when there is no ergo.mod, or "main" for one source text. Lint
// and the graph export read this one identity, so a file's
// module-relative name is never taken for another module's.
func (a Analysis) ModuleOf(p CheckedPackage) string {
	switch {
	case p.Module != "":
		return p.Module
	case a.program == nil:
		return "main"
	case a.program.module != "":
		return a.program.module
	}
	return a.program.dir
}

// packagesOrRoot is Packages, or the one root package of a source text.
func (a Analysis) packagesOrRoot() []CheckedPackage {
	if ps := a.Packages(); ps != nil {
		return ps
	}
	return []CheckedPackage{a.Root()}
}

// CheckTree checks every package below dir (`ergo graph export DIR/...`):
// the packages of dir's module below dir (DiscoverPackages), or, at a
// workspace root, those of every member (DiscoverWorkspace, the one
// admission rule), each module with one shared load (CheckPackages) or,
// when a module refuses one, a package at a time through one CheckCache.
// ws is the workspace at a workspace root, else nil.
func CheckTree(dir string) (as []Analysis, ws *Workspace, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, nil, err
	}
	atRoot, err := IsWorkspaceRoot(nil, abs)
	if err != nil {
		return nil, nil, err
	}
	roots := []string{abs}
	if atRoot {
		found, err := DiscoverWorkspace(nil, abs)
		if err != nil {
			return nil, nil, err
		}
		ws, roots = &found, nil
		for _, i := range found.Order() {
			roots = append(roots, found.Members[i].Dir)
		}
	}
	found := !atRoot
	cache := NewCheckCache()
	var out []Analysis
	for _, root := range roots {
		targets, err := discoverModulePackages(nil, root, root)
		if err != nil {
			if !found {
				continue // a module with no package of its own, or one that does not read
			}
			return nil, nil, err
		}
		var dirs []string
		for _, t := range targets {
			dirs = append(dirs, t.Dir)
		}
		as, err := CheckPackages(dirs, gobind.Platform{})
		if errors.Is(err, ErrNotShared) {
			as, err = nil, nil
			for _, d := range dirs {
				a, err := cache.CheckDir(d)
				if err != nil {
					return nil, nil, err
				}
				as = append(as, a)
			}
		}
		if err != nil {
			return nil, nil, err
		}
		out = append(out, as...)
	}
	return out, ws, nil
}

// GraphOfWorkspace is GraphOf with the workspace's module rows: a module
// node for every member, also one with no package, and a requires edge
// for each of ws's edges.
func GraphOfWorkspace(ws Workspace, as []Analysis) *graphrows.Table {
	mods := make([]graphrows.ModuleRow, len(ws.Members))
	for i, m := range ws.Members {
		mods[i].Name = m.Name()
	}
	for _, e := range ws.Edges {
		mods[e.From].Requires = append(mods[e.From].Requires, ws.Members[e.To].Name())
	}
	return graphrows.BuildWorkspace(mods, sourcePackages(as))
}
