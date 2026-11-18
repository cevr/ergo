package sketchfront

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

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
func GraphOf(as []Analysis) *graphrows.Table {
	var pkgs []graphrows.SourcePackage
	seen := map[string]bool{}
	for _, a := range as {
		own := "main"
		if a.program != nil {
			own = a.program.module
			if own == "" {
				own = a.program.dir
			}
		}
		for _, p := range a.packagesOrRoot() {
			if seen[p.Path] {
				continue
			}
			seen[p.Path] = true
			sp := graphrows.SourcePackage{Path: p.Path, Module: own, Info: p.Partial, Checked: p.Package}
			if p.Module != "" {
				sp.Module = p.Module
			}
			if sp.Info == nil {
				sp.Info = p.Package
			}
			for _, f := range p.Files {
				sp.Files = append(sp.Files, graphrows.SourceFile{Name: f.Rel, Syntax: f.Syntax})
			}
			pkgs = append(pkgs, sp)
		}
	}
	return graphrows.Build(pkgs)
}

// packagesOrRoot is Packages, or the one root package of a source text.
func (a Analysis) packagesOrRoot() []CheckedPackage {
	if ps := a.Packages(); ps != nil {
		return ps
	}
	return []CheckedPackage{a.Root()}
}

// CheckTree checks every package below dir (`ergo graph export DIR/...`):
// the packages of dir's module below dir (DiscoverPackages), or, when dir
// is in no module, those of every module below it, each module with one
// shared load (CheckPackages) or, when a module refuses one, a package at
// a time through one CheckCache. Nested modules, testdata and hidden
// folders are skipped as DiscoverPackages skips them.
func CheckTree(dir string) ([]Analysis, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, err
	}
	l := newLoader(hostFS{}, gobind.Platform{}, abs, "")
	l.localOnly = true
	found, err := l.findModule(abs)
	if err != nil {
		return nil, err
	}
	roots := []string{abs}
	if !found {
		roots = nil
		err := filepath.WalkDir(abs, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && path != abs {
				name := e.Name()
				if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
			}
			if !e.IsDir() && e.Name() == ModFile {
				roots = append(roots, filepath.Dir(path))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	cache := NewCheckCache()
	var out []Analysis
	for _, root := range roots {
		targets, err := DiscoverPackages(root)
		if err != nil {
			if !found {
				continue // a module with no package of its own
			}
			return nil, err
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
					return nil, err
				}
				as = append(as, a)
			}
		}
		if err != nil {
			return nil, err
		}
		out = append(out, as...)
	}
	return out, nil
}

