package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// C45 (check-v1 E6b): a type defined over a tuple destructures through its
// underlying tuple, as a tuple pattern already matches it (D-B6): a
// several-name let of a value of a defined tuple type binds its elements.
// lower/stage_e6b_test.go runs the same program (TestErgoRunDefinedTuple).
const definedTupleProgram = `type Pair (int, string)

type Triple (int, string, bool)

type Again Pair

type Two[T] (T, T)

type Box[T] { v T }

type Bad { why string }

func pair() Pair {
	Pair((1, "a"))
}

func risky(fail bool) Pair fails Bad {
	if fail {
		return Bad{why: "no"}
	}
	Pair((2, "b"))
}

func swap(p Pair) Pair {
	let n, s = p
	Pair((n + 10, s + "!"))
}

let top, word = pair()

let _, again = Again(Pair((3, "c")))

func main() needs Console {
	let a, b = pair()
	Console.Println("\(a) \(b)")
	let x, _, z = Triple((4, "d", true))
	Console.Println("\(x) \(z)")
	let _, m = Again(swap(pair()))
	Console.Println(m)
	let l, r = Two[string](("l", "r"))
	Console.Println(l + r)
	let i, _ = Box[Pair]{v: pair()}.v
	Console.Println("\(i)")
	let c, d = risky(true) catch { case Bad: Pair((0, "caught")) }
	Console.Println("\(c) \(d)")
	let e, f = risky(false) catch { case Bad: Pair((0, "caught")) }
	Console.Println("\(e) \(f)")
	match Triple((5, "e", false)) {
	case (n, s, true):
		Console.Println("\(n) \(s) yes")
	case (n, s, false):
		Console.Println("\(n) \(s) no")
	}
	Console.Println("\(top) \(word) \(again)")
}
`

func TestDefinedTupleLet(t *testing.T) {
	if _, diags := check.CheckSource(definedTupleProgram); len(diags) > 0 {
		t.Fatalf("the defined tuple program: %s", describe(diags))
	}
	// The count of names still has to match the underlying tuple's.
	for _, c := range []struct{ body, want string }{
		{"let a, b, c = pair()", "3 names for 2 values"},
		{"let a, b = Triple((1, \"x\", true))", "2 names for 3 values"},
		{"let a, b = Box[Pair]{v: pair()}", "2 names for 1 values"},
		{"let a, b = 1", "2 names for 1 values"},
	} {
		src := "type Pair (int, string)\ntype Triple (int, string, bool)\ntype Box[T] { v T }\nfunc pair() Pair { Pair((1, \"a\")) }\nfunc f() {\n\t" + c.body + "\n}\n"
		_, diags := check.CheckSource(src)
		if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Message != c.want {
			t.Errorf("%s: want S108 %q, got %s", c.body, c.want, describe(diags))
		}
	}
}
