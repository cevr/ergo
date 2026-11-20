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
// Maps 6; CONSTRAINTS C5b, C5d): over seeded providers whose Equal is
// e(a) == e(b) and whose Hash is hash(h(k)) for expressions e and h drawn
// from a small set, the checker derives NormalForm true, and NormalKey
// e(k), exactly when e and h are the same expression, it reads the key
// alone (not the provider's field) and its type is the key's; else
// NormalForm false and NormalKey k. Under the reference lowering
// (mapLowering=table) NormalForm is false either way, and Map[int, int,
// P] is hashTable always. The reference model is the drawn pair itself.
// A failing seed prints the Ergo source.
//
// Mutants only it kills (testdata/mutants.json): c5b-shape-hash-unread,
// c5b-shape-reference-ignored, c5d-shape-other-type.
func TestNormalFormShapeIsRead(t *testing.T) {
	exprs := []struct {
		text string
		pure bool // reads the key alone, and is of the key's type
	}{
		{"x % 7", true},
		{"x / 3", true},
		{"x", true},
		{"x * 2 + 1", true},
		{"x % p.m", false},
		{"x > 3", false},
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
			form, key, table := "", "", ""
			for _, fn := range pkg.Funcs {
				switch {
				case fn.Name == "NormalForm" && fn.Decl.Derived != nil:
					form = bodyText(src, fn.Decl)
				case fn.Name == "NormalKey" && fn.Decl.Derived != nil:
					key = bodyText(src, fn.Decl)
				case fn.Name == "size":
					table = fn.Sig.Params[0].Type.(*check.Named).Obj.Name
				}
			}
			wantForm, wantKey := "false", "k"
			if want && lowering == "" {
				wantForm, wantKey = "true", strings.ReplaceAll(e.text, "x", "k")
			}
			if form != wantForm || key != wantKey || table != "hashTable" {
				t.Fatalf("seed %d, lowering %q: NormalForm %q, NormalKey %q, Map is %s; want %q, %q, hashTable\n%s", seed, lowering, form, key, table, wantForm, wantKey, src)
			}
		}
	}
	if reach[true] == 0 || reach[false] == 0 {
		t.Fatalf("the seeds reach shaped %d and unshaped %d providers; want both", reach[true], reach[false])
	}
}

// TestProviderMapRefusals holds the diagnostic texts of the readers of a
// Map with a provider that refuse it (types.md, Maps 3, 6 and 7): its
// codec and == over values that are not comparable.
//
// Mutants only it kills (testdata/mutants.json): c5b-codec-not-refused,
// c5b-map-equal-incomparable.
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

// bodyText is the one expression of the derived function d's body, as the
// source src writes it: a name, or the expression cloned from src.
func bodyText(src string, d *syntax.FuncDecl) string {
	x := d.Body.List[0].(*syntax.ExprStmt).X
	if id, ok := x.(*syntax.Ident); ok {
		return id.Name
	}
	return syntax.WhereText(src, x)
}
