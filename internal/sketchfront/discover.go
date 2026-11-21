package sketchfront

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/gobind"
)

// PackageTarget is one selected package of a recursive module command.
type PackageTarget struct {
	Path string // module import path, for reports
	Dir  string // absolute folder, for the existing package command
	Root string // absolute folder of the module that holds it
}

// DiscoverPackages selects the packages below dir in its nearest module,
// dependencies first; at a workspace root (IsWorkspaceRoot: an ergo.work,
// or no module holds dir), the packages of every member, each member once,
// a member after the members it resolves locally (Workspace.Order). It
// reuses the loader's module, package and import rules but never checks a
// body or runs user code. A syntax error or import cycle is left for the
// selected command, so formatting stays syntax-only. Nested modules and
// the folders admitFolder refuses (testdata, hidden, underscored,
// node_modules, vendor) are excluded; directory symlinks are not followed. Imports outside dir may supply ordering
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
	if root, err := IsWorkspaceRoot(fsys, abs); err != nil {
		return nil, err
	} else if root {
		return discoverWorkspacePackages(fsys, dir, abs)
	}
	return discoverModulePackages(fsys, dir, abs)
}

// discoverWorkspacePackages is DiscoverPackages at the workspace root abs:
// each member's packages (discoverModulePackages), members in
// Workspace.Order. A member with no package of its own adds none; a
// member whose ergo.mod does not read fails the selection, as one module
// does.
func discoverWorkspacePackages(fsys FS, dir, abs string) ([]PackageTarget, error) {
	ws, err := DiscoverWorkspace(fsys, abs)
	if err != nil {
		return nil, err
	}
	if len(ws.Members) == 0 {
		return nil, fmt.Errorf("%s: no %s below this directory: recursive packages need a module or a workspace of modules", dir, ModFile)
	}
	var out []PackageTarget
	for _, i := range ws.Order() {
		m := ws.Members[i]
		if m.Problem != "" {
			return nil, errors.New(m.Problem)
		}
		pkgs, err := discoverModulePackages(fsys, m.Dir, m.Dir)
		if errors.Is(err, errNoPackages) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, pkgs...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no Ergo packages below this directory", dir)
	}
	return out, nil
}

// errNoPackages is a module's selection with no package in it.
var errNoPackages = errors.New("no Ergo packages below this directory")

// discoverModulePackages is DiscoverPackages in the module that holds abs
// (dir as the command named it); a nil fsys reads the host.
func discoverModulePackages(fsys FS, dir, abs string) ([]PackageTarget, error) {
	if fsys == nil {
		fsys = hostFS{}
	}
	l := newLoader(fsys, gobind.Platform{}, abs, "")
	l.localOnly = true
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
			if !admitFolder(filepath.Base(path)) {
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
		p, err := l.read(name, path, nil)
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
		return nil, fmt.Errorf("%s: %w", dir, errNoPackages)
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
			out = append(out, PackageTarget{Path: p.path, Dir: p.dir, Root: l.root})
		}
	}
	return out, nil
}
