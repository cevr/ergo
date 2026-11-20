// Package linked writes the Go of the Ergo packages the ergo binary links
// (docs/tooling.md, Linked Ergo packages). A feature written in Ergo
// reaches the binary as checked-in Go: its Ergo source is a module (by
// default under internal/linked/<name>/ergo; the compiler's packages are
// the module compiler/), `go run ./scripts/linked` lowers it into
// internal/linked/<name>/<name>.go, and Go packages call the exported Go
// functions. A row with IR set lowers through the IR (lower.LowerIRLibrary:
// the library form of `ergo lib`, with IR bodies) and writes the
// derivative's Go text of what it compiled (irspike.LibraryText; capstone
// §12 Decided 5); the others lower with lower.LowerLibrary (the lowering
// `ergo lib` uses). The Go is generated, never edited: TestLinkedGoIsFresh
// fails when the checked-in files differ from what Generate writes now.
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
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/apigen"
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
	// IR lowers the module through the IR and writes the patched
	// compile's Go text of it; false lowers it with lower.LowerLibrary.
	IR bool
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
	// The compiler written in Ergo, one package so the binary links
	// compiler/syntax once (capstone S1d): the formatter (S1a, S2c), which
	// internal/hostsyntax calls, and compiler/syntax/dump's entries passed
	// through compiler/api (S2a, S2b), which syntax.Parse decodes since
	// S2d: the columns, the tokens, the failure and the canonical dumps the
	// decoder property compares.
	{Name: "compiler", Source: "compiler", Exports: "api", IR: true},
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
		skip[apigen.Dir(path.Base(c.Std))] = true
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
		// The hub first (ergo gen api), in the module beside the cli
		// package, which is written over it.
		hub, err := apigen.Generate(pkg, c.Std, c.Op)
		if err != nil {
			return nil, fmt.Errorf("linked %s: ergo gen api %s.%s: %w", p.Name, c.Std, c.Op, err)
		}
		files, err := cligen.Generate(pkg, c.Std, c.Op, moduleName(module)+"/"+hub.Dir)
		if err != nil {
			return nil, fmt.Errorf("linked %s: ergo gen cli %s.%s: %w", p.Name, c.Std, c.Op, err)
		}
		for _, gen := range []struct {
			dir   string
			files map[string]string
		}{{hub.Dir, hub.Files}, {c.Dir, files.Files}} {
			for name, text := range gen.files {
				out[path.Join(p.ModuleDir(), gen.dir, name)] = []byte(text)
				module[path.Join(gen.dir, name)] = text
			}
		}
	}
	a, err := checkModule(module, p.Exports)
	if err != nil {
		return nil, fmt.Errorf("linked %s: %w", p.Name, err)
	}
	if len(a.Diagnostics) > 0 {
		return nil, fmt.Errorf("linked %s: %s does not check: %v", p.Name, source, a.Diagnostics[0])
	}
	var text []byte
	if p.IR {
		text, err = irText(a, p)
	} else {
		text, err = libraryText(a, p)
	}
	if err != nil {
		return nil, err
	}
	// gofmt to its fixed point: one pass leaves a /*line*/ comment before
	// a union's marker method on the method's line, and a second pass
	// moves it, so the gate's gofmt step and the checked-in file would
	// disagree (the 36 lines of codego-stale.md, SELFHOST-MERGE).
	src, err := gofmtFixpoint(text)
	if err != nil {
		return nil, fmt.Errorf("linked %s: gofmt: %w", p.Name, err)
	}
	out[path.Join(p.Dir(), p.Name+".go")] = src
	return out, nil
}

// irText is the patched compile's Go text of a lowered through the IR.
func irText(a sketchfront.Analysis, p Package) ([]byte, error) {
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
	return text, nil
}

// libraryText is the Go of a as `ergo lib` lowers it (lower.LowerLibrary).
func libraryText(a sketchfront.Analysis, p Package) ([]byte, error) {
	files, err := sketchfront.Library(a, "", path.Join(Module, p.Dir()), p.Name)
	if err != nil {
		return nil, fmt.Errorf("linked %s: %w", p.Name, err)
	}
	for _, f := range files {
		if f.Path == p.Name+".go" {
			return f.Data, nil
		}
	}
	return nil, fmt.Errorf("linked %s: the lowering wrote no %s.go", p.Name, p.Name)
}

// gofmtFixpoint formats src until gofmt leaves it unchanged.
func gofmtFixpoint(src []byte) ([]byte, error) {
	for range 4 {
		next, err := format.Source(src)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(next, src) {
			return src, nil
		}
		src = next
	}
	return nil, errors.New("gofmt does not reach a fixed point in 4 passes")
}

// moduleName is the module line's path of the module's ergo.mod.
func moduleName(module map[string]string) string {
	for _, line := range strings.Split(module["ergo.mod"], "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
			return f[1]
		}
	}
	return ""
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
