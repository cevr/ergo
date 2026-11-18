// Package linked writes the Go of the Ergo packages the ergo binary links
// (docs/tooling.md, Linked Ergo packages). Until the mixed binary of
// capstone S1 links an Ergo package into this Go one, a feature written in
// Ergo reaches the binary as checked-in Go: its Ergo source is a module
// under internal/linked/<name>/ergo, `go run ./scripts/linked` lowers it
// with lower.LowerLibrary (the lowering `ergo lib` uses) into
// internal/linked/<name>/<name>.go, and cmd/ergo calls the exported Go
// functions. The Go is generated, never edited: TestLinkedGoIsFresh fails
// when the checked-in files differ from what Generate writes now.
//
// A package may also list command lines that `ergo gen cli` generates
// from an operation type of std; Generate writes them into the module
// before it lowers it, as `ergo gen cli` would.
//
// One list, one generator, one test: every Ergo package the binary links
// is a row of Packages. At capstone S1 the linker joins the Ergo package
// itself, and this package is deleted.
package linked

import (
	"fmt"
	"go/format"
	"path"
	"path/filepath"
	"slices"

	"ergo.local/prototype/internal/cligen"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketchfront"
)

// Package is one Ergo module the binary links.
type Package struct {
	// Name is the Go package name and the folder internal/linked/<Name>.
	Name string
	// CLIs are the command lines generated into the module first.
	CLIs []CLI
}

// CLI is the command line of a std operation type, generated into the
// module's folder Dir (`ergo gen cli` over std's package Std, type Op).
type CLI struct {
	Std, Op, Dir string
}

// Packages are the Ergo packages the ergo binary links.
var Packages = []Package{
	// ergo code (SEMTOOLS SEM1): std code's Reader over the fact tables.
	{Name: "code", CLIs: []CLI{{Std: "code", Op: "Reader", Dir: "codecli"}}},
	// ergo graph (CI-SELECT GRAPH-STD): std codegraph over the graph rows.
	{Name: "graph"},
}

// Module is the Go module path of the repository.
const Module = "ergo.local/prototype"

// Dir is the folder of p, from the repository root.
func (p Package) Dir() string { return path.Join("internal/linked", p.Name) }

// Generate is every file that p's generation writes, by path from the
// repository root root: the generated command lines (Ergo) and the
// lowered package (Go).
func Generate(root string, p Package) (map[string][]byte, error) {
	out := map[string][]byte{}
	source := filepath.Join(root, filepath.FromSlash(p.Dir()), "ergo")
	skip := map[string]bool{}
	for _, c := range p.CLIs {
		skip[c.Dir] = true
	}
	module, err := readModule(source, skip)
	if err != nil {
		return nil, fmt.Errorf("linked %s: %w", p.Name, err)
	}
	for _, c := range p.CLIs {
		pkg, err := stdPackage(c.Std, c.Op)
		if err != nil {
			return nil, err
		}
		files, err := cligen.Generate(pkg, c.Std, c.Op, "")
		if err != nil {
			return nil, fmt.Errorf("linked %s: ergo gen cli %s.%s: %w", p.Name, c.Std, c.Op, err)
		}
		for name, text := range files.Files {
			out[path.Join(p.Dir(), "ergo", c.Dir, name)] = []byte(text)
			module[path.Join(c.Dir, name)] = text
		}
	}
	a, err := checkModule(module)
	if err != nil {
		return nil, fmt.Errorf("linked %s: %w", p.Name, err)
	}
	if len(a.Diagnostics) > 0 {
		return nil, fmt.Errorf("linked %s: %s does not check: %v", p.Name, source, a.Diagnostics[0])
	}
	files, err := sketchfront.Library(a, "", path.Join(Module, p.Dir()), p.Name)
	if err != nil {
		return nil, fmt.Errorf("linked %s: %w", p.Name, err)
	}
	for _, f := range files {
		if f.Path == p.Name+".go" {
			src, err := format.Source(f.Data)
			if err != nil {
				return nil, fmt.Errorf("linked %s: gofmt: %w", p.Name, err)
			}
			out[path.Join(p.Dir(), f.Path)] = src
		}
	}
	if _, ok := out[path.Join(p.Dir(), p.Name+".go")]; !ok {
		return nil, fmt.Errorf("linked %s: the lowering wrote no %s.go", p.Name, p.Name)
	}
	return out, nil
}

// stdPackage is the checked std package at the import path std, which
// declares the type op.
func stdPackage(std, op string) (*check.Package, error) {
	pkg, ds := check.CheckSource("import " + std + "\n\nfunc uses(_ " + std + "." + op + ") {}\n")
	if len(ds) > 0 {
		return nil, fmt.Errorf("std %s does not check: %v", std, ds[0])
	}
	for _, o := range pkg.Info.Uses {
		if pn, ok := o.(*check.PkgName); ok && pn.Pkg != nil && pn.Pkg.Path == std {
			return pn.Pkg, nil
		}
	}
	for _, o := range pkg.Info.Defs {
		if pn, ok := o.(*check.PkgName); ok && pn.Pkg != nil && pn.Pkg.Path == std {
			return pn.Pkg, nil
		}
	}
	return nil, fmt.Errorf("std %s: no package behind its import", std)
}

// Sorted is the paths of files in order.
func Sorted(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for p := range files {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}
