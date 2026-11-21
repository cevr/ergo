package sketchfront

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRSplitSpellsWhatItCopies is the smallest repro of batch 22's Q13,
// two bugs of the IR split under Q5(b), on the three examples it broke:
//   - a type spelling's names inside a map or a function type were not
//     respelled (draw-gui's map[int]Placed, regex's
//     func(int, syntax.History) Option[ended]): the IR body named a type of
//     another package unqualified, and the patched compile refused it
//     ("Placed" is not a type). Every name of every type spelling of an IR
//     body is one its own package declares, unless another package
//     declares it alone (mutant q13-map-type-unrespelled);
//   - a package let of another package copied as `var X = q.X` where q
//     declares X with no value, which ergoInit sets inside the root task
//     after every package's Go initialization: address-routes's people.All was
//     an empty list in the root, so its screen said "person 3: not found"
//     (want "person 3: Carol Davis, Manager"). No `var X = q.X` reads such
//     a variable (mutant q13-unset-let-copied); the IR body reads it
//     through a forwarding function of its package when it runs, which
//     address-routes's cases check by running (mutant q13-getter-unused,
//     examples:address-routes).
//
// The three must split, so neither check is vacuous. It checks the
// lowered files in process.
func TestIRSplitSpellsWhatItCopies(t *testing.T) {
	for _, name := range []string{"draw-gui", "regex", "address-routes"} {
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
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rep.Reason != "" {
			t.Fatalf("%s: the IR split refuses: %s", name, rep.Reason)
		}
		decls := map[string]map[string]bool{}  // by folder, its package-scope names
		unset := map[string]map[string]bool{}  // by folder, its variables with no value
		copies := map[string]map[string]bool{} // by folder, the names its `var X = q.X` read
		irs := map[string][]byte{}
		for _, f := range files {
			d := path.Dir(f.Path)
			if path.Base(f.Path) == lower.IRFileName {
				irs[d] = f.Data
				continue
			}
			if !strings.HasSuffix(f.Path, ".go") {
				continue
			}
			for _, m := range []map[string]map[string]bool{decls, unset, copies} {
				if m[d] == nil {
					m[d] = map[string]bool{}
				}
			}
			goVars(t, f, decls[d], unset[d], copies[d])
		}
		for d, names := range copies {
			for n := range names {
				for other, u := range unset {
					if other != d && u[n] {
						t.Errorf("%s: package %s copies %s of %s, which declares it with no value: the copy holds its zero value", name, d, n, other)
					}
				}
			}
		}
		for d, data := range irs {
			for _, n := range typeNames(t, data) {
				if decls[d][n] {
					continue
				}
				for other, ds := range decls {
					if other != d && ds[n] {
						t.Errorf("%s: an IR body in %s spells the type name %s, which %s declares and %s does not", name, d, n, other, d)
					}
				}
			}
		}
	}
}

// goVars reads a Go file's package-scope names into decls, its variables
// declared with no value into unset, and the names its `var X = q.X`
// copies read into copies.
func goVars(t *testing.T, f lower.File, decls, unset, copies map[string]bool) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), f.Path, f.Data, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s does not parse: %v", f.Path, err)
	}
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				decls[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					decls[s.Name.Name] = true
				case *ast.ValueSpec:
					for i, n := range s.Names {
						decls[n.Name] = true
						if len(s.Values) == 0 {
							unset[n.Name] = true
						} else if sel, ok := s.Values[i].(*ast.SelectorExpr); ok {
							if _, ok := sel.X.(*ast.Ident); ok {
								copies[sel.Sel.Name] = true
							}
						}
					}
				}
			}
		}
	}
}

// typeNames are the unqualified names of every type spelling in an IR
// file's bodies: each identifier of a "t", "type" or "v" string, or of a
// case's types, without a dot or slash.
func typeNames(t *testing.T, data []byte) []string {
	t.Helper()
	var f struct {
		Funcs []any `json:"funcs"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	var out []string
	spelling := func(s string) {
		for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
			return !(r == '_' || r == '.' || r == '/' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		}) {
			if strings.ContainsAny(tok, "./") || tok[0] >= '0' && tok[0] <= '9' {
				continue
			}
			out = append(out, tok)
		}
	}
	var walk func(v any, key string)
	walk = func(v any, key string) {
		switch v := v.(type) {
		case map[string]any:
			if v["k"] == "pcall" {
				// t is the function's import path; v its type arguments.
				for k, x := range v {
					if k != "t" {
						walk(x, k)
					}
				}
				return
			}
			for k, x := range v {
				if k == "v" && v["k"] == "const" {
					continue // a constant's value, not a type
				}
				walk(x, k)
			}
		case []any:
			for _, x := range v {
				walk(x, key)
			}
		case string:
			if key == "t" || key == "type" || key == "v" || key == "types" {
				spelling(v)
			}
		}
	}
	walk(f.Funcs, "")
	return out
}
