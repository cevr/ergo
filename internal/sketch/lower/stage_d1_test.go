package lower

import (
	"strings"
	"testing"
)

// The untyped `let x` that stage C deferred to D-D1 (TestCheckCorpusBuilds'
// "a let with neither a type nor a value" refusal) is now the checker's
// S108, at package level and in a body, so lowering never meets it.
func TestUntypedLetIsRefusedBeforeLowering(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"package", "let pattern\n"},
		{"local", "func f() {\n\tlet x\n\t_ = x\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := lowerSource("r", tc.src, false)
			if len(o.diags) != 1 || o.diags[0].Code != "S108" || !strings.Contains(o.diags[0].Message, "needs a type or a value") {
				t.Fatalf("want one S108 naming the missing type or value; got %v", o.diags)
			}
			if len(o.refused) != 0 || o.panicked != nil {
				t.Fatalf("a refused program is not lowered; got refusals %v, panic %v", o.refused, o.panicked)
			}
		})
	}
}

// Stage D1 (check-v1): `let` and `let mut` (D-D1, Q2, Q3), no shadowing
// (D-D2) and prelude names (D-D3). The refusals are the checker's
// (internal/sketch/check stage_d1_test.go); every form the stage admits
// runs here through the public CLI.

// TestErgoRunLetMut: a `let mut` local is reassigned with `=` and every
// `op=`, through fields (nested too) and slice elements, from a function
// literal that captures it, and in a `for` condition loop; a `let x T`
// starts at its zero value; a record copy is a value (the original keeps
// its field); a write through a pointer needs no let mut, and a borrow,
// `&r` or the implicit one of `r.bump()`, reaches the let mut local.
// Map entries are D2's (the Map rule).
func TestErgoRunLetMut(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Inner { N int }
type Outer { In Inner, Tag string }

func report(name string, ok bool) needs Console {
	if ok {
		Console.Println(name)
	} else {
		Console.Println("FAIL " + name)
	}
}

func ops() int {
	let mut n = 6
	n = n + 1
	n += 3
	n -= 2
	n *= 4
	n /= 2
	n %= 7
	n <<= 3
	n >>= 1
	n |= 1
	n &= 13
	n ^= 2
	n &^= 8
	n
}

func fields() bool {
	let o = Outer{In: Inner{N: 1}, Tag: "a"}
	let mut c = o
	c.In.N = 5
	c.Tag = "b"
	c.In.N += 1
	c.In.N == 6 && c.Tag == "b" && o.In.N == 1 && o.Tag == "a"
}

func elements(xs []int) bool {
	let mut ys = xs
	ys[0] = 9
	ys[1] += 1
	ys[0] == 9 && ys[1] == 3
}

func captured() int {
	let mut total = 0
	let add = func(k int) { total += k }
	add(2)
	add(5)
	total
}

func loop() int {
	let mut i = 0
	let mut sum = 0
	for i < 5 {
		sum += i
		i += 1
	}
	sum
}

func zero() bool {
	let mut z int
	let empty string
	let ok = z == 0 && empty == ""
	z = 3
	ok && z == 3
}

func bump(r *Inner) { r.N = r.N + 1 }

func set(p *Inner) { p.N = 7 }

func pointers() bool {
	let mut r = Inner{N: 1}
	r.bump()
	let was = r.N
	let p = &r
	set(p)
	p.bump()
	was == 2 && r.N == 8
}

func helper() int { 1 }

func siblings(flag bool) int {
	let helper = 2
	if flag {
		let t = 10
		t + helper
	} else {
		let t = 20
		t
	}
}

func main() needs Console {
	report("ops", ops() == 3)
	report("fields", fields())
	report("elements", elements([]int{1, 2}))
	report("captured", captured() == 7)
	report("loop", loop() == 10)
	report("zero", zero())
	report("pointers", pointers())
	report("siblings", siblings(true) == 12 && siblings(false) == 20 && helper() == 1)
}
`, "ops\nfields\nelements\ncaptured\nloop\nzero\npointers\nsiblings\n")
}

// TestErgoRunImplicitBorrowSpellings: the implicit borrow of a called
// selector reaches the let mut local in every spelling, parenthesised once
// or twice, from a callback the callee runs (the run control of the check
// test TestImplicitBorrowSpellingsInSkippedFrames; stage D1 round 2 review
// M2).
func TestErgoRunImplicitBorrowSpellings(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type R { N int }

func Bump(r *R) { r.N = r.N + 1 }

func each(cb func()) { cb() }

func main() needs Console {
	let mut r = R{N: 0}
	each(func() { r.Bump() })
	each(func() { (r.Bump)() })
	each(func() { ((r.Bump))() })
	if r.N == 3 { Console.Println("three borrows") }
}
`, "three borrows\n")
}
