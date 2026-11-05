package format

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

// nestedChains are small programs that nest every construct with
// punctuation the tree does not record as a position (commas, `|`, the `]`
// of a slice type, the `[` and `]` of a map type, the `.` of a selector,
// `..`, slice colons, interpolation delimiters) inside chains of selectors,
// calls, indexes and literals.
var nestedChains = []struct{ name, src string }{
	{"selector call index chain", "func f() {\n\tlet x = a.b(c, d)[e].g(h)[i:j].k[l:m:n]\n}\n"},
	{"slice and map types", "let xs [][]Map[string, []int]\n\nlet ys = []int{1, 2}\n"},
	{"record updates", "func f() {\n\tlet w = W{..g(a).b[c], X: V[int]{..w.y, Z: 1}.z, Y: 2}\n}\n"},
	{"nested literals", "let m = Map[string, []T]{\"a\": {{X: 1, Y: []int{2, 3}}, {X: 4}}, \"b\": nil}\n"},
	{"func types and results", "func f[E](a, b int, g func(x, y int) (int, string) fails E | go.Error needs A | B) int fails E needs C | D {\n\treturn g(a, b)\n}\n"},
	{"union and error rows", "type U[C, T] = A | b.B | C[T, U]\n\nfunc f[A]() fails A | b.B { return nil }\n"},
	{"type params and instantiation", "type Pair[K comparable, V] {\n\tk, l K,\n\tv    V,\n}\n\nlet p = Pair[string, int]{k: \"a\", v: 1}\n"},
	{"catch and match tuples", "func f() {\n\tlet r = try g(a, b) catch {\n\tcase A as e, b.B:\n\t\tlog(e)\n\t}\n\tmatch (r, s) {\n\tcase (A, _), (B{X: x} as b, C):\n\tcase _:\n\t}\n}\n"},
	{"statements", "func f() {\n\tfor i in 0..n {\n\t\tif g() {\n\t\t\tcontinue\n\t\t} else if b {\n\t\t\tbreak\n\t\t}\n\t}\n\tfor k, v in m {\n\t\tx += v\n\t}\n\tlet mut (q, r) = (1, \"\\(k) of \\(f(\"n\"))\")\n\tassert q == 1\n}\n"},
	{"context and machine", "let W = context provides A | B needs C {\n\tC, D:        X{},\n\treplace e.E: Y{a, b},\n\tShared,\n}\n\ntype M machine[S, E] {\n\tinitial: Idle{},\n\tstep:    f,\n}\n"},
	{"imports and declarations", "import a\nimport b c\nimport go net/url\n\nextern go example.com/x.F func F(a int64) (int64, bool)\n\ntest T(x int64) {\n\tassert x == x\n}\n\ntype Codec {\n\tEncode(v int64) []byte,\n\n\tlaw RoundTrip(v int64) { Encode(v) == Encode(v) },\n}\n"},
}

// TestCommentsAtEveryBoundary inserts each comment kind before every token
// of the nested chains, the grammar fixtures and the spec fences, and of
// every seventh token of the corpus, and checks each admitted variant: the
// output parses, keeps the tree and the place of every comment, and
// formats to itself (spec risk 4, review T1).
func TestCommentsAtEveryBoundary(t *testing.T) {
	type input struct {
		name, src string
		stride    int
	}
	var inputs []input
	for _, c := range nestedChains {
		if _, err := syntax.Parse(c.src); err != nil {
			t.Fatalf("%s does not parse: %v", c.name, err)
		}
		inputs = append(inputs, input{c.name, c.src, 1})
	}
	for _, g := range sketchtest.GrammarFixtures {
		inputs = append(inputs, input{g.Name, g.Src, 1})
	}
	fences, err := sketchtest.SpecFences()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fences {
		inputs = append(inputs, input{f.Name, f.Src, 1})
	}
	v0, _, err := sketchtest.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	stride := 7
	if testing.Short() {
		stride = 31
	}
	for _, c := range v0 {
		inputs = append(inputs, input{c.Name, c.Src, stride})
	}
	checked, failed := 0, 0
	for _, in := range inputs {
		if _, err := syntax.Parse(in.src); err != nil {
			continue
		}
		for i, at := range sketchtest.Boundaries(in.src) {
			if i%in.stride != 0 {
				continue
			}
			for _, kind := range sketchtest.CommentKinds {
				src := in.src[:at] + kind + in.src[at:]
				if _, err := syntax.Parse(src); err != nil {
					continue
				}
				checked++
				out, err := Source(src)
				msg := ""
				switch {
				case err != nil:
					msg = "parsed but did not format: " + err.Error()
				default:
					if msg = contracts(src, out); msg == "" {
						if again, _ := Source(out); again != out {
							msg = "not idempotent"
						}
					}
				}
				if msg != "" {
					failed++
					if failed <= 10 {
						t.Errorf("%s, comment %q before offset %d: %s\n--- input\n%s\n--- output\n%s", in.name, strings.TrimSpace(kind), at, msg, src, out)
					}
				}
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d variants failed", failed, checked)
	}
	t.Logf("%d variants checked", checked)
}
