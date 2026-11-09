package check

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// keyedForms are composite literals whose keys name the package var h%[1]d
// (round 16 M1): map keys, elided nested literals, maps of structs and
// structs as keys. The structs have a field named h0, which is a field
// name, never a reference to the var h0. The decided language has no fixed
// arrays and no anonymous structs (#196), so their two forms are gone.
var keyedForms = []string{
	"let m = Map[KT, int]{h%[1]d: 1}; return len(m)",
	"let m = []Map[KT, int]{{h%[1]d: 1}}; return len(m)",
	"let m = Map[KT, S]{h%[1]d: {h0: 1, K: h%[1]d}}; return len(m)",
	"let m = Map[S, int]{{h0: 1, K: h%[1]d}: 2}; return len(m)",
	"let m = Map[KT, Map[KT, int]]{h%[1]d: {h%[2]d: 1}}; return len(m)",
	"let m = []S{{K: h%[1]d, h0: 1}}; return len(m)",
	"let m = Map[KT, []S]{h%[1]d: {{h0: 1, K: h%[2]d}}}; return len(m)",
	"let m = S{h0: 1, K: h%[1]d}; return m.h0",
}

// keyedLiterals generates a source in which bodies refer to package vars
// only through composite-literal keys, in a random declaration order. Each
// var's type holds the inferred error row of a function, so a missing edge
// types a body before that row is final and rejects the valid source (round
// 16 M1, mapkey-var-row.ergo). The verdict is acceptance.
func keyedLiterals(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x6b6579))
	n := 2 + r.IntN(3)
	decls := []string{
		"type Bang {}",
		"type Key[T] {}",
		"func KeyOf[T](x T) Key[T] { Key[T] }",
		"func mark() fails Bang { return Bang }",
		"type S { h0 int, K KT }",
	}
	for i := range n {
		decls = append(decls,
			fmt.Sprintf("func a%d() { try mark() }", i),
			fmt.Sprintf("let h%d = KeyOf(a%d)", i, i),
			fmt.Sprintf("func f%d() int { %s }", i, fmt.Sprintf(keyedForms[r.IntN(len(keyedForms))], i, r.IntN(n))))
	}
	r.Shuffle(len(decls), func(i, j int) { decls[i], decls[j] = decls[j], decls[i] })
	src := strings.Join(decls, "\n") + "\n"
	return decided(strings.ReplaceAll(src, "KT", "Key[func() fails Bang]"))
}

// TestClassGMessage: the class G S108 names the value use, or the literal
// that carries it, and the row to write with the parameters elided (round
// 16 m2).
func TestClassGMessage(t *testing.T) {
	for _, tc := range []struct{ src, at, want string }{
		// Decided sources (#196): mark and keep are ordinary functions, and
		// keep is generic, since there is no any type; each line moves
		// up by the package clause.
		{
			src: "type Bang {}\nfunc mark() fails Bang { return Bang }\nfunc keep[T](f T) {}\n" +
				"func walk(n int) { try mark(); keep(func() { try walk(n) }) }\n",
			at:   "func() { try walk",
			want: "walk is used as a value through the function literal at 4:%d inside its own recursive group; write its error row: func walk(…) fails …",
		},
		{
			src:  "type Bang {}\nfunc mark() fails Bang { return Bang }\nfunc walk() { try mark(); let g = walk; _ = g }\n",
			at:   "walk; _",
			want: "walk is used as a value at 3:%d inside its own recursive group; write its error row: func walk() fails …",
		},
		// run checks its block (its row R is open): a block typing never
		// checks, as one an arity error refuses, carries no class G use
		// (round 8 review M2).
		{
			src: "type Cap { M() }\nfunc run[R](n int, body func() needs R) needs R { body() }\n" +
				"func walk(n int) { Cap.M(); run(n) { walk(n) } }\n",
			at:   "{ walk(n) }",
			want: "walk is used as a value through the trailing block at 3:%d inside its own recursive group; write its capability row: func walk(…) needs …",
		},
	} {
		line := tc.src[strings.LastIndex(tc.src[:strings.Index(tc.src, tc.at)], "\n")+1:]
		want := fmt.Sprintf(tc.want, strings.Index(line, tc.at)+1)
		_, diags := CheckSource(tc.src)
		found := false
		for _, d := range diags {
			if d.Message == want {
				found = true
			}
		}
		if !found {
			t.Errorf("want %q, got %v", want, diags)
		}
	}
}

// TestKeyedLiterals: generated sources whose only references are literal
// keys are accepted, in every declaration order, and the walk resolves
// every key as typing does (round 16 M1).
func TestKeyedLiterals(t *testing.T) {
	for seed := range uint64(200) {
		src := keyedLiterals(seed)
		if _, diags := CheckSource(src); len(diags) > 0 {
			t.Fatalf("seed %d: want acceptance, got %v\n%s", seed, diags, src)
		}
		if p := RefWalkProblems(src); len(p) > 0 {
			t.Fatalf("seed %d: reference walk: %v\n%s", seed, p, src)
		}
		if p := OrderProblems(src, 3); len(p) > 0 {
			t.Fatalf("seed %d: declaration order: %s", seed, p[0])
		}
	}
}
