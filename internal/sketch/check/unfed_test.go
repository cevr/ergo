package check_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestUnfedLawIsAnError is the rule's text and position (LAW-UNFED): a law
// no case can feed is a check-time error where the missing value is
// declared, not a skip at test time. A provider with no generator is
// refused at its implements entry, unless a named context of its package
// builds it; a law's own parameter with no generator is refused at the
// parameter. Among several builders, one that wraps its own type is no
// candidate. Mutants lu-provider-unreported, lu-provider-before-contexts,
// lu-param-unreported and lu-wrapper-candidate are its kills.
func TestUnfedLawIsAnError(t *testing.T) {
	const shape = `type Shape {
	Area() int,
	law Positive() { Area() > 0 },
}
`
	rows := []struct {
		name, src string
		want      []string
	}{
		{
			name: "provider with no generator",
			src: shape + `type Fn { f func(int) int } implements Shape
func Area(s Fn) int { s.f(1) }
`,
			want: []string{"5:40 S108 law Shape.Positive cannot run at Fn: its values have no generator: func(int) int is a function type; bind one in a named context of a test file, let Name = context { Shape: … }, and the law runs under it (types.md, Laws 10)"},
		},
		{
			name: "provider a named context builds",
			src: shape + `type Fn { f func(int) int } implements Shape
func Area(s Fn) int { s.f(1) }
let Wired = context { Shape: Fn{f: func(n int) int { n }} }
`,
		},
		{
			name: "provider with a constructor",
			src: shape + `type Fn { f func(int) int } implements Shape
func NewFn() Fn { Fn{f: func(n int) int { n }} }
func Area(s Fn) int { s.f(1) }
`,
		},
		{
			// fstest's fs.Root: NewRecorder wraps a Root, so NewTree is
			// the one builder, and the wrapper's law is fed by it.
			name: "a builder that wraps its own type is no candidate",
			src: shape + `type Sq { n int } implements Shape
func NewSq() Sq { Sq{n: 1} }
func Area(s Sq) int { s.n }
type Logged { inner Shape } implements Shape
func NewLogged(inner Shape) Logged { Logged{inner: inner} }
func Area(l Logged) int { l.inner.Area() }
test Any(s Shape) { assert s.Area() > 0 }
`,
		},
		{
			name: "top-level law parameter",
			src: `law Applies(f func(int) int) { f(1) == f(1) }
`,
			want: []string{"1:13 S108 law Applies cannot run: its parameter f has no generator: func(int) int is a function type (types.md, Laws 10)"},
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var got []string
			for _, d := range checkDiagList(t, row.src) {
				got = append(got, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
			}
			if strings.Join(got, "\n") != strings.Join(row.want, "\n") {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(row.want, "\n"))
			}
		})
	}
}
