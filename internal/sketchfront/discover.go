package sketchfront

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// PackageTarget is one selected package of a recursive module command.
type PackageTarget struct {
	Path string // module import path, for reports
	Dir  string // absolute folder, for the existing package command
}

// DiscoverPackages selects the packages below dir in its nearest module,
// dependencies first. It reuses the loader's module, package and import rules
// but never checks a body or runs user code. A syntax error or import cycle
// is left for the selected command, so formatting stays syntax-only.
// Nested modules, testdata and hidden/underscore directories are excluded;
// directory symlinks are not followed. Imports outside dir may supply ordering
// edges, but do not enlarge the set of selected packages.
func DiscoverPackages(dir string) ([]PackageTarget, error) { return DiscoverPackagesThrough(nil, dir) }

// DiscoverPackagesThrough is DiscoverPackages reading through fsys (a warm
// request's Recorder, so a new or removed package folder is an input);
// nil reads the host. It lists folders in name order.
func DiscoverPackagesThrough(fsys FS, dir string) ([]PackageTarget, error) {
	if fsys == nil {
		fsys = hostFS{}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	l := &loader{fsys: fsys, root: abs, pkgs: map[string]*loadedPackage{}, missing: map[string]string{}, state: map[*loadedPackage]int{}, texts: map[string]string{}}
	found, err := l.findModule(abs)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s: recursive packages require an %s module", dir, ModFile)
	}
	if l.module == "" {
		return nil, fmt.Errorf("%s: %s", filepath.Join(l.root, ModFile), l.diags[0].Message)
	}
	var roots []*loadedPackage
	selected := map[*loadedPackage]bool{}
	var walk func(path string) error
	walk = func(path string) error {
		if path != abs {
			name := filepath.Base(path)
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
				return nil
			}
			_, modErr := fsys.Stat(filepath.Join(path, ModFile))
			if modErr == nil {
				return nil
			}
			if !errors.Is(modErr, fs.ErrNotExist) {
				return modErr
			}
		}
		rel, err := filepath.Rel(l.root, path)
		if err != nil {
			return err
		}
		name := l.module
		if rel != "." {
			name += "/" + filepath.ToSlash(rel)
		}
		p, err := l.read(name, path)
		if err != nil {
			return err
		}
		if p != nil {
			roots = append(roots, p)
			selected[p] = true
		}
		entries, err := fsys.ReadDir(path)
		if err != nil {
			return err
		}
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, e := range entries {
			if e.IsDir() {
				if err := walk(filepath.Join(path, e.Name())); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(abs); err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("%s: no Ergo packages below this directory", dir)
	}
	for _, p := range roots {
		if l.state[p] == unvisited {
			if err := l.visit(p, nil); err != nil {
				return nil, err
			}
		}
	}
	var out []PackageTarget
	for _, p := range l.order {
		if selected[p] {
			out = append(out, PackageTarget{Path: p.path, Dir: p.dir})
		}
	}
	return out, nil
}
