package syntax

import (
	"errors"
	"testing"
)

// TestLocalDeclNamesTheLetForm: a Go local declaration is S001 at its
// token with the let form that replaces it, `let mut` exactly when a later
// statement of its block assigns the name, and the fix that writes it
// (rows: the diagnostic's text and position, the owner's var tip of
// 2026-11-20).
func TestLocalDeclNamesTheLetForm(t *testing.T) {
	const varMsg = "Ergo has no var; declare with `let x = e`, `let x T = e` or `let mut x = e`; here "
	const defMsg = "Ergo declares names with `let x = e`, or `let mut x = e` for a name that is reassigned; here "
	for _, r := range []struct {
		name, src     string
		line, col     int
		message, text string
	}{
		{"var-reassigned", "func f() {\n\tvar count = 0\n\tcount = count + 1\n}\n", 2, 2, varMsg + "`let mut count = 0`, since count is reassigned", "let mut"},
		{"var-typed", "func f() int {\n\tvar count int = 0\n\tcount\n}\n", 2, 2, varMsg + "`let count int = 0`, since count is never reassigned", "let"},
		{"var-zero", "func f() int {\n\tvar count int\n\tif true {\n\t\tcount += 2\n\t}\n\tcount\n}\n", 2, 2, varMsg + "`let mut count int`, since count is reassigned", "let mut"},
		{"define", "func f() int {\n\tcount := 0\n\tcount\n}\n", 2, 8, defMsg + "`let count = 0`, since count is never reassigned", "let count ="},
		{"define-field", "func f() {\n\tp := P{X: 1}\n\tp.X = 2\n}\n", 2, 4, defMsg + "`let mut p = P{X: 1}`, since p is reassigned", "let mut p ="},
		{"define-compare", "func f() {\n\tx := 1\n\tif x == 1 {\n\t\tuse(x)\n\t}\n}\n", 2, 4, defMsg + "`let x = 1`, since x is never reassigned", "let x ="},
	} {
		t.Run(r.name, func(t *testing.T) {
			_, err := Parse(r.src)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("%v is not *Error", err)
			}
			if e.Line != r.line || e.Column != r.col || e.Message != r.message {
				t.Fatalf("got %d:%d %q\nwant %d:%d %q", e.Line, e.Column, e.Message, r.line, r.col, r.message)
			}
			if e.Fix == nil || e.Fix.Text != r.text {
				t.Fatalf("fix %+v, want text %q", e.Fix, r.text)
			}
		})
	}
}
