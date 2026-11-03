package check_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestRowVariableWitnesses: the round-11 review's witnesses (class F).
// cache-witness.ergo: initializer discovery builds Box{f} before f's row is
// inferred; Box[func()]{f} must still be refused (S108), since f fails with
// Bang. row-gap-witness.ergo: a row built during discovery, error[Fault[f's
// type]], must not make a valid later instantiation wider than itself.
func TestRowVariableWitnesses(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "classf", "*.ergo"))
	if len(files) < 67 {
		t.Fatalf("want the 44 witnesses and controls of rounds 12 to 16, found %v", files)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			_, diags := check.CheckSource(string(data))
			want, have := expected(string(data)), got(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
			}
		})
	}
}

// wideSignature writes the round-11 review's M2 family: one unexported
// function wide with m int parameters and n small functions that each
// write Box{wide}. wide's row is final before the small functions are
// checked, so its signature's identity is built once.
func wideSignature(n, m int) string {
	var b strings.Builder
	b.WriteString("type Box[T] { F T }\nfunc wide(")
	for i := range m {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "p%d int", i)
	}
	b.WriteString(") {}\n")
	for i := range n {
		fmt.Fprintf(&b, "func f%d() { _ = Box{wide} }\n", i)
	}
	return check.Decided(b.String())
}

// TestFinalSignatureShared: the signature of a function whose row is
// final is interned once, not once per later function (class F: no
// epochs). Round 11 rebuilt it per function: n(n+3) signature key fields
// (3780, 58320 and 231840 at n=m=60, 240 and 480). Counted, not timed.
func TestFinalSignatureShared(t *testing.T) {
	var prevN, prevWork int
	for _, n := range []int{60, 240, 480} {
		src := wideSignature(n, n)
		work, d := check.SignatureWork(src)
		if len(d) > 0 {
			t.Fatalf("n=m=%d: diagnostics %s", n, describe(d))
		}
		t.Logf("n=m=%d bytes=%d signature key fields=%d", n, len(src), work)
		if work > 8*(n+n) {
			t.Errorf("n=m=%d: %d signature key fields, more than 8 per function and parameter", n, work)
		}
		if prevN > 0 && work*prevN*10 > prevWork*n*11 {
			t.Errorf("n=m %d→%d: signature work grows from %d to %d, faster than the source", prevN, n, prevWork, work)
		}
		prevN, prevWork = n, work
	}
}

// shuffledInterface writes the round-11 review's M3 family: a union
// variant V[I], I an operation type of m methods in a shuffled order, and a
// switch that builds the union's variant index. The review's variant was
// an interface literal, which the decided language removed (#196): the
// named type keeps the shape, and the literal's path is check-v1's to
// delete (ruling C).
func shuffledInterface(m int) string {
	var b strings.Builder
	b.WriteString("type V[T] {}\ntype I { ")
	rng := rand.New(rand.NewPCG(1, 2))
	for _, i := range rng.Perm(m) {
		fmt.Fprintf(&b, "M%d(), ", i)
	}
	b.WriteString("}\ntype U = V[I] | V[int]\nfunc F(x U) int {\n\tmatch x {\n\tcase V[int]:\n\t\treturn 1\n\tcase _:\n\t\treturn 0\n\t}\n}\n")
	return check.Decided(b.String())
}

// TestInterfaceIndexNoSort: an interface literal's methods are sorted once,
// when its type is resolved; building the variant index reads that order
// and sorts nothing (round 11 M3). Round 11 made 79020, 171549 and 296358
// uncharged sort comparisons inside index construction at 6000, 12000 and
// 20000 methods.
func TestInterfaceIndexNoSort(t *testing.T) {
	var prevN, prevOps int
	for _, n := range []int{6000, 12000, 20000} {
		src := shuffledInterface(n)
		ops, cmps, d := check.IndexSortWork(src)
		if len(d) > 0 {
			t.Fatalf("methods=%d: diagnostics %s", n, describe(d))
		}
		t.Logf("methods=%d bytes=%d variant ops=%d sort comparisons in index construction=%d", n, len(src), ops, cmps)
		if cmps != 0 {
			t.Errorf("methods=%d: %d sort comparisons while the index is built", n, cmps)
		}
		if ops > 4*n {
			t.Errorf("methods=%d: %d variant operations, more than 4 per method", n, ops)
		}
		if prevN > 0 && ops*prevN*10 > prevOps*n*11 {
			t.Errorf("methods %d→%d: variant operations grow from %d to %d, faster than the source", prevN, n, prevOps, ops)
		}
		prevN, prevOps = n, ops
	}
}
