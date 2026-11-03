package check

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// TestInstantiationCycleSignatures: a method's signature instantiates its
// receiver's generic type like its body does. A growing instantiation in a
// parameter, a result, a bodiless (extern) declaration, an interface-typed parameter
// or a constraint, through renamed receiver parameters or across two
// receiver types, is an instantiation cycle (S108), as go/types reports
// (round-4 M1). The non-growing control stays clean.
func TestInstantiationCycleSignatures(t *testing.T) {
	// src is the Go text go/types reads; decided is the same program in the
	// decided syntax, which the checker reads: a method is a function whose
	// first parameter is the receiver (196m), and a bodiless declaration is
	// a function whose body only builds its result (a user extern takes
	// basic types only, C26).
	cases := []struct {
		name, src string
		cycle     bool
		decided   string
	}{
		{"parameter", `type R[P any] struct{}
func (R[P]) M(x R[[]P]) {}`, true, `type R[P] {}
func M[P](_ R[P], x R[[]P]) {}`},
		{"result-bodiless", `type R[P any] struct{}
func (R[P]) M() R[[]P]`, true, `type R[P] {}
func M[P](_ R[P]) R[[]P] { R[[]P] }`},
		{"interface-parameter", `type R[P any] struct{}
type I[P any] interface{ F(R[[]P]) }
func (R[P]) M(x I[P]) {}`, true, `type R[P] {}
type I[P] { F(R[[]P]) }
func M[P](_ R[P], x I[P]) {}`},
		{"renamed-receiver", `type R[P any] struct{}
func (R[Q]) M(x R[[]Q]) {}`, true, `type R[P] {}
func M[Q](_ R[Q], x R[[]Q]) {}`},
		{"cross-receiver", `type R[P any] struct{}
type S[Q any] struct{}
func (R[P]) M(x S[[]P]) {}
func (S[Q]) N(x R[Q]) {}`, true, `type R[P] {}
type S[Q] {}
func M[P](_ R[P], x S[[]P]) {}
func N[Q](_ S[Q], x R[Q]) {}`},
		{"function-signature-and-method-body", `type R[P any] struct{}
func F[T any](x R[[]T]) {}
func (R[P]) M() { var r R[P]; _ = r; F[P](R[[]P]{}) }`, true, `type R[P] {}
func F[T](x R[[]T]) {}
func M[P](_ R[P]) { let r R[P]; _ = r; F[P](R[[]P]{}) }`},
		{"nongrowing-control", `type R[P any] struct{}
func (R[P]) M(x R[P]) {}`, false, `type R[P] {}
func M[P](_ R[P], x R[P]) {}`},
		{"nongrowing-cross-control", `type R[P any] struct{}
type S[Q any] struct{}
func (R[P]) M(x S[P]) {}
func (S[Q]) N(x R[Q]) {}`, false, `type R[P] {}
type S[Q] {}
func M[P](_ R[P], x S[P]) {}
func N[Q](_ S[Q], x R[Q]) {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\n\n" + tc.src + "\n"
			_, diags := CheckSource(decided(tc.decided + "\n"))
			var ds []string
			for _, d := range diags {
				ds = append(ds, d.String())
			}
			if got := mentions(ds, "instantiation cycle"); got != tc.cycle {
				t.Fatalf("ergo reports an instantiation cycle = %v, want %v; diagnostics: %q", got, tc.cycle, ds)
			}
			if !tc.cycle && len(ds) > 0 {
				t.Fatalf("the control has diagnostics: %q", ds)
			}
			g := goErrors(src)
			if got := mentions(g, "instantiation cycle"); got != tc.cycle || !tc.cycle && len(g) > 0 {
				t.Fatalf("go/types disagrees with the case: %q", g)
			}
		})
	}
}

// goErrors checks src with go/types and returns its errors.
func goErrors(src string) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		return []string{err.Error()}
	}
	var errs []string
	conf := types.Config{Error: func(e error) { errs = append(errs, e.Error()) }}
	_, _ = conf.Check("p", fset, []*ast.File{file}, nil)
	return errs
}

func mentions(ds []string, sub string) bool {
	for _, d := range ds {
		if strings.Contains(d, sub) {
			return true
		}
	}
	return false
}
