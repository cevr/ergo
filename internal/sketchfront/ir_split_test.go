package sketchfront

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRFilePerPackage: the IR path's package split (compile-speed CS10)
// gives each Go package of a split program the IR file of its own bodies,
// in its folder. On examples of several Ergo packages: the functions an
// IR file names are exactly its package's functions without a Go body
// (a generic one's Go body is the placeholder the patched compile
// replaces, PATCH-MIN M3: no body of its own either);
// some program has IR bodies in two packages; and every package-scope
// name an IR body spells is declared in its own package, the one scope
// the patched compile resolves it in, which for some program means a
// name that another package declares (the split's alias or forwarding
// function). A generic name of another package has no such declaration
// (Q5, option b): the IR spells it PATH.Name, a call of it is a pcall, and
// for some program PATH is a package of the split that declares Name and
// that the body's package imports, where the patched compile resolves it.
// stopwatch names a renamed generic of std/machines (armed_1). A
// package let of another package is copied into the package (sudoku's
// Units): Ergo has no mutable package state. runtime-faults and address-routes wait
// for FLIP red #9 (an IR file naming a generic instance twice).
// It checks the lowered files in process; the CLI builds a
// split IR program with the patched compile in cmd/ergo's
// TestDirectBuildStartsNoGoCommand. Mutants: every IR body in the root's
// file (cs-ir-file-shared); no declaration of another package's name
// (cs-ir-foreign-undeclared); a generic name spelled before the split's
// rename (m5-generic-unrenamed), not imported (m5-generic-unimported), or
// called by a plain call (m5-generic-plain-call); a package let of another
// package refused (q5b-foreign-let-refused); a placeholder the reader does
// not match, a Go body the IR file also names (irf-placeholder-unread).
func TestIRFilePerPackage(t *testing.T) {
	twoPackages, foreign, generic := false, false, false
	for _, name := range []string{"particles", "forge", "todo-shell", "calc", "sudoku", "stopwatch"} {
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
		imports := map[string]map[string]bool{}
		for _, f := range files {
			dir := path.Dir(f.Path)
			switch {
			case path.Base(f.Path) == lower.IRFileName:
				irs[dir] = readIRNames(t, f.Data)
			case strings.HasSuffix(f.Path, ".go"):
				if decls[dir] == nil {
					decls[dir], bodiless[dir], imports[dir] = map[string]bool{}, map[string]bool{}, map[string]bool{}
				}
				goDecls(t, f, decls[dir], bodiless[dir], imports[dir])
			}
		}
		for dir, fns := range irs {
			for _, fn := range fns {
				for _, c := range fn.plain {
					t.Errorf("%s: the IR body %s in %s calls %s, another package's function, by a plain call", name, fn.name, dir, c)
				}
				for _, q := range fn.quals {
					p, n, _ := cutLast(q, ".")
					for other := range decls {
						if other == "." || !strings.HasSuffix(p, "/"+other) {
							continue
						}
						generic = true
						if !decls[other][n] {
							t.Errorf("%s: the IR body %s in %s names %s, which %s does not declare", name, fn.name, dir, q, other)
						}
						if !imports[dir][p] {
							t.Errorf("%s: the IR body %s in %s names %s, which %s does not import", name, fn.name, dir, q, dir)
						}
					}
				}
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
	if !twoPackages || !foreign || !generic {
		t.Fatalf("no program has IR bodies in two packages (%v), an IR name of another package (%v) or a generic name of a package of the split (%v): the property is vacuous", twoPackages, foreign, generic)
	}
}

// cutLast is s around the last sep.
func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

// irNames are an IR function's name and the package-scope names its body
// spells: the functions it calls and the named part of its types; quals
// the qualified names it spells (PATH.Name, a pcall's or a type's), and
// plain the qualified names it calls by a plain call.
type irNames struct {
	name         string
	names        []string
	quals, plain []string
}

// qualName is a qualified name in a type spelling: an import path with a
// slash, then .Name.
var qualName = regexp.MustCompile(`[A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+\.[A-Za-z_][A-Za-z0-9_]*`)

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
				r.quals = append(r.quals, qualName.FindAllString(s, -1)...)
			}
		}
		var walk func(v any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				switch v["k"] {
				case "call":
					n := v["name"].(string)
					r.names = append(r.names, n)
					if strings.Contains(n, "/") {
						r.plain = append(r.plain, n)
					}
				case "pcall":
					r.quals = append(r.quals, v["t"].(string)+"."+v["name"].(string))
					typ(v["v"]) // its type arguments
					for _, x := range v {
						walk(x)
					}
					return // t is its path, not a type
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
// without a body to bodiless, a method as T.M, and its import paths to
// imports.
func goDecls(t *testing.T, f lower.File, decls, bodiless, imports map[string]bool) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), f.Path, f.Data, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s does not parse: %v", f.Path, err)
	}
	for _, s := range file.Imports {
		p, _ := strconv.Unquote(s.Path.Value)
		imports[p] = true
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
			if d.Body == nil || irPlaceholder(d.Body) {
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

// irPlaceholder reports a body that is exactly the placeholder a generic
// declaration whose body the IR gives takes (lower's irPlaceholder): one
// call of panic with the string the patched compile's reader matches
// (toolchain/ir/_go/.../ergoir/ergoir_placeholder.go, ergoPlaceholder).
func irPlaceholder(body *ast.BlockStmt) bool {
	if len(body.List) != 1 {
		return false
	}
	st, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := st.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return false
	}
	if fun, ok := call.Fun.(*ast.Ident); !ok || fun.Name != "panic" {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	text, err := strconv.Unquote(lit.Value)
	return err == nil && text == "ergoir: no IR body"
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
