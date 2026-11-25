package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func TestLawAlwaysSometimesContextualSafety(t *testing.T) {
	src := `type always { Offset int }
type sometimes { Flag bool }
type Rule = always | sometimes

func checkRule(r Rule) int {
	match r {
	case always as a:
		a.Offset
	case sometimes as s:
		if s.Flag { 1 } else { 0 }
	}
}

func alwaysCall(x int) int { x + 1 }
func sometimesCall(x int) bool { x > 0 }

func useWords() bool {
	let a = always{Offset: 3}
	let s = sometimes{Flag: true}
	let n = alwaysCall(checkRule(a) + checkRule(s))
	sometimesCall(n)
}

func useLocalCalls() bool {
	let always = func(x int) bool { x > 0 }
	let sometimes = func(x int) bool { x < 0 }
	always(1) && sometimes(-1)
}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	f.Name = "ctx.ergo"
	_, diags := check.Check("main", []*syntax.File{f}, nil)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestLawAlwaysSometimesCheckerRules(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantCode string
		wantMsg  string
	}{
		{
			name: "valid statements and quantifiers including nested closure",
			src: `func runCb(f func(int) bool) bool { f(5) }

law Universal always(n int) {
	always n == n
	always (n == n || n != n)
	sometimes n > 0
	runCb(func(k int) bool {
		always k >= 0
		sometimes k == 5
		true
	})
}

law Existential sometimes(n int) {
	always n == n
	n > 0
}

type Counter {
	Get() int,
	law CanBePositive sometimes() { Get() > 0 },
	law AlwaysNonNeg always() { Get() >= 0 },
}
`,
		},
		{
			name: "always outside law in func",
			src: `func f(x int) bool {
	always x == x
	true
}
`,
			wantCode: "S108",
			wantMsg:  "always stands only in a law body",
		},
		{
			name: "sometimes outside law in func",
			src: `func f(x int) bool {
	sometimes x > 0
	true
}
`,
			wantCode: "S108",
			wantMsg:  "sometimes stands only in a law body",
		},
		{
			name: "always outside law in test",
			src: `test T {
	always true
}
`,
			wantCode: "S108",
			wantMsg:  "always stands only in a law body",
		},
		{
			name: "sometimes outside law in test",
			src: `test T {
	sometimes true
}
`,
			wantCode: "S108",
			wantMsg:  "sometimes stands only in a law body",
		},
		{
			name: "sometimes in closure inside test refused",
			src: `func apply(f func() bool) bool { f() }
test T {
	assert apply(func() bool {
		sometimes true
		true
	})
}
`,
			wantCode: "S108",
			wantMsg:  "sometimes stands only in a law body",
		},
		{
			name: "always non-bool operand",
			src: `law L(n int) {
	always n
	true
}
`,
			wantCode: "S108",
			wantMsg:  "always takes a bool, not int",
		},
		{
			name: "sometimes non-bool operand",
			src: `law L(s string) {
	sometimes s
	true
}
`,
			wantCode: "S108",
			wantMsg:  "sometimes takes a bool, not string",
		},
		{
			name: "sometimes on top-level rewrite law refused",
			src: `func id(x int) int { x }
law IdRewrite sometimes(x int) { id(x) } == { x }
`,
			wantCode: "S108",
			wantMsg:  "a rewrite law cannot take sometimes",
		},
		{
			name: "always on member rewrite law refused",
			src: `type Acc {
	Add(n int),
	law Joins always(a int, b int) { Add(a); Add(b) } == { Add(a + b) },
}
`,
			wantCode: "S108",
			wantMsg:  "a rewrite law cannot take always",
		},
		{
			name: "always inside member rewrite law refused",
			src: `type Acc {
	Add(n int),
	law Joins(a int, b int) { always a == a; Add(a); Add(b) } == { Add(a + b) },
}
`,
			wantCode: "S108",
			wantMsg:  "always stands only in a law body",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := syntax.Parse(tc.src)
			if err != nil {
				t.Fatalf("parse failed: %v\n%s", err, tc.src)
			}
			f.Name = "main.ergo"
			pkg, diags := check.Check("main", []*syntax.File{f}, nil)
			if tc.wantCode == "" {
				if len(diags) > 0 {
					t.Fatalf("unexpected diagnostics: %v", diags)
				}
				// Verify Law.Sometimes flag on the valid case.
				foundExist, foundUniv := false, false
				for _, l := range pkg.Laws {
					if l.Name == "Existential" || l.Name == "CanBePositive" {
						if !l.Sometimes {
							t.Errorf("law %s: Sometimes = false, want true", l.Name)
						}
						foundExist = true
					}
					if l.Name == "Universal" || l.Name == "AlwaysNonNeg" {
						if l.Sometimes {
							t.Errorf("law %s: Sometimes = true, want false", l.Name)
						}
						foundUniv = true
					}
				}
				if !foundExist || !foundUniv {
					t.Fatalf("missing expected laws in %v", pkg.Laws)
				}
				return
			}
			matched := false
			for _, d := range diags {
				if d.Code == tc.wantCode && strings.Contains(d.Message, tc.wantMsg) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("wanted %s containing %q, got: %v", tc.wantCode, tc.wantMsg, diags)
			}
		})
	}
}
