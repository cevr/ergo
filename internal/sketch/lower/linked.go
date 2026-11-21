package lower

import (
	"go/parser"
	"go/token"
	"path"
	"slices"
	"strconv"
	"strings"
)

// LinkedRuntime is the part of runtime that a module of the files program
// can link (CI-SELECT CS-CUTOFF): the runtime module's go.mod and every
// file of each runtime package that program imports, at any depth through
// the runtime's own imports. A file of a runtime package the program does
// not reach is not compiled into its binary, so an edit of it leaves the
// program's emitted digest, and the verdicts keyed on it, as they were.
//
// It over-approximates, never under: a package's imports are those of all
// its files under every build constraint, and a package holds every file
// below its folder down to the next folder that is a package (testdata,
// embedded assets). A runtime file that does not parse reaches every
// package (fail closed).
func LinkedRuntime(runtime, program []File) []File {
	g := runtimeGraphOf(runtime)
	reached := map[string]bool{}
	var visit func(dir string)
	visit = func(dir string) {
		if reached[dir] {
			return
		}
		reached[dir] = true
		for _, d := range g.imports[dir] {
			visit(d)
		}
	}
	if g.broken {
		for dir := range g.imports {
			visit(dir)
		}
	}
	for _, f := range program {
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		imports, ok := runtimeImports(f.Path, f.Data)
		if !ok {
			for dir := range g.imports {
				visit(dir)
			}
			continue
		}
		for _, d := range imports {
			visit(d)
		}
	}
	var out []File
	for _, f := range runtime {
		if dir, ok := g.owner[f.Path]; !ok || reached[dir] {
			out = append(out, f)
		}
	}
	return out
}

// runtimeGraph is the import graph of the runtime's packages, by folder
// in the emitted module ("ergo/runtime/v1"): each package's runtime
// imports, and the package that owns each file. A file no package owns
// (the runtime's go.mod) is always linked.
type runtimeGraph struct {
	imports map[string][]string
	owner   map[string]string
	broken  bool
}

// runtimeGraphOf is the graph of runtime: one imports-only parse of each
// of its Go files.
func runtimeGraphOf(runtime []File) runtimeGraph {
	g := runtimeGraph{imports: map[string][]string{}, owner: map[string]string{}}
	pkgs := map[string]bool{}
	for _, f := range runtime {
		if strings.HasSuffix(f.Path, ".go") {
			pkgs[path.Dir(f.Path)] = true
		}
	}
	for _, f := range runtime {
		for dir := path.Dir(f.Path); dir != "." && dir != runtimeDir; dir = path.Dir(dir) {
			if pkgs[dir] {
				g.owner[f.Path] = dir
				break
			}
		}
	}
	for dir := range pkgs {
		g.imports[dir] = nil
	}
	for _, f := range runtime {
		if !strings.HasSuffix(f.Path, ".go") || strings.HasSuffix(f.Path, "_test.go") {
			continue
		}
		dir := path.Dir(f.Path)
		imports, ok := runtimeImports(f.Path, f.Data)
		if !ok {
			g.broken = true
			continue
		}
		for _, d := range imports {
			if !slices.Contains(g.imports[dir], d) {
				g.imports[dir] = append(g.imports[dir], d)
			}
		}
	}
	return g
}

// runtimeImports are the runtime folders a Go file imports, and whether
// it parsed.
func runtimeImports(name string, src []byte) ([]string, bool) {
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
	if err != nil {
		return nil, false
	}
	var out []string
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, false
		}
		if rest, ok := strings.CutPrefix(p, runtimeModule+"/"); ok {
			out = append(out, runtimeDir+"/"+rest)
		}
	}
	return out, true
}
