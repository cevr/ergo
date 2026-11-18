package check_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestNormalFormShapeIsRead is the property of the shape rule (types.md,
// Maps 5; CONSTRAINTS C5b): over seeded providers whose Equal is
// e(a) == e(b) and whose Hash is hash(h(k)) for expressions e and h drawn
// from a small set, the checker derives the normal form, and Map[int,
// int, P] is keyTable, exactly when e and h are the same expression and
// it reads the key alone (not the provider's field); under the reference
// lowering (mapLowering=table) the Map is hashTable either way. The
// reference model is the drawn pair itself. A failing seed prints the
// Ergo source.
//
// Mutants only it kills (testdata/mutants.json): c5b-shape-hash-unread,
// c5b-shape-reference-ignored.
func TestNormalFormShapeIsRead(t *testing.T) {
	exprs := []struct {
		text string
		pure bool // reads the key alone
	}{
		{"x % 7", true},
		{"x / 3", true},
		{"x", true},
		{"x * 2 + 1", true},
		{"x % p.m", false},
	}
	reach := map[bool]int{}
	for seed := uint64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewPCG(seed, 5))
		e := exprs[r.IntN(len(exprs))]
		h := e
		if r.IntN(2) == 0 {
			h = exprs[r.IntN(len(exprs))]
		}
		at := func(text, x string) string { return "(" + strings.ReplaceAll(text, "x", x) + ")" }
		src := fmt.Sprintf(`type Mod {
	m int,
} implements Hashing[int]

func Hash(p Mod, k int) uint64 { hash%s }

func Equal(p Mod, a int, b int) bool { %s == %s }

func size(m Map[int, int, Mod]) int { len(m) }
`, at(h.text, "k"), at(e.text, "a"), at(e.text, "b"))
		want := e == h && e.pure
		reach[want]++
		for _, lowering := range []string{"", "table"} {
			restore := check.SetMapLowering(lowering)
			f, err := syntax.Parse(src)
			if err != nil {
				restore()
				t.Fatal(err)
			}
			pkg, ds := check.Check("main", []*syntax.File{f}, nil)
			restore()
			if len(ds) > 0 {
				t.Fatalf("seed %d: %v\n%s", seed, ds, src)
			}
			derived, table := false, ""
			for _, fn := range pkg.Funcs {
				derived = derived || fn.Name == "NormalKey"
				if fn.Name == "size" {
					table = fn.Sig.Params[0].Type.(*check.Named).Obj.Name
				}
			}
			wantTable := "hashTable"
			if want && lowering == "" {
				wantTable = "keyTable"
			}
			if derived != want || table != wantTable {
				t.Fatalf("seed %d, lowering %q: derived %v, Map is %s; want derived %v, %s\n%s", seed, lowering, derived, table, want, wantTable, src)
			}
		}
	}
	if reach[true] == 0 || reach[false] == 0 {
		t.Fatalf("the seeds reach shaped %d and unshaped %d providers; want both", reach[true], reach[false])
	}
}

// TestProviderMapRefusals holds the diagnostic texts of the readers of a
// Map with a provider that refuse it (types.md, Maps 3, 6 and 7): its
// codec, == over values that are not comparable, and a normal-form Map
// passed where a Map over a provider type parameter is expected.
//
// Mutants only it kills (testdata/mutants.json): c5b-codec-not-refused,
// c5b-map-equal-incomparable, c5b-generic-normal-form-unnamed.
func TestProviderMapRefusals(t *testing.T) {
	const provider = `import strings

type Fold {} implements Hashing[string]

func Hash(_ Fold, k string) uint64 { hash(strings.ToLower(k)) }

func Equal(_ Fold, a string, b string) bool { strings.ToLower(a) == strings.ToLower(b) }

`
	for _, row := range []struct{ src, text string }{
		{`func put(m Map[string, int, Fold], w Writer) { encode(m, w) }`,
			"Map[string, int, Fold] does not satisfy wire: it is a Map with a provider, which has no wire form yet"},
		{`func same(a Map[string, []int, Fold], b Map[string, []int, Fold]) bool { a == b }`,
			"two Maps with a provider are == when they hold the same keys, each with an == value; []int values are not comparable (types.md, Maps 3)"},
		{`func size[H Hashing[string]](m Map[string, int, H]) int { len(m) }

func main() { let n = size(Map[string, int, Fold]{}); n }`,
			"whose provider Fold has a normal form, so it is not yet a value of Map[string, int, H] over a type parameter; write the function for Fold (types.md, Maps 6)"},
	} {
		f, err := syntax.Parse(provider + row.src + "\n")
		if err != nil {
			t.Fatal(err)
		}
		_, ds := check.Check("main", []*syntax.File{f}, nil)
		found := false
		for _, d := range ds {
			found = found || strings.Contains(d.Message, row.text)
		}
		if !found {
			t.Errorf("%s\ngives %v; want a diagnostic with %q", row.src, ds, row.text)
		}
	}
}
