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
// compiler did. dir is where go list runs (the module
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
	cmd.Env = goEnv(env, p)
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
// that differs between them is refused (interop.md). With Phones they are
// Ergo's target list: `ergo build` links for each of them, emits for each
// phone, and refuses any other platform (EF110), so a program that checks
// against the bindings links on every platform it may be built for.
var Platforms = []Platform{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}

// Phone is a phone platform of Ergo's target list. `ergo build` writes
// its Go module and stops before the link: a phone program is linked by
// its packager, as a library (a c-archive on iOS, a c-shared one on
// Android) with the platform's own C compiler (interop.md, Platforms).
// It checks against the bindings of Bindings, the platform of Platforms
// that Go's build constraints read it as: GOOS=ios satisfies the darwin
// tag and GOOS=android the linux tag, so every file of a binding's
// package built for Bindings builds for the phone too. A declaration only
// a file constrained to ios or android (or to !ios, !android) holds is
// not in the binding; the emitted module's compile for the phone is the
// check of that (cmd/ergo's TestPhoneModuleTypechecks). Bindings are not made for the
// phones themselves: `go list -export` of a host's packages for a phone
// needs cgo there (MyGo's purego and its phone glue).
type Phone struct {
	Platform, Bindings Platform
}

// Phones are the phone platforms: the iPhone, an Android phone, and the
// Android emulator of an amd64 host.
var Phones = []Phone{
	{Platform{"ios", "arm64"}, Platform{"darwin", "arm64"}},
	{Platform{"android", "arm64"}, Platform{"linux", "arm64"}},
	{Platform{"android", "amd64"}, Platform{"linux", "amd64"}},
}

// PhoneOf is the phone platform p, and whether p is one.
func PhoneOf(p Platform) (Phone, bool) {
	for _, phone := range Phones {
		if phone.Platform == p {
			return phone, true
		}
	}
	return Phone{}, false
}

// Bind writes the binding file of each Go package that patterns name, by
// import path, loading them on every platform of Platforms. A package
// outside Go's standard library is stamped with its module (ModuleLine),
// and every such package a binding names gets its binding too, so the
// written set checks on its own.
func Bind(dir string, env []string, goVersion string, patterns ...string) (map[string]string, error) {
	perPlatform := make([]map[string]*types.Package, len(Platforms))
	var order []string
	c := newCloser(Platforms)
	var facts []Facts
	for i, p := range Platforms {
		pkgs, modules, err := Load(dir, env, p, patterns...)
		if err != nil {
			return nil, err
		}
		f, err := Derive(dir, env, p, patterns...)
		if err != nil {
			return nil, err
		}
		facts = append(facts, f)
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
	// The packages patterns name get the facts every platform derives; a
	// package a binding only names gets none (no fact: the copy stays).
	c.facts = meet(facts)
	files := map[string]string{}
	for i := range Platforms {
		for _, pkg := range perPlatform[i] {
			c.add(i, pkg)
		}
	}
	for _, path := range order {
		n := 0
		for i := range Platforms {
			if _, ok := perPlatform[i][path]; ok {
				n++
			}
		}
		if n != len(Platforms) {
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
