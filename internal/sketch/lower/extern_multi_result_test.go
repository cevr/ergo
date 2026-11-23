package lower

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestStdTupleResultsLower: a std body's Go results, one value or several
// and then an error or not, lower to Ergo's one value (a tuple of several)
// on both paths. The emitter's stdResult and the IR's bind (std call, then
// tupleErrBind) each take a hand-built v1.Body of a seeded shape: arity 1
// to 3, an error after the values or none, generic or not. Each output is
// type-checked with go/types against a Go body of that shape, and the two
// paths agree on the Go type of the value they make.
//
// The bug (PROCESS-RUN, lowering-bug.md): task's TaskExit returned (int,
// string, error), and the emitter wrote `v, err := ergo.TaskExit[T](t)`,
// two variables for three results; the IR refused the call. The checker
// does not tell a typed failure E from go.Error at these sites: both are
// Go's error in the body's results, so the property draws the Go shape
// only. The real body proves it end to end when PROCESS-RUN reverts its
// workaround (TestStdLawsRun).
//
// Mutants it alone catches: emr-emitter-tuple-error (stdResult's
// (A, B, error) case removed), emr-ir-bind-error (tupleErrBind leaves the
// error out of its tdef), emr-ir-call-error (stdTupleAdmits, the IR std
// call's verdict, counts the error as a value). Replay one seed with
// -run '^TestStdTupleResultsLower$/^seed<N>$'.
func TestStdTupleResultsLower(t *testing.T) {
	pool := []*check.Basic{check.Typ[check.Bool], check.Typ[check.String], check.Typ[check.Int], check.Typ[check.Int64], check.Typ[check.Float64]}
	for seed := range 64 {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(uint64(seed), 0x656d72))
			n, fallible, generic := 1+r.IntN(3), r.IntN(2) == 0, r.IntN(2) == 0
			if seed < 2 {
				n, fallible = 2+seed, true // the shape of the bug, always drawn
			}
			var elems []check.Type
			var goRes []string
			for range n {
				e := pool[r.IntN(len(pool))]
				elems, goRes = append(elems, e), append(goRes, e.Name)
			}
			body := v1.Body{Results: goRes}
			if fallible {
				body.Results = append(slices.Clone(goRes), "error")
			}
			var result check.Type = elems[0]
			if n > 1 {
				result = &check.Tuple{Types: elems}
			}
			name, targs := "Pair", ""
			tparams := ""
			if generic {
				targs, tparams = "int", "[T any]"
			}
			decl := "func Pair" + tparams + "() (" + strings.Join(body.Results, ", ") + ") { panic(0) }\n"
			shape := fmt.Sprintf("seed %d: Go body %s; replay: go test -run '^TestStdTupleResultsLower$/^seed%d$' ./internal/sketch/lower", seed, strings.TrimSuffix(decl, " { panic(0) }\n"), seed)

			// The emitter: stdResult's text is the value (and error) a
			// function of the Ergo result returns.
			fc := newFuncCtx(emptyLowerer(t), newNamer(nil), false)
			callText := name
			if targs != "" {
				callText += "[" + targs + "]"
			}
			text := fc.stdResult(body, result, "", nil, func(string) string { return callText + "()" })
			emitType := fc.goType(result)
			sig := emitType
			if fallible {
				sig = "(" + emitType + ", error)"
			}
			emitted := "package p\n\n" + decl + "\nfunc f() " + sig + " {\n\treturn " + text + "\n}\n"
			if err := goTypes(emitted, nil); err != nil {
				t.Fatalf("%s\nemitter's Go does not type-check: %v\n%s", shape, err, emitted)
			}

			// The IR, for a tuple: bind's form of a fallible call
			// (tupleErrBind), tdef's of an infallible one. One result is
			// bind's def2, which no tuple reaches.
			if n < 2 {
				return
			}
			tuple := result.(*check.Tuple)
			// The IR's std call admits the shape, an error's only under
			// bind (stdTupleAdmits, the call site's verdict).
			if got := stdTupleAdmits(body, tuple, true); got != tupleAdmitted {
				t.Fatalf("%s\nthe IR's std call refuses it under bind (verdict %d)", shape, got)
			}
			if got := stdTupleAdmits(body, tuple, false); fallible && got != tupleOutsideBind || !fallible && got != tupleAdmitted {
				t.Fatalf("%s\nthe IR's std call outside bind gives verdict %d", shape, got)
			}
			b := (&irState{}).newBuilder(fc, nil)
			b.fn, b.names = &irFunc{Name: "f"}, newNamer(nil)
			p := irPos{0, 1, 1}
			call := &irExpr{K: "pcall", Pos: p, T: "example.com/emr", Name: name, V: targs}
			var stmts []*irStmt
			var val *irExpr
			if fallible {
				e := b.temp("err", "error", p)
				pre, v := b.tupleErrBind(call, tuple, e, nil, p)
				fail := &irStmt{K: "if", Pos: p, X: &irExpr{K: "bin", Pos: p, Op: "!=", X: &irExpr{K: "local", Pos: p, I: e}, Y: &irExpr{K: "zero", Pos: p, T: "error"}},
					Then: []*irStmt{{K: "fail", Pos: p, X: &irExpr{K: "local", Pos: p, I: e}}}}
				stmts, val = append(pre, fail), &irExpr{K: "local", Pos: p, I: v}
			} else {
				stmts, val = b.tdef(call, tuple, nil, p)
			}
			if got, want := strings.ReplaceAll(b.tupleType(tuple, nil), " ", ""), strings.ReplaceAll(emitType, " ", ""); got != want {
				t.Fatalf("%s\nthe IR's value is %s, the emitter's %s", shape, got, want)
			}
			b.fn.Body = append(stmts, &irStmt{K: "ret", Pos: p, X: val})
			ir, err := json.Marshal(irFile{V: 1, Files: []string{"main.ergo"}, Funcs: []irFunc{*b.fn}})
			if err != nil {
				t.Fatal(err)
			}
			stub := "package emr\n\n" + decl
			mainGo := "package main\n\nimport \"example.com/emr\"\n\nvar _ = emr.Pair" + map[bool]string{true: "[int]", false: ""}[generic] + "\n\nfunc f() " + sig + "\n\nfunc main() {}\n"
			imp := stubImporter{"example.com/emr": stub}
			out, err := IRGoText([]File{{Path: "main.go", Data: []byte(mainGo)}, {Path: IRFileName, Data: ir}}, imp)
			if err != nil {
				t.Fatalf("%s\nIR text: %v\n%s", shape, err, ir)
			}
			if err := goTypes(string(out[0].Data), imp); err != nil {
				t.Fatalf("%s\nthe IR's Go does not type-check: %v\n%s", shape, err, out[0].Data)
			}
		})
	}
}

// emptyLowerer is the lowerer of an empty program, before its naming
// pass: the one a lone function context spells its types with.
func emptyLowerer(t *testing.T) *lowerer {
	t.Helper()
	src := "func main() {}\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("checker diagnostics %v", diags)
	}
	return newLowerer(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
}

// goTypes type-checks one Go file with go/types, importing by imp.
func goTypes(src string, imp types.Importer) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	_, err = (&types.Config{Importer: imp}).Check(f.Name.Name, fset, []*ast.File{f}, nil)
	return err
}

// stubImporter imports each package by its one Go file's source.
type stubImporter map[string]string

func (s stubImporter) Import(path string) (*types.Package, error) {
	src, ok := s[path]
	if !ok {
		return nil, fmt.Errorf("no package %s", path)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path+".go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return (&types.Config{}).Check(path, fset, []*ast.File{f}, nil)
}
