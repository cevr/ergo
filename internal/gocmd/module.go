package gocmd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Module is the Go module `ergo go` runs in.
type Module struct {
	Root string // the folder of go.mod, absolute
	Path string // its module path
	// GoMod is go.mod's text (or the user's -modfile's).
	GoMod []byte
}

// FindModule is the module of the folder dir: the nearest go.mod at or
// above it, as cmd/go finds it with GO111MODULE on and no go.work.
func FindModule(dir string) (Module, bool, error) {
	for d := dir; ; d = filepath.Dir(d) {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil {
			p := modulePath(data)
			if p == "" {
				return Module{}, false, fmt.Errorf("%s: no module line", filepath.Join(d, "go.mod"))
			}
			return Module{Root: d, Path: p, GoMod: data}, true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Module{}, false, err
		}
		if filepath.Dir(d) == d {
			return Module{}, false, nil
		}
	}
}

// modulePath is go.mod's module path (golang.org/x/mod's ModulePath).
func modulePath(mod []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(mod))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		rest, ok := strings.CutPrefix(line, "module")
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t' && rest[0] != '"') {
			continue
		}
		rest = strings.TrimSpace(rest)
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = strings.TrimSpace(rest[:i])
		}
		if strings.HasPrefix(rest, `"`) {
			if p, err := strconv.Unquote(rest); err == nil {
				return p
			}
			return ""
		}
		return rest
	}
	return ""
}

// ErgoPackage is a folder of the module that holds .ergo files.
type ErgoPackage struct {
	Package
	// Name is the Go package name: the folder's name.
	Name string
}

// ErgoPackages are the module's Ergo packages, by folder, as `./...`
// walks it: a folder whose name starts with . or _, testdata, and a
// nested module are skipped. A folder with .ergo files and a Go file of
// its own package is refused: one language per package (owner q296),
// since a Go file of the package could build any value of an Ergo type
// and skip its constructors. A Go test of package NAME_test beside the
// .ergo files is Go importing the package, and is allowed.
func ErgoPackages(m Module) ([]ErgoPackage, error) {
	var out []ErgoPackage
	err := filepath.WalkDir(m.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != m.Root {
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return err
		}
		var ergo, goFiles []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			switch filepath.Ext(e.Name()) {
			case ".ergo":
				ergo = append(ergo, e.Name())
			case ".go":
				goFiles = append(goFiles, e.Name())
			}
		}
		if len(ergo) == 0 {
			return nil
		}
		rel, err := filepath.Rel(m.Root, p)
		if err != nil {
			return err
		}
		ip := m.Path
		if rel != "." {
			ip += "/" + filepath.ToSlash(rel)
		}
		name := filepath.Base(p)
		if !token.IsIdentifier(name) || name == "main" {
			return fmt.Errorf("%s: an Ergo package's folder name is its Go package name, and %q is not one: rename the folder", p, name)
		}
		for _, g := range goFiles {
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(p, g), nil, parser.PackageClauseOnly)
			if err != nil {
				return err
			}
			if strings.HasSuffix(g, "_test.go") && f.Name.Name == name+"_test" {
				continue
			}
			return fmt.Errorf("%s: package %s holds both .ergo files (%s) and the Go file %s: one package is one language (owner q296); move the Go into another package, or the Ergo out (SPLIT-OUT)", p, ip, strings.Join(ergo, ", "), g)
		}
		out = append(out, ErgoPackage{Package: Package{ImportPath: ip, Dir: p}, Name: name})
		return nil
	})
	slices.SortFunc(out, func(a, b ErgoPackage) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	return out, err
}
