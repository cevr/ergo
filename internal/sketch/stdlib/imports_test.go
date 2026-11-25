package stdlib_test

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/testwait"
)

// TestStdImportsAreAcyclic: a std package may import another std package
// (io imports strings), and the std importer checks each package in its
// own sync.Once, so an import cycle in std would wait forever. This test
// finds every std import from the sources and fails on a cycle, naming it.
func TestStdImportsAreAcyclic(t *testing.T) {
	edges := map[string][]string{}
	for _, path := range stdlib.Packages() {
		sources, _ := stdlib.Package(path)
		for name, src := range sources {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("%s/%s: %v", path, name, err)
			}
			for _, d := range f.Decls {
				if imp, ok := d.(*syntax.ImportDecl); ok {
					for _, spec := range importPaths(imp) {
						if _, isStd := stdlib.Package(spec); isStd {
							edges[path] = append(edges[path], spec)
						}
					}
				}
			}
		}
	}
	if !slices.Contains(edges["io"], "strings") {
		t.Errorf("the walk found no import of strings in io: %v", edges)
	}
	if !slices.Contains(edges["core"], "option") {
		t.Errorf("core must import option; got %v", edges["core"])
	}
	if slices.Contains(edges["core"], "collections") {
		t.Errorf("core must not import collections directly (core -> option -> collections); got %v", edges["core"])
	}
	if !slices.Contains(edges["option"], "collections") {
		t.Errorf("option must import collections; got %v", edges["option"])
	}
	if slices.Contains(edges["option"], "core") {
		t.Errorf("option must not import core; got %v", edges["option"])
	}
	for _, forbidden := range []string{"core", "option", "slices"} {
		if slices.Contains(edges["collections"], forbidden) {
			t.Errorf("collections must not import %s; got %v", forbidden, edges["collections"])
		}
	}
	if cycle := findCycle(edges); cycle != nil {
		t.Errorf("std imports form a cycle: %s", strings.Join(cycle, " -> "))
	}
	if got := findCycle(map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}}); len(got) != 4 {
		t.Errorf("the cycle finder missed a -> b -> c -> a: %v", got)
	}
	for _, path := range []string{"collections", "option", "core"} {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		var imported []string
		for _, f := range pkg.Files() {
			for _, imp := range pkg.ImportsOf(f) {
				if imp.Pkg != nil && !imp.Pkg.Host() {
					imported = append(imported, imp.Pkg.Path)
				}
			}
		}
		switch path {
		case "core":
			if !slices.Contains(imported, "option") {
				t.Errorf("checked core.Imports = %v; want option", imported)
			}
			if slices.Contains(imported, "collections") {
				t.Errorf("checked core.Imports = %v; must not import collections directly", imported)
			}
		case "option":
			if !slices.Contains(imported, "collections") {
				t.Errorf("checked option.Imports = %v; want collections", imported)
			}
			if slices.Contains(imported, "core") {
				t.Errorf("checked option.Imports = %v; must not import core", imported)
			}
		case "collections":
			if len(imported) != 0 {
				t.Errorf("checked collections.Imports = %v; want no std imports", imported)
			}
		}
	}
}

// TestStdImportConcurrent: many goroutines import std packages that
// import one another at once; each gets the one checked package, and none
// waits forever (the importer held one lock across a nested import before
// S1b, so a std package that imported another deadlocked).
func TestStdImportConcurrent(t *testing.T) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	got := make([]*check.Package, 16)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := []string{"io", "strings"}[i%2]
			p, err := check.Std.Import(path)
			if err != nil {
				t.Errorf("%s: %v", path, err)
			}
			got[i] = p
		}()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-testwait.After(t):
		t.Fatal("the std importer did not return by the test's deadline")
	}
	for i := range got {
		if got[i] != got[i%2] {
			t.Errorf("import %d gave another package than import %d", i, i%2)
		}
	}
}

// importPaths are the paths of an import declaration's Ergo imports.
func importPaths(d *syntax.ImportDecl) []string {
	var out []string
	for _, s := range d.Specs {
		// An import go names a Go package, not a std one, even where the
		// paths agree (std math imports Go's math).
		if s.Path != nil && s.Kind != syntax.ImportGo {
			out = append(out, strings.Trim(s.Path.Value, `"`))
		}
	}
	return out
}

// findCycle is one cycle of edges, its first node repeated at its end, or
// nil.
func findCycle(edges map[string][]string) []string {
	state := map[string]int{} // 1 on the stack, 2 done
	var stack []string
	var visit func(n string) []string
	visit = func(n string) []string {
		state[n] = 1
		stack = append(stack, n)
		for _, m := range edges[n] {
			switch state[m] {
			case 1:
				i := slices.Index(stack, m)
				return append(slices.Clone(stack[i:]), m)
			case 0:
				if c := visit(m); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = 2
		return nil
	}
	keys := make([]string, 0, len(edges))
	for k := range edges {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if state[k] == 0 {
			if c := visit(k); c != nil {
				return c
			}
		}
	}
	return nil
}
