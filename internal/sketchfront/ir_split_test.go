package sketchfront

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRFilePerPackage: the IR path's package split (compile-speed CS10)
// gives each Go package of a split program the IR file of its own bodies,
// in its folder. On examples of several Ergo packages: the functions an
// IR file names are exactly its package's functions without a Go body;
// some program has IR bodies in two packages; and every package-scope
// name an IR body spells is declared in its own package, the one scope
// the patched compile resolves it in, which for some program means a
// name that another package declares (the split's alias or forwarding
// function). It checks the lowered files in process; the CLI builds a
// split IR program with the patched compile in cmd/ergo's
// TestDirectBuildStartsNoGoCommand. Mutants: every IR body in the root's
// file (cs-ir-file-shared); no declaration of another package's name
// (cs-ir-foreign-undeclared).
func TestIRFilePerPackage(t *testing.T) {
	twoPackages, foreign := false, false
	for _, name := range []string{"particles", "okra-keys", "forge", "todo-shell"} {
		dir, err := filepath.Abs(filepath.Join("..", "..", "examples", name, "cmd", name))
		if err != nil {
			t.Fatal(err)
		}
		a, err := CheckDir(dir)
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("%s does not check: %v %v", name, err, a.Diagnostics)
		}
		unit, err := analysisUnit(a, filepath.Join(dir, "main.ergo"))
		if err != nil {
			t.Fatal(err)
		}
		files, _, rep, err := lower.LowerIRSplit(unit, false)
		if err != nil || rep.Reason != "" {
			t.Fatalf("%s: the IR split fails: %v %+v", name, err, rep)
		}
		decls, bodiless, irs := map[string]map[string]bool{}, map[string]map[string]bool{}, map[string][]irNames{}
		for _, f := range files {
			dir := path.Dir(f.Path)
			switch {
			case path.Base(f.Path) == lower.IRFileName:
				irs[dir] = readIRNames(t, f.Data)
			case strings.HasSuffix(f.Path, ".go"):
				if decls[dir] == nil {
					decls[dir], bodiless[dir] = map[string]bool{}, map[string]bool{}
				}
				goDecls(t, f, decls[dir], bodiless[dir])
			}
		}
		withIR := 0
		for dir, want := range bodiless {
			fns, ok := irs[dir]
			if !ok && len(want) > 0 {
				t.Errorf("%s: package %s has functions without a body and no IR file", name, dir)
			}
			var got []string
			for _, fn := range fns {
				got = append(got, fn.name)
				for _, n := range fn.names {
					elsewhere := false
					for other, d := range decls {
						elsewhere = elsewhere || (other != dir && d[n])
					}
					switch {
					case !decls[dir][n] && elsewhere:
						t.Errorf("%s: the IR body %s in %s names %s, which another package declares and %s does not", name, fn.name, dir, n, dir)
					case elsewhere:
						foreign = true
					}
				}
			}
			if len(got) > 0 {
				withIR++
			}
			if w := slices.Sorted(maps.Keys(want)); !slices.Equal(slices.Sorted(slices.Values(got)), w) {
				t.Errorf("%s: %s's IR file names %v; its functions without a body are %v", name, dir, got, w)
			}
		}
		for dir := range irs {
			if _, ok := bodiless[dir]; !ok {
				t.Errorf("%s: an IR file in %s, which has no Go package", name, dir)
			}
		}
		twoPackages = twoPackages || withIR >= 2
	}
	if !twoPackages || !foreign {
		t.Fatalf("no program has IR bodies in two packages (%v) or an IR name of another package (%v): the property is vacuous", twoPackages, foreign)
	}
}

// irNames are an IR function's name and the package-scope names its body
// spells: the functions it calls and the named part of its types.
type irNames struct {
	name  string
	names []string
}

// readIRNames reads an IR file's functions and their names.
func readIRNames(t *testing.T, data []byte) []irNames {
	t.Helper()
	var f struct {
		Funcs []map[string]any `json:"funcs"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	var out []irNames
	for _, fn := range f.Funcs {
		r := irNames{name: fn["name"].(string)}
		typ := func(v any) {
			if s, ok := v.(string); ok && s != "" {
				r.names = append(r.names, strings.TrimLeft(s, "[]*"))
			}
		}
		var walk func(v any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				if v["k"] == "call" {
					r.names = append(r.names, v["name"].(string))
				}
				typ(v["t"])
				typ(v["type"])
				if ts, ok := v["types"].([]any); ok {
					for _, x := range ts {
						typ(x)
					}
				}
				for _, x := range v {
					walk(x)
				}
			case []any:
				for _, x := range v {
					walk(x)
				}
			}
		}
		walk(fn["locals"])
		walk(fn["body"])
		out = append(out, r)
	}
	return out
}

// goDecls adds a Go file's package-scope names to decls and its functions
// without a body to bodiless, a method as T.M.
func goDecls(t *testing.T, f lower.File, decls, bodiless map[string]bool) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), f.Path, f.Data, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s does not parse: %v", f.Path, err)
	}
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil {
				name = recvName(d.Recv.List[0].Type) + "." + name
			} else {
				decls[name] = true
			}
			if d.Body == nil {
				bodiless[name] = true
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					decls[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						decls[n.Name] = true
					}
				}
			}
		}
	}
}

// recvName is a receiver type's name.
func recvName(x ast.Expr) string {
	switch x := x.(type) {
	case *ast.StarExpr:
		return recvName(x.X)
	case *ast.IndexExpr:
		return recvName(x.X)
	case *ast.IndexListExpr:
		return recvName(x.X)
	case *ast.Ident:
		return x.Name
	}
	return ""
}
