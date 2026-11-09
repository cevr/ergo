package lower

import (
	"testing"
)

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
