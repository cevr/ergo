package sketchfront

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"runtime"
	"slices"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/fingerprint"
	"ergo.local/prototype/internal/sketch/lower"
)

// CheckGoModule reads the Ergo packages in the folders dirs (absolute) of
// the Go module of import path module rooted at root, and the packages
// they import, in one loader, and checks them: the packages `ergo go`
// builds (internal/gocmd), each by its Go import path. The Go module is
// the Ergo module: an ergo.mod at root names module, and with none the
// module is go.mod's. The program's root is the last package of the
// import order; every other is an import of it, imported by it or not.
func CheckGoModule(root, module string, dirs []string) (Analysis, error) {
	return checkGoModule(hostFS{}, gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, root, module, dirs)
}

func checkGoModule(fsys FS, platform gobind.Platform, root, module string, dirs []string) (Analysis, error) {
	if !filepath.IsAbs(root) {
		return Analysis{}, fmt.Errorf("sketchfront: the Go module folder %s is not absolute", root)
	}
	if len(dirs) == 0 {
		return Analysis{}, fmt.Errorf("sketchfront: the Go module %s has no Ergo package", module)
	}
	root = filepath.Clean(root)
	l := newLoader(fsys, platform, root, moduleCache())
	l.fps = map[string]fingerprint.Package{}
	l.noMain = true
	work.loads.Add(1)
	switch _, err := fsys.Stat(filepath.Join(root, ModFile)); {
	case err == nil:
		if _, err := l.findModule(root); err != nil {
			return Analysis{}, err
		}
		if len(l.diags) == 0 && l.module != module {
			return Analysis{}, fmt.Errorf("%s names the module %s and go.mod %s: an Ergo package of a Go module has its Go import path, so the two name one module", filepath.Join(root, ModFile), l.module, module)
		}
	case errors.Is(err, fs.ErrNotExist):
		// The Go module is the Ergo module (an ergo.mod above root is
		// another module's, as a go.mod above it is).
		l.root, l.module = root, module
		l.readGoModule()
	default:
		return Analysis{}, err
	}
	if len(l.diags) == 0 {
		for _, dir := range dirs {
			rel, err := filepath.Rel(root, filepath.Clean(dir))
			if err != nil || !filepath.IsLocal(rel) && rel != "." {
				return Analysis{}, fmt.Errorf("sketchfront: %s is not a folder of the Go module at %s", dir, root)
			}
			path := module
			if rel != "." {
				path += "/" + filepath.ToSlash(rel)
			}
			p, err := l.read(path, filepath.Clean(dir), nil)
			if err != nil {
				return Analysis{}, err
			}
			if p == nil {
				return Analysis{}, fmt.Errorf("%s: %s", dir, l.missing[path])
			}
			if l.state[p] == unvisited {
				if err := l.visit(p, nil); err != nil {
					return Analysis{}, err
				}
			}
		}
		if d := l.dependencyPin(); d != nil {
			l.diags = []report.Diagnostic{*d}
		}
		if len(l.diags) == 0 {
			l.segments = l.moduleSegments()
			prepareStd(slices.Collect(maps.Values(l.segments)))
			l.checkAll()
		}
	}
	prepareStd(nil)
	p := &program{texts: l.texts, bindings: l.bindings, dir: l.root, platform: platform, module: l.module, modRoot: l.root, trust: l.trust}
	if l.goMod != nil {
		p.goModule = l.goModule
	}
	if len(l.order) > 0 {
		p.root = l.order[len(l.order)-1]
		p.imports = l.order[:len(l.order)-1]
	}
	a := Analysis{Diagnostics: l.diags, program: p}
	if p.root != nil && !p.root.broken && len(l.diags) == 0 {
		a.pkg = p.root.pkg
	}
	return a, nil
}

// GoPackages lowers the checked Go module a (CheckGoModule) as the Go
// packages of its Ergo packages (lower.LowerGoPackages): each at its
// folder, as the Go package names gives it by import path, and the
// packages the lowering adds below the module's _ergo folder; paths are
// relative to the module root. Its files name each .ergo file by its
// absolute path, since the Go files reach the compile through -overlay
// from ergo's cache, where a path relative to one names a file of the
// cache.
func GoPackages(a Analysis, names map[string]string) ([]lower.File, error) {
	if len(a.Diagnostics) > 0 || a.program == nil || a.pkg == nil {
		return nil, &BuildError{Diagnostics: a.Diagnostics}
	}
	unit := a.program.unit()
	unit.Root = ""
	files, _, err := lower.LowerGoPackages(unit, a.program.module, names)
	return files, err
}
