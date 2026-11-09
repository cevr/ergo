package lower

import (
	"strings"
	"testing"
)

// Stage 4 (S4c): the forms the lowering refused until stage 4.

// genericErrorCases are programs whose main fails with a value of a
// generic error type, or of a type with a field of a generic union: the
// error report names the type and prints each field as the instance's.
var genericErrorCases = []struct {
	name, src, stdout, report string
}{
	{"field-of-T", `type Fault[T] { F T }

func fail(n int) fails Fault[int] {
	return Fault[int]{F: n}
}

func main() fails Fault[int] needs Console {
	Console.Println("before")
	try fail(5)
	Console.Println("never")
}
`, "before\n", "Fault{F:5}"},
	// Two instances of one generic error type: the Error method switches
	// on the instance, so each prints its own field.
	// The reported instance sorts after the caught one.
	{"two-instances", `type Fault[T] { F T, N int }

func quiet() fails Fault[bool] { Fault[bool]{F: true, N: 1} }

func main() fails Fault[string] needs Console {
	quiet() catch {
	case Fault[bool] as f:
		Console.Println("\(f.N)")
	}
	return Fault[string]{F: "s", N: 2}
}
`, "1\n", "Fault{F:s N:2}"},
	// An instance made inside a generic function, at the caller's type
	// argument: the caller's row names it.
	{"made-in-generic", `type Point { X int, Y int }

type Fault[T] { F T }

func wrap[T](x T) fails Fault[T] {
	return Fault[T]{F: x}
}

func main() fails Fault[Point] {
	try wrap(Point{X: 1, Y: 2})
}
`, "", "Fault{F:{X:1 Y:2}}"},
	// A field of a generic union, in a non-generic error type, and a
	// generic error type over a slice of a generic record.
	{"generic-union-field", `type Just[T] { V T }

type Nothing {}

type Maybe[T] = Just[T] | Nothing

type Bad { M Maybe[int], Rest []Maybe[string] }

func main() fails Bad {
	return Bad{M: Just[int]{V: 7}, Rest: []Maybe[string]{Nothing, Just[string]{V: "x"}}}
}
`, "", "Bad{M:{V:7} Rest:[{} {V:x}]}"},
}

// TestGenericErrorText (lower-v1 stage 4, item 5): the text of an error
// type whose field is a type parameter or a generic union is written per
// instance, so a failing main reports the instance's fields and exits 1.
func TestGenericErrorText(t *testing.T) {
	needsBuild(t)
	var progs []*lowered
	for _, c := range genericErrorCases {
		progs = append(progs, mustLower(t, c.name, c.src))
	}
	exes := buildAll(t, progs, true)
	if exes == nil {
		return
	}
	for _, c := range genericErrorCases {
		stdout, stderr, code := runExe(t, exes[c.name])
		if code != 1 || stdout != c.stdout || !strings.Contains(stderr, c.report) {
			t.Errorf("%s: exit %d stdout %q stderr %q; want exit 1, stdout %q and a report holding %q", c.name, code, stdout, stderr, c.stdout, c.report)
		}
	}
}
