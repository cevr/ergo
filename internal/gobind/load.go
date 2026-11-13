package gobind

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
)

// ModuleLine starts the third line of the binding of a package outside
// Go's standard library: the stamp of the Go module that holds it, as the
// go.mod where ergo bind go ran names it (ModuleLine). The loader
// compares it with go.mod, a file read, and reports a stale binding at
// the import (IG7; docs/spec/interop.md, Go modules).
const ModuleLine = "// ergo:module "

// goModule is go list's Module of a package.
type goModule struct {
	Path, Version string
	Main          bool
	Replace       *struct{ Path, Version string }
}

// stamp is the stamp of a package's module: `path version`, then
// ` => replacement [version]` when go.mod replaces it; only `path` for go.mod's
// own module. It is "" for Go's standard library.
func (m *goModule) stamp() string {
	switch {
	case m == nil:
		return ""
	case m.Main:
		return m.Path
	}
	s := m.Path + " " + m.Version
	if m.Replace != nil {
		s += " => " + m.Replace.Path
		if m.Replace.Version != "" {
			s += " " + m.Replace.Version
		}
	}
	return s
}

// Load type-checks the Go packages that patterns name, from the export
// data `go list -deps -export` writes, for one platform, as the retired .ef
// compiler did (docs/prototype.md). dir is where go list runs (the module
// of a local or third-party package); env is the go command's environment,
// to which Load adds GOOS, GOARCH, CGO_ENABLED=0 and -mod=readonly, so go
// list never edits go.mod or go.sum. It runs no Go process but go list,
// and only `ergo bind go` and go generate call it: the checker never does
// (interop.md, The checker's source of truth). It also returns the module
// stamp of every package it listed outside Go's standard library.
func Load(dir string, env []string, p Platform, patterns ...string) ([]*types.Package, map[string]string, error) {
	args := append([]string{"list", "-deps", "-export", "-json=ImportPath,Name,Export,DepOnly,Error,Module"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	// The user's GOFLAGS stay (-modcacherw, build tags), but -mod is ours.
	flags := []string{"-mod=readonly"}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GOFLAGS="); ok {
			flags = []string{"-mod=readonly"}
			for _, f := range strings.Fields(v) {
				if !strings.HasPrefix(f, "-mod=") && !strings.HasPrefix(f, "--mod=") {
					flags = append(flags, f)
				}
			}
		}
	}
	cmd.Env = append(append([]string{}, env...), "GOOS="+p.GOOS, "GOARCH="+p.GOARCH, "CGO_ENABLED=0", "GOFLAGS="+strings.Join(flags, " "))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("go list for %s: %v: %s", p, err, strings.TrimSpace(stderr.String()))
	}
	type listed struct {
		ImportPath, Name, Export string
		DepOnly                  bool
		Error                    *struct{ Err string }
		Module                   *goModule
	}
	exports := map[string]string{}
	modules := map[string]string{}
	var roots []string
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var l listed
		if err := dec.Decode(&l); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("go list for %s: %v", p, err)
		}
		if l.Error != nil {
			return nil, nil, fmt.Errorf("go list for %s: %s: %s", p, l.ImportPath, l.Error.Err)
		}
		exports[l.ImportPath] = l.Export
		if s := l.Module.stamp(); s != "" {
			modules[l.ImportPath] = s
		}
		if !l.DepOnly {
			if why := rootRefusal(l.ImportPath, l.Name); why != "" {
				return nil, nil, fmt.Errorf("%s: %s", l.ImportPath, why)
			}
			roots = append(roots, l.ImportPath)
		}
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok || file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
	var pkgs []*types.Package
	for _, path := range roots {
		if path == "unsafe" {
			continue
		}
		pkg, err := imp.Import(path)
		if err != nil {
			return nil, nil, fmt.Errorf("import %s for %s: %v", path, p, err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, modules, nil
}

// rootRefusal is why a package named on the command line gets no binding:
// a program imports no main package, and Go admits an internal or vendor
// package only inside the tree that holds it, which the emitted program
// never is (interop.md, EF111 of the retired design).
func rootRefusal(path, name string) string {
	switch {
	case name == "main":
		return "a main package is a program, which Go does not import"
	case internalPath(path):
		return "Go admits an internal or vendor package only inside the tree that holds it, and the emitted program is outside every tree"
	}
	return ""
}

// Platforms are the GOOS/GOARCH pairs a binding holds for: a declaration
// that differs between them is refused (interop.md). `ergo build` links
// for each of them, and for the rest of BuildPlatforms.
var Platforms = []Platform{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}

// BuildPlatforms are the GOOS/GOARCH pairs `ergo build` links for and
// refuses any other (EF110): the binding platforms, then Windows, the
// browser and WASI. Go is the one target; GOOS and GOARCH are Go's choice
// of where it runs. A program for one of the last three may import only
// a Go binding that states it (Bind; a std binding does not, so its
// import is S104 there), and the runtime
// refuses there what it cannot do as a typed failure (exec.Failed;
// term.NotTerminal), so a program that checks links on each of them.
var BuildPlatforms = append(slices.Clip(Platforms), Platform{"windows", "amd64"}, Platform{"js", "wasm"}, Platform{"wasip1", "wasm"})

// Bind writes the binding file of each Go package that patterns name, by
// import path, loading them on every platform of Platforms. A package
// outside Go's standard library is stamped with its module (ModuleLine),
// and every such package a binding names gets its binding too, so the
// written set checks on its own.
//
// A binding also states each build platform past Platforms (windows,
// js, wasip1) where the packages load and its declarations are the ones
// it has on Platforms, rendered alike: a portable package's binding holds
// there too, and a program that imports it builds there. A platform
// where the package differs is left out of its header, never a reason to
// refuse a declaration the binding platforms agree on (interop.md,
// Platforms).
func Bind(dir string, env []string, goVersion string, patterns ...string) (map[string]string, error) {
	files, err := bindOn(dir, env, goVersion, Platforms, patterns)
	if err != nil {
		return nil, err
	}
	agree := map[string][]Platform{}
	for _, p := range BuildPlatforms[len(Platforms):] {
		wide, err := bindOn(dir, env, goVersion, append(slices.Clip(Platforms), p), patterns)
		if err != nil {
			continue // the packages do not load there
		}
		for path, text := range files {
			if w, ok := wide[path]; ok && withoutHeader(w) == withoutHeader(text) {
				agree[path] = append(agree[path], p)
			}
		}
	}
	for path, ps := range agree {
		files[path] = withPlatforms(files[path], path, goVersion, append(slices.Clip(Platforms), ps...))
	}
	return files, nil
}

// withoutHeader is a binding's text without its `// ergo:bind` line, the
// one line the platforms it was bound on decide.
func withoutHeader(text string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "// ergo:bind ") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// withPlatforms is text with its `// ergo:bind` line naming ps.
func withPlatforms(text, path, goVersion string, ps []Platform) string {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.String()
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "// ergo:bind ") {
			lines[i] = "// ergo:bind " + path + " " + goVersion + " " + strings.Join(names, " ")
			break
		}
	}
	return strings.Join(lines, "\n")
}

// bindOn is Bind's bindings over platforms.
func bindOn(dir string, env []string, goVersion string, platforms []Platform, patterns []string) (map[string]string, error) {
	perPlatform := make([]map[string]*types.Package, len(platforms))
	var order []string
	c := newCloser(platforms)
	for i, p := range platforms {
		pkgs, modules, err := Load(dir, env, p, patterns...)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			c.modules = modules
		}
		perPlatform[i] = map[string]*types.Package{}
		for _, pkg := range pkgs {
			perPlatform[i][pkg.Path()] = pkg
			if i == 0 {
				order = append(order, pkg.Path())
			}
		}
	}
	files := map[string]string{}
	for i := range platforms {
		for _, pkg := range perPlatform[i] {
			c.add(i, pkg)
		}
	}
	for _, path := range order {
		n := 0
		for i := range platforms {
			if _, ok := perPlatform[i][path]; ok {
				n++
			}
		}
		if n != len(platforms) {
			continue // a package of one platform only (syscall/js) has no binding
		}
		text, err := c.file(path, perPlatform[0][path].Name(), goVersion)
		if err != nil {
			return nil, err
		}
		files[path] = text
	}
	// The packages outside Go's standard library that the bindings name.
	var named []string
	for path := range c.done {
		if _, ok := files[path]; !ok && c.modules[path] != "" && !internalPath(path) {
			named = append(named, path)
		}
	}
	sort.Strings(named)
	for _, path := range named {
		text, err := c.file(path, c.name(path), goVersion)
		if err != nil {
			return nil, err
		}
		files[path] = text
	}
	return files, nil
}
