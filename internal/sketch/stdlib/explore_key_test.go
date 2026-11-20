package stdlib_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestKeyEqualsEquality is the seeded property of std explore's state key
// (MODELCHECK E1, risk 5: a key that merges two states a step can tell
// apart is a false "proved"). Each seed generates wire types (records,
// unions with and without fields, slices, Maps, ints, bools, strings and
// floats, nested up to three deep), pairs of values of them where the
// second is a near miss of the first (one part changed, often to an equal
// value; a text and the same text with a trailing zero byte; 0 and -0; a
// Map written with its keys in another order), and an Ergo test per pair
// that holds explore.KeyOf's equality to the reference's: equal exactly
// when the values are equal with each float compared by its bits. It also
// holds Ergo's == to the reference's == (a float by ==, so 0 == -0). The
// tests run at check time, in process. A failing seed prints the Ergo
// source and the replay command.
//
// Mutants it kills that KeyIsEquality (one fixed type) does not:
// mc-map-order (internal/sketch/eval codecwalk.go writes a Map's keys in
// Go's iteration order, not sorted), mc-key-variant-untagged (the key
// takes in a union variant's fields and not its Tag), and mc-hash-8-bits
// (a test of 1,024 distinct ints asks for 1,024 keys).
func TestKeyEqualsEquality(t *testing.T) {
	seeds, first := 30, uint64(1)
	if s := os.Getenv("ERGO_KEY_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds, first = 1, v
	}
	reach := map[string]int{}
	for seed := first; seed < first+uint64(seeds); seed++ {
		g := &keyGen{r: rand.New(rand.NewPCG(seed, 0x6b6579)), reach: reach}
		src := g.program(12)
		fail := func(format string, args ...any) {
			t.Fatalf("seed %d: %s\nErgo source:\n%s\nreplay: ERGO_KEY_SEED=%d go test -run TestKeyEqualsEquality ./internal/sketch/stdlib", seed, fmt.Sprintf(format, args...), src, seed)
		}
		f, err := syntax.Parse(src)
		if err != nil {
			fail("parse: %v", err)
		}
		pkg, diags := check.Check("main", []*syntax.File{f}, nil)
		if len(diags) > 0 {
			fail("check: %v", diags)
		}
		b := eval.DefaultBudget()
		b.Steps, b.Bytes = 20_000_000, 256<<20
		rep, err := eval.Run(pkg, b)
		if err != nil {
			fail("%v", err)
		}
		if len(rep.Results) != g.pairs {
			fail("%d results for %d pairs", len(rep.Results), g.pairs)
		}
		for _, r := range rep.Results {
			if r.Status != eval.Passed {
				fail("%s: %s (%s)", r.Name, r.Status, r.Reason)
			}
		}
	}
	if seeds > 1 {
		for _, want := range []string{"keys equal", "keys differ", "floats differ only in sign", "text differs only by a zero byte", "map keys reordered", "variant switched", "empty variant"} {
			if reach[want] == 0 {
				t.Errorf("no seed reached %q: %v", want, reach)
			}
		}
	}
}

// kType is a generated type: a basic kind, or a slice, Map, record or
// union of other generated types.
type kType struct {
	kind   string // int, bool, string, float, slice, map, record, union
	elem   *kType
	name   string
	fields []*kType    // a record's
	vars   []*kVariant // a union's
}

type kVariant struct {
	name   string
	fields []*kType
}

// kValue is a generated value of a kType.
type kValue struct {
	t      *kType
	i      int64
	b      bool
	s      string
	f      float64
	elems  []*kValue          // slice, record and variant fields
	keys   []string           // a Map's keys, in the order its literal writes them
	m      map[string]*kValue // a Map's entries
	varIdx int                // a union value's variant
}

type keyGen struct {
	r     *rand.Rand
	decls strings.Builder
	n     int
	pairs int
	reach map[string]int
}

func (g *keyGen) typ(depth int) *kType {
	basic := []string{"int", "bool", "string", "float"}
	if depth >= 3 || g.r.IntN(3) == 0 {
		return &kType{kind: basic[g.r.IntN(len(basic))]}
	}
	switch g.r.IntN(4) {
	case 0:
		return &kType{kind: "slice", elem: g.typ(depth + 1)}
	case 1:
		return &kType{kind: "map", elem: g.typ(depth + 1)}
	case 2:
		g.n++
		t := &kType{kind: "record", name: fmt.Sprintf("R%d", g.n)}
		for range 1 + g.r.IntN(3) {
			t.fields = append(t.fields, g.typ(depth+1))
		}
		fs := make([]string, len(t.fields))
		for i, f := range t.fields {
			fs[i] = fmt.Sprintf("F%d %s", i, f.ergo())
		}
		fmt.Fprintf(&g.decls, "type %s { %s }\n\n", t.name, strings.Join(fs, ", "))
		return t
	}
	g.n++
	id := g.n
	t := &kType{kind: "union", name: fmt.Sprintf("U%d", id)}
	var vs []string
	for k := range 2 + g.r.IntN(2) {
		v := &kVariant{name: fmt.Sprintf("U%dV%d", id, k)}
		for range g.r.IntN(3) {
			v.fields = append(v.fields, g.typ(depth+1))
		}
		t.vars = append(t.vars, v)
		fs := make([]string, len(v.fields))
		for i, f := range v.fields {
			fs[i] = fmt.Sprintf("F%d %s", i, f.ergo())
		}
		vs = append(vs, fmt.Sprintf("%s{%s}", v.name, strings.Join(fs, ", ")))
	}
	fmt.Fprintf(&g.decls, "type %s = %s\n\n", t.name, strings.Join(vs, " | "))
	return t
}

func (t *kType) ergo() string {
	switch t.kind {
	case "float":
		return "float64"
	case "slice":
		return "[]" + t.elem.ergo()
	case "map":
		return "Map[string, " + t.elem.ergo() + "]"
	case "record", "union":
		return t.name
	}
	return t.kind
}

var keyTexts = []string{"", "a", "ab", "b"}
var keyFloats = []float64{0, 1.5, -2.25, 3}

func (g *keyGen) value(t *kType) *kValue {
	v := &kValue{t: t}
	switch t.kind {
	case "int":
		v.i = int64(g.r.IntN(3)) - 1
	case "bool":
		v.b = g.r.IntN(2) == 0
	case "string":
		v.s = keyTexts[g.r.IntN(len(keyTexts))]
	case "float":
		v.f = keyFloats[g.r.IntN(len(keyFloats))]
		if g.r.IntN(4) == 0 {
			v.f = math.Copysign(0, -1)
		}
	case "slice":
		for range g.r.IntN(3) {
			v.elems = append(v.elems, g.value(t.elem))
		}
	case "map":
		v.m = map[string]*kValue{}
		for _, k := range []string{"k", "l", "m"} {
			if g.r.IntN(2) == 0 {
				v.keys = append(v.keys, k)
				v.m[k] = g.value(t.elem)
			}
		}
	case "record":
		for _, f := range t.fields {
			v.elems = append(v.elems, g.value(f))
		}
	case "union":
		v.varIdx = g.r.IntN(len(t.vars))
		for _, f := range t.vars[v.varIdx].fields {
			v.elems = append(v.elems, g.value(f))
		}
	}
	return v
}

// clone is a deep copy of v.
func (v *kValue) clone() *kValue {
	c := *v
	c.elems = nil
	for _, e := range v.elems {
		c.elems = append(c.elems, e.clone())
	}
	c.keys = slices.Clone(v.keys)
	if v.m != nil {
		c.m = map[string]*kValue{}
		for k, e := range v.m {
			c.m[k] = e.clone()
		}
	}
	return &c
}

// nearby changes one part of v (a copy): the part a random walk down the
// tree ends at.
func (g *keyGen) nearby(v *kValue) *kValue {
	c := v.clone()
	g.change(c)
	return c
}

func (g *keyGen) change(v *kValue) {
	// Go down into a part about half the time, when there is one.
	switch v.t.kind {
	case "slice", "record", "union":
		if len(v.elems) > 0 && g.r.IntN(2) == 0 {
			g.change(v.elems[g.r.IntN(len(v.elems))])
			return
		}
	case "map":
		if len(v.keys) > 0 && g.r.IntN(2) == 0 {
			g.change(v.m[v.keys[g.r.IntN(len(v.keys))]])
			return
		}
	}
	switch v.t.kind {
	case "int":
		v.i += int64(g.r.IntN(2))
	case "bool":
		v.b = g.r.IntN(2) == 0
	case "string":
		if g.r.IntN(2) == 0 {
			v.s += "\x00"
			g.reach["text differs only by a zero byte"]++
		}
	case "float":
		switch g.r.IntN(3) {
		case 0:
			v.f = -v.f
			if v.f == 0 {
				g.reach["floats differ only in sign"]++
			}
		case 1:
			v.f = 0
		}
	case "slice":
		if g.r.IntN(2) == 0 {
			v.elems = append(v.elems, g.value(v.t.elem))
		} else if len(v.elems) > 0 {
			v.elems = v.elems[:len(v.elems)-1]
		}
	case "map":
		if len(v.keys) > 1 && g.r.IntN(2) == 0 {
			slices.Reverse(v.keys)
			g.reach["map keys reordered"]++
		} else if _, ok := v.m["z"]; !ok {
			v.keys = append(v.keys, "z")
			v.m["z"] = g.value(v.t.elem)
		}
	case "record":
		if len(v.elems) > 0 {
			i := g.r.IntN(len(v.elems))
			v.elems[i] = g.value(v.t.fields[i])
		}
	case "union":
		w := g.value(v.t)
		if w.varIdx != v.varIdx {
			g.reach["variant switched"]++
		}
		*v = *w
	}
}

// literal is v as an Ergo expression.
func (v *kValue) literal() string {
	switch v.t.kind {
	case "int":
		return strconv.FormatInt(v.i, 10)
	case "bool":
		return strconv.FormatBool(v.b)
	case "string":
		return strconv.Quote(v.s)
	case "float":
		if v.f == 0 && math.Signbit(v.f) {
			return "neg(0.0)"
		}
		return strconv.FormatFloat(v.f, 'f', -1, 64) + func() string {
			if v.f == math.Trunc(v.f) {
				return ".0"
			}
			return ""
		}()
	case "slice":
		es := make([]string, len(v.elems))
		for i, e := range v.elems {
			es[i] = e.literal()
		}
		return v.t.ergo() + "{" + strings.Join(es, ", ") + "}"
	case "map":
		es := make([]string, len(v.keys))
		for i, k := range v.keys {
			es[i] = strconv.Quote(k) + ": " + v.m[k].literal()
		}
		return v.t.ergo() + "{" + strings.Join(es, ", ") + "}"
	case "record":
		es := make([]string, len(v.elems))
		for i, e := range v.elems {
			es[i] = fmt.Sprintf("F%d: %s", i, e.literal())
		}
		return v.t.name + "{" + strings.Join(es, ", ") + "}"
	}
	vr := v.t.vars[v.varIdx]
	if len(vr.fields) == 0 {
		return vr.name
	}
	es := make([]string, len(v.elems))
	for i, e := range v.elems {
		es[i] = fmt.Sprintf("F%d: %s", i, e.literal())
	}
	return vr.name + "{" + strings.Join(es, ", ") + "}"
}

// equal is the reference: bits compares each float by its bits (the
// key's rule), else by == (Ergo's ==). A Map is its set of entries.
func (v *kValue) equal(w *kValue, bits bool) bool {
	switch v.t.kind {
	case "int":
		return v.i == w.i
	case "bool":
		return v.b == w.b
	case "string":
		return v.s == w.s
	case "float":
		if bits {
			return math.Float64bits(v.f) == math.Float64bits(w.f)
		}
		return v.f == w.f
	case "map":
		if len(v.m) != len(w.m) {
			return false
		}
		for k, e := range v.m {
			o, ok := w.m[k]
			if !ok || !e.equal(o, bits) {
				return false
			}
		}
		return true
	case "union":
		if v.varIdx != w.varIdx {
			return false
		}
	}
	if len(v.elems) != len(w.elems) {
		return false
	}
	for i := range v.elems {
		if !v.elems[i].equal(w.elems[i], bits) {
			return false
		}
	}
	return true
}

// program is n pairs of near-miss values, each of a type of its own, and
// a test per pair.
func (g *keyGen) program(n int) string {
	var tests strings.Builder
	for i := range n {
		t := g.typ(0)
		a := g.value(t)
		b := g.nearby(a)
		if t.kind == "union" && len(t.vars[a.varIdx].fields) == 0 {
			g.reach["empty variant"]++
		}
		same := a.equal(b, true)
		if same {
			g.reach["keys equal"]++
		} else {
			g.reach["keys differ"]++
		}
		fmt.Fprintf(&tests, "test Pair%d {\n\tlet a %s = %s\n\tlet b %s = %s\n\tlet (ka, _) = explore.KeyOf(a)\n\tlet (kb, _) = explore.KeyOf(b)\n\tassert (ka == kb) == %v\n\tassert (a == b) == %v\n}\n\n",
			i, t.ergo(), a.literal(), t.ergo(), b.literal(), same, a.equal(b, false))
		g.pairs++
	}
	// Distinct: 1,024 distinct states have 1,024 keys, which a key of 8
	// or 12 bits cannot (mc-hash-8-bits).
	g.pairs++
	return "import dense\nimport explore\n\nfunc neg(x float64) float64 { -x }\n\n" + g.decls.String() + tests.String() +
		"test Distinct {\n\tlet mut seen = dense.NewKeys()\n\tfor i in 0..1024 {\n\t\tlet (k, _) = explore.KeyOf(i)\n\t\tdense.AddKey(&seen, k.Hi, k.Lo)\n\t}\n\tassert dense.KeyCount(seen) == 1024\n}\n"
}
