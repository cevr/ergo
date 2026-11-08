package lower

import (
	"fmt"
	"strings"
	"testing"
)

// TestReviewRegressions: the stage 2 round 1 review's probes, kept as
// behaviors at the lowering boundary.
//
//   - B1: a *T dot call whose argument writes statements changes the
//     receiver, not a copy (p02).
//   - M1: a function value called from a pure function runs a with block
//     in its body: the environment it receives is never nil (p18).
//   - M2: a place is fixed before the value: a pointer and a slice reassigned
//     by the value do not move the write (p03).
//   - m1: an error type's text comes from its fields: a 196m String function
//     on a field's type does not change it, and each kind of field has its
//     text (p10), a generic error type's too when no field has a type
//     parameter in its type.
//   - m7: an entry that provides two capabilities is evaluated once: the
//     two keys hold one value.
func TestReviewRegressions(t *testing.T) {
	cases := []behavior{{
		name: "ptrspill",
		src: `type Counter { n int }

func Add(c *Counter, k int) {
	c.n = c.n + k
}

func three() int {
	return 3
}

func main() needs Console {
	let mut c = Counter{n: 0}
	c.Add(three())
	let mut cs = []Counter{Counter{n: 1}}
	cs[0].Add(three())
	if c.n == 3 && cs[0].n == 4 {
		Console.Println("kept")
	} else {
		Console.Println("lost")
	}
}
`,
		stdout: "kept\n",
	}, {
		name: "nilenv",
		src: `type Store { Get() int }

type Mem { k int }

func Get(m Mem) int {
	return m.k
}

let W = context {
	Store: Mem{k: 4},
}

func run(f func() int) int {
	return f()
}

func main() needs Console {
	let v = run(func() int {
		return with W {
			Store.Get()
		}
	})
	if v == 4 {
		Console.Println("ok")
	}
}
`,
		stdout: "ok\n",
	}, {
		name: "placeorder",
		src: `type Box { v int }

func main() needs Console {
	let mut a = Box{v: 0}
	let mut b = Box{v: 0}
	let mut p = &a
	let f = func() int {
		p = &b
		return 1
	}
	p.v = f()
	if a.v == 1 && b.v == 0 {
		Console.Println("pointer: target first")
	}
	let mut xs = []int{0}
	let ys = []int{0}
	let g = func() int {
		xs = ys
		return 2
	}
	xs[0] = g()
	if ys[0] == 0 {
		Console.Println("slice: target first")
	}
}
`,
		stdout: "pointer: target first\nslice: target first\n",
	}, {
		name: "errortext",
		src: `type Inner { code int }

func String(i Inner) string {
	return "masked"
}

type Kind = Red | Blue

type Red { n int }

type Blue { m int }

type Bad {
	at Inner,
	ok bool,
	ratio float32,
	wait duration,
	tags []string,
	kind Kind,
	small int8,
	big uint64,
}

func fail() fails Bad {
	return Bad{at: Inner{code: 7}, ok: true, ratio: 0.1, wait: 1500ms, tags: []string{"a", "b"}, kind: Red{n: 1}, small: -3, big: 18446744073709551615}
}

func main() fails Bad needs Console {
	Console.Println("start")
	try fail()
}
`,
		stdout: "start\n",
		exit:   1,
		stderr: []string{"error: Bad{at:{code:7} ok:true ratio:0.1 wait:1.5s tags:[a b] kind:{n:1} small:-3 big:18446744073709551615}\n"},
	}, {
		name: "genericerror",
		src: `type Fault[T] { code int }

func fail() fails Fault[int] {
	return Fault[int]{code: 2}
}

func main() fails Fault[int] needs Console {
	Console.Println("start")
	try fail()
}
`,
		stdout: "start\n",
		exit:   1,
		stderr: []string{"error: Fault{code:2}\n"},
	}, {
		// Stage 2 r2 B1: a dereferenced receiver is the pointee, with an
		// effectful argument (q04) and a constant one (q10), and a field of a
		// dereferenced pointer.
		name: "derefreceiver",
		src: `type Counter { n int }

type Pair { c Counter }

func Add(c *Counter, k int) { c.n = c.n + k }

func three() int { return 3 }

func main() needs Console {
	let mut c = Counter{n: 0}
	let p = &c
	(*p).Add(three())
	(*p).Add(3)
	let mut pair = Pair{c: Counter{n: 1}}
	let q = &pair
	(*q).c.Add(three())
	q.c.Add(1)
	if c.n == 6 && pair.c.n == 5 {
		Console.Println("kept")
	} else {
		Console.Println("lost")
	}
}
`,
		stdout: "kept\n",
	}, {
		// Stage 2 r2 M1: generated text functions take every local from a
		// namer: a variant named v (q01), a type parameter named v or e, and
		// a record named s, whose text function is texts (q09).
		name: "textnames",
		src: `type v { code int }

type Other { n int }

type U = v | Other

type s { code int }

type Bad { at U, list []s }

type Gen[v] { code int }

type Gen2[e] { code int }

func fail(k int) fails Bad | Gen[int] | Gen2[int] {
	if k == 0 {
		return Bad{at: v{code: 7}, list: []s{s{code: 8}}}
	}
	if k == 1 {
		return Gen[int]{code: 9}
	}
	return Gen2[int]{code: 10}
}

func main() fails Bad | Gen[int] | Gen2[int] needs Console {
	Console.Println("start")
	try fail(K)
}
`,
		stdout: "start\n",
		exit:   1,
	}, {
		name: "sharedonce",
		// A named context is closed, so its provider cannot print; the provider
		// holds a slice instead, and Store's write is visible through Reader
		// only when both keys hold one evaluated value.
		src: `type Store { Bump() }

type Reader { Read() int }

type Both { s []int }

func Bump(b Both) {
	let mut s = b.s
	s[0] = s[0] + 1
}

func Read(b Both) int { return b.s[0] }

let Shared = context {
	Store, Reader: Both{s: []int{0}},
}

func main() needs Console {
	with Shared {
		Store.Bump()
		if Reader.Read() == 1 {
			Console.Println("one value")
		} else {
			Console.Println("two values")
		}
	}
}
`,
		stdout: "one value\n",
	}}
	// textnames fails from main with each of its three error types.
	var expanded []behavior
	for _, c := range cases {
		if c.name != "textnames" {
			expanded = append(expanded, c)
			continue
		}
		for k, text := range []string{"Bad{at:{code:7} list:[{code:8}]}", "Gen{code:9}", "Gen2{code:10}"} {
			v := c
			v.name = fmt.Sprintf("textnames%d", k)
			v.src = strings.Replace(c.src, "fail(K)", fmt.Sprintf("fail(%d)", k), 1)
			v.stderr = []string{"error: " + text + "\n"}
			expanded = append(expanded, v)
		}
	}
	runBehaviors(t, expanded)
}

// TestRefusalsBeforeGo: forms Go would reject or misread are refused at a
// located construct, never passed to Go.
//
//   - M4: an extern intrinsic is bound to the prelude declaration it
//     implements; a user extern with the same target, whatever its
//     signature, is refused at its declaration (p06, p11); since check-v1
//     stage E4 (C26) the checker refuses it, S108, before lowering.
//   - M5: float patterns and repeated value, type or catch arms are the
//     checker's S108, so Go never sees a duplicate case (p19, p20).
//   - m1: an error type with a field whose type is a type parameter is
//     refused at the type.
//
// Each case states its exact code, message and position: S108 is a checker
// diagnostic, and S109 a lowering refusal (the code sketchfront gives every
// Refusal), with no checker diagnostic.
func TestRefusalsBeforeGo(t *testing.T) {
	cases := []struct {
		name, src    string
		code         string
		line, column int
		message      string
		alsoLines    []int // further checker diagnostics with this code, column and message
	}{{
		name: "extern-arity",
		src: `extern go ergo.local/prototype/runtime/rt.Sleep func nap(d duration)

func main() needs Console {
	nap(1ms)
	Console.Println("ok")
}
`,
		code: "S108", line: 1, column: 1, message: "only std binds the Ergo runtime; its functions take hidden arguments that std's bindings supply (here ergo.local/prototype/runtime/rt.Sleep)",
	}, {
		name: "extern-swapped",
		src: `extern go ergo.local/prototype/runtime/rt.Sleep func nap(d duration, s LiveScheduler)

func main() needs Console | Ambient {
	let s = NewLiveScheduler()
	nap(1ms, s)
	Console.Println("ok")
}
`,
		code: "S108", line: 1, column: 1, message: "only std binds the Ergo runtime; its functions take hidden arguments that std's bindings supply (here ergo.local/prototype/runtime/rt.Sleep)",
	}, {
		name: "extern-same-signature",
		src: `extern go ergo.local/prototype/runtime/rt.Sleep func nap(s LiveScheduler, d duration)

func main() needs Console | Ambient {
	let s = NewLiveScheduler()
	nap(s, 1ms)
	Console.Println("ok")
}
`,
		code: "S108", line: 1, column: 1, message: "only std binds the Ergo runtime; its functions take hidden arguments that std's bindings supply (here ergo.local/prototype/runtime/rt.Sleep)",
	}, {
		name: "float-cases",
		src: `func kind(x float32) string {
	match x {
	case 0.1:
		return "a"
	case 0.1000000001:
		return "b"
	case _:
		return "c"
	}
}

func main() needs Console {
	Console.Println(kind(0.1))
}
`,
		code: "S108", line: 3, column: 7, message: "a float pattern compares for equality, a trap for floats and durations; test with if (rows.md, Totality 1)", alsoLines: []int{5},
	}, {
		name: "int-cases",
		src: `func kind(x int) string {
	match x {
	case 1, 2:
		return "a"
	case 3, 2:
		return "b"
	case _:
		return "c"
	}
}

func main() needs Console {
	Console.Println(kind(1))
}
`,
		code: "S108", line: 5, column: 10, message: "this case is already covered by the earlier case `2` (unreachable)",
	}, {
		name: "catch-arms",
		src: `type A { n int }

type B { n int }

func f() int fails A | B {
	return 1
}

func main() needs Console {
	let v = f() catch {
	case A:
		1
	case B:
		2
	case A:
		3
	}
	if v == 1 {
		Console.Println("one")
	}
}
`,
		code: "S108", line: 15, column: 7, message: "A is caught twice (rows.md, Catch 2)",
	}, {
		name: "type-cases",
		src: `type A { n int }

type B { n int }

type U = A | B

func kind(u U) string {
	match u {
	case A:
		return "a"
	case B:
		return "b"
	case A:
		return "c"
	}
}

func main() needs Console {
	Console.Println(kind(A{n: 1}))
}
`,
		code: "S108", line: 13, column: 7, message: "this case is already covered by the earlier case `A` (unreachable)",
	}, {
		// A generic error type's text lowers per instance since lower-v1
		// stage 4 (TestGenericErrorText); a Map field still has no text.
		name: "map-error-field",
		src: `type Fault { F Map[string, int] }

func fail() fails Fault {
	return Fault{F: Map[string, int]{}}
}

func main() fails Fault needs Console {
	try fail()
}
`,
		code: "S109", line: 1, column: 6, message: "the text of an error type with a field of type Map[string, int] waits for derived text forms (#49)",
	}}
	// stage 2 round 3 m1 (r04) was a refusal of an error type whose field
	// Error_ met its renamed field Error; since lower-v1 stage 4 (S4d) the
	// later field takes Error__ (TestErrorFieldNamesStayDistinct).
	for _, c := range cases {
		o := lowerSource(c.name, c.src, true)
		if o.panicked != nil || o.err != nil {
			t.Errorf("%s: panic %v, error %v", c.name, o.panicked, o.err)
			continue
		}
		if c.code == "S109" {
			if len(o.diags) != 0 || len(o.refused) != 1 || o.refused[0].Line != c.line || o.refused[0].Column != c.column || o.refused[0].Message != c.message {
				t.Errorf("%s: diagnostics %v, refusals %+v; want no diagnostic and one refusal at %d:%d: %s", c.name, o.diags, o.refused, c.line, c.column, c.message)
			}
			continue
		}
		lines := append([]int{c.line}, c.alsoLines...)
		if len(o.diags) != len(lines) || len(o.refused) != 0 {
			t.Errorf("%s: diagnostics %v, refusals %+v; want %s at lines %v, column %d: %s", c.name, o.diags, o.refused, c.code, lines, c.column, c.message)
			continue
		}
		for i, line := range lines {
			d := o.diags[i]
			if d.Code != c.code || d.Line != line || d.Column != c.column || d.Message != c.message {
				t.Errorf("%s: diagnostic %v; want %s at %d:%d: %s", c.name, d, c.code, line, c.column, c.message)
			}
		}
	}
}
