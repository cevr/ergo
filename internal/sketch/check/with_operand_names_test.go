package check_test

// checker-fuzz: a with operand's bare name that names no package context
// and no value (a function, a type, a prelude function, a row capability,
// a type parameter, nothing) is an "unknown context" error in typing,
// which reads no value and records no use. The reference walk takes it as
// a name too, so it adds no reference edge and no use that typing would
// not resolve when it checks the enclosing callback frame. The two fuzz
// inputs (testdata/fuzz/FuzzCheck/fuzz-found-637ee1cf23c5e17e and
// fuzz-found-c6cdcf917bdf4ba5) are members of this class: the operand
// names a package function inside a with block or a function literal.

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

const withOperandHeader = contextShadowPrelude + `let pv = Tag{Text: "p"}
func fn() {}
`

// TestWithOperandNamesAgreeWithTyping generates every operand kind in
// every frame around it, bare and parenthesised, and asserts that the
// reference walk and typing agree on each (RefWalkProblems), and that the
// class G order oracle holds.
func TestWithOperandNamesAgreeWithTyping(t *testing.T) {
	// value: typing reads the operand as a context value or binds a
	// package context; otherwise it reports a with-operand error at it.
	operands := []struct {
		name, op string
		value    bool
	}{
		{"package-function", "fn", false},
		{"own-function", "g", false},
		{"type", "Quiet", false},
		{"prelude-function", "len", false},
		{"unknown", "nope", false},
		{"row-capability", "A", false},
		{"type-parameter", "T", false},
		{"package-var", "pv", true},
		{"local-var", "loc", true},
		{"package-context", "W", true},
	}
	frames := []struct{ name, src string }{
		{"body", "with %s { 1 }"},
		{"with-block", "with W { with %s { 1 } }"},
		{"func-literal", "let k = func() { with %s { 1 } }\n\tk()"},
		{"nested-literal", "with W { let k = func() { with W { with %s { 1 } } }\n\tk() }"},
	}
	for _, o := range operands {
		for _, f := range frames {
			for _, paren := range []bool{false, true} {
				op := o.op
				if paren {
					op = "(" + op + ")"
				}
				body := fmt.Sprintf(f.src, op)
				src := withOperandHeader + "func g[T]() needs A {\n\tlet loc = Tag{Text: \"l\"}\n\t_ = loc\n\t" + body + "\n}\n"
				t.Run(fmt.Sprintf("%s/%s/paren=%v", o.name, f.name, paren), func(t *testing.T) {
					// Control: the program reaches the operand rule it names.
					at := strings.LastIndex(src, op) + strings.Index(op, o.op)
					_, d := check.CheckSource(src)
					diagnosed := false
					for _, x := range d {
						if x.Offset == at {
							diagnosed = true
						}
					}
					if diagnosed == o.value {
						t.Fatalf("operand %s at %d: diagnosed %v, want %v: %s\n%s", o.op, at, diagnosed, !o.value, describe(d), src)
					}
					if p := check.RefWalkProblems(src); len(p) > 0 {
						t.Fatalf("reference walk: %s\n%s", strings.Join(p, "; "), src)
					}
					if p := check.OrderProblems(src, 2); len(p) > 0 {
						t.Fatalf("declaration order: %s", p[0])
					}
				})
			}
		}
	}
}
