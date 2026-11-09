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
	"strings"
)

// Load type-checks the Go packages that patterns name, from the export
// data `go list -deps -export` writes, for one platform, as the retired .ef
// compiler did (docs/prototype.md). dir is where go list runs (the module
// of a local or third-party package); env is the go command's environment,
// to which Load adds GOOS, GOARCH and CGO_ENABLED=0. It runs no Go process
// but go list, and only `ergo bind go` and go generate call it: the checker
// never does (interop.md, The checker's source of truth).
func Load(dir string, env []string, p Platform, patterns ...string) ([]*types.Package, error) {
	args := append([]string{"list", "-deps", "-export", "-json=ImportPath,Name,Export,DepOnly,Error"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, env...), "GOOS="+p.GOOS, "GOARCH="+p.GOARCH, "CGO_ENABLED=0", "GOFLAGS=-mod=mod")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list for %s: %v: %s", p, err, strings.TrimSpace(stderr.String()))
	}
	type listed struct {
		ImportPath, Name, Export string
		DepOnly                  bool
		Error                    *struct{ Err string }
	}
	exports := map[string]string{}
	var roots []string
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var l listed
		if err := dec.Decode(&l); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list for %s: %v", p, err)
		}
		if l.Error != nil {
			return nil, fmt.Errorf("go list for %s: %s: %s", p, l.ImportPath, l.Error.Err)
		}
		exports[l.ImportPath] = l.Export
		if !l.DepOnly {
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
			return nil, fmt.Errorf("import %s for %s: %v", path, p, err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, nil
}

// Platforms are the GOOS/GOARCH pairs a binding holds for: a declaration
// that differs between them is refused (interop.md).
var Platforms = []Platform{{"darwin", "arm64"}, {"linux", "amd64"}}

// Bind writes the binding file of each Go package that patterns name, by
// import path, loading them on every platform of Platforms.
func Bind(dir string, env []string, goVersion string, patterns ...string) (map[string]string, error) {
	perPlatform := make([]map[string]*types.Package, len(Platforms))
	var order []string
	for i, p := range Platforms {
		pkgs, err := Load(dir, env, p, patterns...)
		if err != nil {
			return nil, err
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
	for _, path := range order {
		var pkgs []*types.Package
		var platforms []Platform
		for i, p := range Platforms {
			if pkg, ok := perPlatform[i][path]; ok {
				pkgs = append(pkgs, pkg)
				platforms = append(platforms, p)
			}
		}
		if len(pkgs) != len(Platforms) {
			continue // a package of one platform only (syscall/js) has no binding
		}
		text, err := File(pkgs, platforms, goVersion)
		if err != nil {
			return nil, err
		}
		files[path] = text
	}
	return files, nil
}
