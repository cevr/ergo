package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// goListRunnableTests uses cmd/go's test registration as the oracle, not
// the gate's own source scan. go list -test exposes the generated .test
// package's GoFiles in the build cache without compiling or running it.
func goListRunnableTests(root string, dirs []string) (map[string][]string, error) {
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no package directories to list")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	args := []string{"list", "-test", "-json=ImportPath,Dir,ForTest,GoFiles,TestGoFiles,XTestGoFiles"}
	for _, dir := range dirs {
		args = append(args, "./"+dir)
	}
	command := exec.Command("go", args...)
	command.Dir = root
	command.Env = stepEnvironment()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := listOutput(command)
	if err != nil {
		return nil, fmt.Errorf("go list -test: %w: %s", err, stderr.String())
	}
	packages := map[string]listedPackage{}
	testMains := map[string]string{}
	for decoder := json.NewDecoder(bytes.NewReader(out)); ; {
		var p struct {
			listedPackage
			GoFiles []string
		}
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if p.ForTest != "" {
			continue
		}
		// Plain packages list relative source paths; Go's generated test
		// mains instead name the absolute path of the cached source.
		if strings.HasSuffix(p.ImportPath, ".test") && len(p.GoFiles) == 1 && filepath.IsAbs(p.GoFiles[0]) {
			testMains[p.ImportPath] = p.GoFiles[0]
		} else {
			packages[p.Dir] = p.listedPackage
		}
	}
	tests := make(map[string][]string, len(dirs))
	for _, dir := range dirs {
		p, ok := packages[filepath.Join(root, filepath.FromSlash(dir))]
		if !ok {
			return nil, fmt.Errorf("go list -test omitted package %s", dir)
		}
		if len(p.TestGoFiles)+len(p.XTestGoFiles) == 0 {
			tests[dir] = nil // All test files can be build-constrained out.
			continue
		}
		main := testMains[p.ImportPath+".test"]
		if main == "" {
			return nil, fmt.Errorf("go list -test did not name the generated test source for %s", dir)
		}
		tests[dir], err = registeredTestNames(main)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
	}
	return tests, nil
}

func registeredTestNames(source string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), source, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	lists := map[string]ast.Expr{}
	for _, decl := range file.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, spec := range g.Specs {
			v := spec.(*ast.ValueSpec)
			if len(v.Names) == 1 && len(v.Values) == 1 {
				lists[v.Names[0].Name] = v.Values[0]
			}
		}
	}
	var names []string
	for _, group := range []string{"tests", "fuzzTargets", "examples"} {
		list, ok := lists[group].(*ast.CompositeLit)
		if !ok {
			return nil, fmt.Errorf("%s: generated test main has no %s registration list", source, group)
		}
		for _, elt := range list.Elts {
			entry, ok := elt.(*ast.CompositeLit)
			if !ok || len(entry.Elts) == 0 {
				return nil, fmt.Errorf("%s: malformed %s entry", source, group)
			}
			literal, ok := entry.Elts[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return nil, fmt.Errorf("%s: missing name in %s entry", source, group)
			}
			name, err := strconv.Unquote(literal.Value)
			if err != nil {
				return nil, fmt.Errorf("%s: invalid name in %s entry: %w", source, group, err)
			}
			names = append(names, name)
		}
	}
	return names, nil
}

// The inventory includes external tests and runnable examples, without
// benchmarks, documentation-only examples or tests of the inactive build
// tag. A package with all test files excluded is still part of the batch
// (listed-tests-drop-examples, listed-tests-first-package-only).
func TestGoListNamesRunnableTests(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/inventory\n\ngo 1.24\n")
	write(t, root, "p/p.go", "package p\n")
	write(t, root, "p/p_test.go", `package p
import "testing"
func TestInternal(t *testing.T) {}
func BenchmarkNotRun(b *testing.B) {}
`)
	write(t, root, "p/external_test.go", `package p_test
import "testing"
func TestExternal(t *testing.T) {}
func FuzzExternal(f *testing.F) {}
func Example() {
	// Output:
}
func Example_documentation() {}
`)
	write(t, root, "q/q.go", "package q\n")
	write(t, root, "q/q_test.go", `//go:build gate_inventory_fixture

package q
import "testing"
func TestInactive(t *testing.T) {}
`)
	got, err := goListRunnableTests(root, []string{"p", "q"})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got["p"])
	if want := []string{"Example", "FuzzExternal", "TestExternal", "TestInternal"}; !slices.Equal(got["p"], want) {
		t.Errorf("package p: got %v, want %v", got["p"], want)
	}
	if names, ok := got["q"]; !ok || len(names) != 0 {
		t.Errorf("package q: got %v (present: %t), want present with no names", names, ok)
	}
}
