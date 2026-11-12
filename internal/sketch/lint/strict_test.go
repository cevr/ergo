package lint_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// A name of the banned-words generator and whether it holds the word
// shape as a word of its own.
var nameParts = []struct {
	name   string
	banned bool
}{
	{"packetShape", true},
	{"ShapeOf", true},
	{"SHAPEList", true},
	{"edge_shape", true},
	{"reshape", false}, // one word, not shape
	{"shapes", false},  // another word
	{"outline", false},
	{"shapeless", false},
}

// TestBannedWordsAgreesWithReference declares functions and locals named
// from parts with and without the banned word shape (as a camel-case,
// upper-case or underscored word, and inside longer words) and checks that
// banned-words fires on exactly the declarations whose name holds it as a
// word, and never on a use or a string.
func TestBannedWordsAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xba))
		p := newPlanted()
		var calls []string
		for i := 0; i < 1+r.IntN(5); i++ {
			fn := nameParts[r.IntN(len(nameParts))]
			local := nameParts[r.IntN(len(nameParts))]
			name := fmt.Sprintf("%s%d", fn.name, i)
			p.write(fmt.Sprintf("\nfunc %s() string {", name), fn.banned, 1)
			p.write(fmt.Sprintf("\tlet %s = \"shape\"", local.name), local.banned, 0)
			p.write("\t"+local.name+"\n}", false, 0)
			calls = append(calls, name+"()")
		}
		p.write("\nfunc main() needs Console {", false, 0)
		for _, c := range calls {
			p.write("\tConsole.Println("+c+")", false, 0)
		}
		p.write("}", false, 0)
		p.check(t, "banned-words", "TestBannedWordsAgreesWithReference", seed)
	}
}

// A let of the known-value-widening generator, as the body of a function
// of n: its text and whether the rule fires on its first line.
var widenings = []struct {
	fires bool
	text  string
}{
	{true, "let f Figure = Circle{R: n}\n\tarea(f)"},
	{false, "let f = Circle{R: n}\n\tarea(f)"},
	{false, "let f Circle = Circle{R: n}\n\tarea(f)"},
	// a let mut keeps the union for a later variant
	{false, "let mut f Figure = Circle{R: n}\n\tif n > 1 {\n\t\tf = Square{S: n}\n\t}\n\tarea(f)"},
	// the value's variant is not known
	{false, "let f Figure = pick(n)\n\tarea(f)"},
}

const figurePrelude = `type Circle { R int }

type Square { S int }

type Figure = Circle | Square

func area(f Figure) int {
	match f {
	case Circle as c:
		c.R
	case Square as s:
		s.S
	}
}

func pick(n int) Figure {
	if n > 0 {
		return Circle{R: n}
	}
	Square{S: n}
}
`

// TestKnownValueWideningAgreesWithReference plants lets that write a
// union over a known variant, and controls: no written type, the variant's
// type, a let mut, and a value of unknown variant.
func TestKnownValueWideningAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(30) {
		r := rand.New(rand.NewPCG(seed, 0x3d))
		p := newPlanted()
		p.write(figurePrelude, false, 0)
		for i := 0; i < 1+r.IntN(5); i++ {
			w := widenings[r.IntN(len(widenings))]
			p.write(fmt.Sprintf("\nfunc w%d(n int) int {", i), false, 0)
			p.write("\t"+w.text, w.fires, 0)
			p.write("}", false, 0)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "known-value-widening", "TestKnownValueWideningAgreesWithReference", seed)
	}
}
