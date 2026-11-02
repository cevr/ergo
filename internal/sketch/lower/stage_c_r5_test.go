package lower

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Stage C round 4 review T1: forms the corpus guard found that checked
// clean and failed lowering with an internal compiler error or an
// ownerless refusal. Each now runs.
func TestErgoRunCorpusGuardForms(t *testing.T) {
	wantRun(t, `type S {}
type P {}
type J { M() int }
type A[T] { M() T }

func M(_ S) int { return 1 }
func M(_ *P) int { return 7 }
func FnI[T](x A[T]) T { return x.M() }
func use(j J) int { j.M() }

func unnamed(S, int) int { 2 }
func apply(f func(int, int) int) int { f(1, 2) }

func main() needs Console {
	let s = S
	let mut p = P
	let q = &p
	if unnamed(s, 1) == 2 && apply(func(a int, b int) int { b }) == 2 {
		Console.Println("unnamed parameters")
	}
	if use(q) == 7 {
		Console.Println("pointer source")
	}
	if FnI(s) == 1 && FnI(q) == 7 {
		Console.Println("generic operation type")
	}
}
`, "unnamed parameters\npointer source\ngeneric operation type\n")
}

// The guard's parenthesised instance, once a deferred refusal, runs since
// check-v1 stage E1: Info gives its call the instance's type arguments
// (C7, C9).
func TestCorpusGuardParenthesisedInstance(t *testing.T) {
	wantRun(t, `func Fn[T](x T) T { x }

func main() needs Console {
	if ((Fn[int]))(1) == 1 && (Fn[string])("s") == "s" {
		Console.Println("parenthesised instance")
	}
}
`, "parenthesised instance\n")
}

// Round 4 review T2: the corpus reader enumerates every declaration form
// of a check test: a package-level const or var, a case table, a struct
// field, a call argument, a := in a function, and a literal after a
// named prefix.
func TestCheckCorpusEnumeration(t *testing.T) {
	dir := t.TempDir()
	src := "package check_test\n\n" +
		"const pkgConst = `func A() {}`\n" +
		"var pkgVar = \"func B() {}\"\n" +
		"const decls = \"type D {}\\n\"\n" +
		"var table = []struct{ src string }{{\"func C() {}\"}}\n" +
		"func TestX(t *testing.T) {\n" +
		"\tlocal := \"func E() {}\"\n" +
		"\tcheck(decls + \"func F(d D) {}\")\n" +
		"\t_ = local\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, files, total, k := checkCorpus(t, dir, 1000)
	if files != 1 {
		t.Fatalf("read %d files", files)
	}
	have := map[string]bool{}
	for _, s := range got {
		have[s] = true
	}
	for _, want := range []string{"func A() {}\n", "func B() {}\n", "type D {}\n", "func C() {}\n", "func E() {}\n", "type D {}\nfunc F(d D) {}\n"} {
		if !have[want] {
			t.Errorf("the corpus misses %q; it has %q", want, strings.Join(got, "|"))
		}
	}
	if k != 1 {
		t.Fatalf("under the limit the stride is %d", k)
	}
	// Round 5 review T3 residual: past the limit, the reader builds only
	// every k-th candidate, the same ones on every run, never more than
	// the limit.
	limit := total / 3
	first, _, total2, k2 := checkCorpus(t, dir, limit)
	again, _, _, _ := checkCorpus(t, dir, limit)
	if total2 != total || k2 != (total+limit-1)/limit || len(first) > limit || strings.Join(first, "|") != strings.Join(again, "|") {
		t.Fatalf("limit %d of %d: stride %d, built %d, stable %v", limit, total, k2, len(first), strings.Join(first, "|") == strings.Join(again, "|"))
	}
}
