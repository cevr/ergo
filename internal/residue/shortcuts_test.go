package residue_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestShortcutsAreRegistered holds the lowerer's shortcut registry
// (internal/sketch/lower/shortcuts.go; ASSERT1, plan IR-G §2) to the
// source. A shortcut is a lowering that a proof selects; the registry
// names, per shortcut, its trust class, its one admission function, its
// reference lowering, its agreement test and its mutant. The rule:
//
//   - every call takes("x") in the lowerer names a row, and sits in that
//     row's admission function: no other function decides a site of x;
//   - every row has a class, an admission function that asks takes for
//     it, a reference hook "var=value" whose variable the package
//     declares, an agreement test the package's tests declare, and a
//     ledger mutant of that name that the agreement test kills;
//   - a memory-class row names the runtime bodies it calls, each listed
//     in internal/residue/testdata/unsafe.txt, or, for a fact kind, the
//     Go pass that consumes the fact;
//   - every fact kind (a factKind constant, lower/facts.go) is a row of
//     its name whose admission is admitFact (FACT0).
//
// Mutants it alone catches: assert-shortcut-unregistered (lower's ledger:
// the row's name no longer matches its admission's takes); a row with its
// agreement test, its reference hook or its mutant missing.
func TestShortcutsAreRegistered(t *testing.T) {
	out, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(out))
	dir := filepath.Join(repo, "internal", "sketch", "lower")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	type takeCall struct {
		name, fn, at string
	}
	var takes []takeCall
	var kinds []takeCall // the fact kinds: value, constant, position
	funcs := map[string]bool{}
	vars := map[string]bool{}
	testFuncs := map[string]bool{}
	var rows []map[string]string
	unsafeOf := map[string][]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		test := strings.HasSuffix(name, "_test.go")
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if test {
					if strings.HasPrefix(d.Name.Name, "Test") && d.Recv == nil {
						testFuncs[d.Name.Name] = true
					}
					continue
				}
				funcs[d.Name.Name] = true
				if d.Body == nil {
					continue
				}
				ast.Inspect(d.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "takes" || len(call.Args) != 1 {
						return true
					}
					lit, ok := call.Args[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s: takes is called with %s, not a literal name: the rule cannot see which shortcut it asks for", fset.Position(call.Pos()), exprText(call.Args[0]))
						return true
					}
					s, _ := strconv.Unquote(lit.Value)
					takes = append(takes, takeCall{s, d.Name.Name, fset.Position(call.Pos()).String()})
					return true
				})
			case *ast.GenDecl:
				if !test && d.Tok == token.CONST {
					for _, spec := range d.Specs {
						vs := spec.(*ast.ValueSpec)
						if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "factKind" {
							continue
						}
						for i := range vs.Names {
							if i < len(vs.Values) {
								if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
									s, _ := strconv.Unquote(lit.Value)
									kinds = append(kinds, takeCall{s, vs.Names[i].Name, fset.Position(lit.Pos()).String()})
								}
							}
						}
					}
				}
				if test || d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, id := range vs.Names {
						vars[id.Name] = true
						if id.Name != "shortcuts" || i >= len(vs.Values) {
							continue
						}
						list, ok := vs.Values[i].(*ast.CompositeLit)
						if !ok {
							t.Fatalf("%s: the registry is not a literal", fset.Position(id.Pos()))
						}
						for _, el := range list.Elts {
							row := map[string]string{}
							lit, ok := el.(*ast.CompositeLit)
							if !ok {
								t.Fatalf("%s: a registry row is not a literal", fset.Position(el.Pos()))
							}
							for _, kv := range lit.Elts {
								kv, ok := kv.(*ast.KeyValueExpr)
								if !ok {
									t.Fatalf("%s: a registry row field has no key", fset.Position(lit.Pos()))
								}
								key := exprText(kv.Key)
								switch v := kv.Value.(type) {
								case *ast.BasicLit:
									row[key], _ = strconv.Unquote(v.Value)
								case *ast.Ident:
									row[key] = v.Name
								case *ast.CompositeLit:
									for _, u := range v.Elts {
										if b, ok := u.(*ast.BasicLit); ok {
											s, _ := strconv.Unquote(b.Value)
											unsafeOf[fset.Position(lit.Pos()).String()] = append(unsafeOf[fset.Position(lit.Pos()).String()], s)
										}
									}
									row[key] = fset.Position(lit.Pos()).String()
								default:
									row[key] = "set"
								}
							}
							row["at"] = fset.Position(lit.Pos()).String()
							rows = append(rows, row)
						}
					}
				}
			}
		}
	}
	if len(rows) == 0 {
		t.Fatal("no registry found: internal/sketch/lower declares no var shortcuts")
	}
	ledger := lowerLedger(t, repo)
	unsafeTxt, err := os.ReadFile(filepath.Join(repo, "internal", "residue", "testdata", "unsafe.txt"))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]string{}
	for _, row := range rows {
		at, name := row["at"], row["name"]
		for _, k := range []string{"name", "class", "admission", "reference", "agreement", "mutant", "referenced"} {
			if row[k] == "" {
				t.Errorf("%s: the shortcut %q has no %s", at, name, k)
			}
		}
		if byName[name] != nil {
			t.Errorf("%s: two rows are named %q", at, name)
		}
		byName[name] = row
		if !slices.Contains([]string{"valueClass", "memoryClass", "detectorClass"}, row["class"]) {
			t.Errorf("%s: %q: the class %q is not valueClass, memoryClass or detectorClass", at, name, row["class"])
		}
		if a := row["admission"]; a != "" && !funcs[a] {
			t.Errorf("%s: %q: the admission function %s is not declared in internal/sketch/lower", at, name, a)
		}
		if ref := row["reference"]; ref != "" {
			v, _, ok := strings.Cut(ref, "=")
			if !ok || !vars[v] {
				t.Errorf("%s: %q: the reference %q is not var=value of a variable internal/sketch/lower declares", at, name, ref)
			}
		}
		if a := row["agreement"]; a != "" && !testFuncs[a] {
			t.Errorf("%s: %q: the agreement test %s is not a test of internal/sketch/lower", at, name, a)
		}
		if m := row["mutant"]; m != "" {
			killers, ok := ledger[m]
			if !ok {
				t.Errorf("%s: %q: the mutant %s is not in internal/sketch/lower/testdata/mutants.json", at, name, m)
			} else if !slices.Contains(killers, "lower:"+row["agreement"]) {
				t.Errorf("%s: %q: the agreement test %s is not among the killers %v of the mutant %s", at, name, row["agreement"], killers, m)
			}
		}
		bodies := unsafeOf[at]
		if row["class"] == "memoryClass" && len(bodies) == 0 && row["consumer"] == "" {
			t.Errorf("%s: %q is of the memory class and names no runtime body (unsafe) and no consumer of its fact", at, name)
		}
		for _, b := range bodies {
			if !strings.Contains(string(unsafeTxt), "\n"+b+" | ") {
				t.Errorf("%s: %q calls %s, which internal/residue/testdata/unsafe.txt does not list", at, name, b)
			}
		}
		asked := false
		for _, c := range takes {
			if c.name == name && c.fn == row["admission"] {
				asked = true
			}
		}
		if !asked {
			t.Errorf("%s: %q: its admission function %s never asks takes(%q)", at, name, row["admission"], name)
		}
	}
	// A fact kind leaves the build through admitFact only, and is a row
	// of its own (FACT0): a kind the export writes and the registry does
	// not list fails here.
	for _, k := range kinds {
		row := byName[k.name]
		switch {
		case row == nil:
			t.Errorf("%s: the fact kind %s (%q) is not a row of the registry: register it in shortcuts.go", k.at, k.fn, k.name)
		case row["admission"] != "admitFact":
			t.Errorf("%s: the fact kind %q is admitted by %s, not by admitFact, the one admission of facts", k.at, k.name, row["admission"])
		}
	}
	for _, c := range takes {
		row := byName[c.name]
		switch {
		case row == nil:
			t.Errorf("%s: takes(%q) in %s names no row of the registry: register the shortcut in shortcuts.go", c.at, c.name, c.fn)
		case row["admission"] != c.fn:
			t.Errorf("%s: takes(%q) is asked in %s, not in the shortcut's admission function %s", c.at, c.name, c.fn, row["admission"])
		}
	}
}

// lowerLedger is the killers of each mutant of internal/sketch/lower's
// ledger, by name.
func lowerLedger(t *testing.T, repo string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, "internal", "sketch", "lower", "testdata", "mutants.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ledger struct {
		Mutants []struct {
			Name    string   `json:"name"`
			Killers []string `json:"killers"`
		} `json:"mutants"`
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, m := range ledger.Mutants {
		out[m.Name] = m.Killers
	}
	return out
}

// exprText is a short text of x for a message.
func exprText(x ast.Expr) string {
	switch x := x.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.BasicLit:
		return x.Value
	case *ast.SelectorExpr:
		return exprText(x.X) + "." + x.Sel.Name
	}
	return "an expression"
}
