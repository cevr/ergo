// Package linked writes the Go of the Ergo packages the ergo binary links
// (docs/tooling.md, Linked Ergo packages). A feature written in Ergo
// reaches the binary as checked-in Go: its Ergo source is a module (by
// default under internal/linked/<name>/ergo; the compiler's packages are
// the module compiler/), `go run ./scripts/linked` lowers it through the
// IR (lower.LowerIRLibrary: the library form of `ergo lib`, with IR
// bodies) and writes the derivative's Go text of what it compiled
// (irspike.LibraryText; capstone §12 Decided 5) into
// internal/linked/<name>/<name>.go, and Go packages call the exported Go
// functions. The Go is generated, never edited: TestLinkedGoIsFresh fails
// when the checked-in files differ from what Generate writes now.
//
// A package may also list command lines that `ergo gen cli` generates
// from an operation type of std; Generate writes them into the module
// before it lowers it, as `ergo gen cli` would.
//
// One list, one generator, one test: every Ergo package the binary links
// is a row of Packages. The package lasts through capstone S4; at S5
// `ergo` is compiler/cmd/ergo, built from the seed, and it is deleted.
package linked

import (
	"fmt"
	"go/format"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/cligen"
	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
)

// Package is one Ergo module the binary links.
type Package struct {
	// Name is the Go package name and the folder internal/linked/<Name>.
	Name string
	// Source is the module's folder from the repository root; "" is
	// internal/linked/<Name>/ergo.
	Source string
	// Exports is the package, by folder in the module, whose exported
	// functions become the Go functions; "" is the module's root package.
	Exports string
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
	// The compiler written in Ergo, one package so the binary links
	// compiler/syntax once (capstone S1d): the formatter (S1a, S2c), which
	// internal/hostsyntax calls, and compiler/syntax/dump's entries passed
	// through compiler/api (S2a, S2b), which syntax.Parse decodes since
	// S2d: the columns, the tokens, the failure and the canonical dumps the
	// decoder property compares.
	{Name: "compiler", Source: "compiler", Exports: "api"},
}

// Module is the Go module path of the repository.
const Module = "ergo.local/prototype"

// Dir is the folder of p, from the repository root.
func (p Package) Dir() string { return path.Join("internal/linked", p.Name) }

// ModuleDir is the folder of p's Ergo module, from the repository root.
func (p Package) ModuleDir() string {
	if p.Source != "" {
		return p.Source
	}
	return path.Join(p.Dir(), "ergo")
}

// Generate is every file that p's generation writes, by path from the
// repository root root: the generated command lines (Ergo) and the
// lowered package (Go).
func Generate(root string, p Package) (map[string][]byte, error) {
	out := map[string][]byte{}
	source := filepath.Join(root, filepath.FromSlash(p.ModuleDir()))
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
			out[path.Join(p.ModuleDir(), c.Dir, name)] = []byte(text)
			module[path.Join(c.Dir, name)] = text
		}
	}
	a, err := checkModule(module, p.Exports)
	if err != nil {
		return nil, fmt.Errorf("linked %s: %w", p.Name, err)
	}
	if len(a.Diagnostics) > 0 {
		return nil, fmt.Errorf("linked %s: %s does not check: %v", p.Name, source, a.Diagnostics[0])
	}
	unit, err := sketchfront.IRSpikeUnit(a, "")
	if err != nil {
		return nil, fmt.Errorf("linked %s: %w", p.Name, err)
	}
	files, _, err := lower.LowerIRLibrary(unit, p.Name)
	if err != nil {
		return nil, fmt.Errorf("linked %s: %w", p.Name, err)
	}
	_, tc, err := irspike.ErgoToolchains()
	if err != nil {
		return nil, fmt.Errorf("linked %s: the patched toolchain: %w", p.Name, err)
	}
	var stderr strings.Builder
	text, err := irspike.LibraryText(files, tc, p.Name, &stderr)
	if err != nil {
		return nil, fmt.Errorf("linked %s: %w\n%s", p.Name, err, stderr.String())
	}
	src, err := format.Source(text)
	if err != nil {
		return nil, fmt.Errorf("linked %s: gofmt: %w", p.Name, err)
	}
	out[path.Join(p.Dir(), p.Name+".go")] = src
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
