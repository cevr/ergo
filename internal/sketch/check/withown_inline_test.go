package check_test

import (
	"strings"
	"testing"
)

// Inline providers use the same ownership fact and freshness boundary as
// named contexts. Each expected identity describes a user-visible release.
func TestContextOwnershipInlineAcquisitions(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"explicit calls", `func first() First { First }
func second() Second { Second }
func f() { with wire { A: first(), B: second() } { A.Use(); B.Use() } }
`, "f: First[A]! Second[B]"},
		{"shared entry", `func pair() Pair { Pair }
func f() { with wire { A, B: pair() } { B.Use(); A.Use() } }
`, "f: Pair[A,B]!"},
		{"unreached entry", `func f() { with wire { A: First, B: Second } { B.Use() } }
`, "f: Second[B]"},
		{"replace one shared capability", `let Base = wire { A, B: Pair }
func f() { with wire { replace A: Other, Base } { A.Use(); B.Use() } }
`, "f: Pair[B]! Other[A]!"},
		{"fallible constructor", `type Fault {}
func first(ok bool) First fails Fault { if ok { return First }; return Fault }
func f() fails Fault { with wire { A: try first(true) } { A.Use() } }
`, "f: First[A]!"},
		{"local constructor argument", `func first(n int) First { let _ = n; First }
func f() { let n = 1; with wire { A: first(n) } { A.Use() } }
`, "f: First[A]!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg, ds := checkOwn(t, tc.src)
			if len(ds) != 0 {
				t.Fatalf("diagnostics: %v", ds)
			}
			if got := strings.Join(acquisitions(t, pkg), "\n"); got != tc.want {
				t.Fatalf("inline acquisitions: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestContextOwnershipInlineRefusals(t *testing.T) {
	for _, tc := range []struct{ name, src, code string }{
		{"borrowed local", `func f() { let a = First; with wire { A: a } { A.Use() } }`, "S109"},
		{"borrowed constructor", `let old = First
func first() First { old }
func f() { with wire { A: first() } { A.Use() } }`, "S109"},
		{"release need", `type Res {}
func Use(_ Res) {}
func Close(_ Res) needs Log { Log.Say("close") }
func f() { with wire { A: Res } { A.Use() } }`, "S102"},
		{"exit action", `import scope
func f() { with wire { A: First } { scope.OnExit() { A.Use() } } }`, "S108"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ds := checkOwn(t, tc.src)
			for _, d := range ds {
				if string(d.Code) == tc.code {
					return
				}
			}
			t.Fatalf("want %s, got %v", tc.code, ds)
		})
	}
}
