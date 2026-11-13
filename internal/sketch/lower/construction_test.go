package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// openForm is a way a program builds or changes a provider value outside
// its constructor and operations (construction.go): the declarations it
// adds, the field or the operation it adds to P and Ops, and the text of
// what constructionOpen reports for it. rank orders the reports: the
// fields first, then the operations, then the declarations in source
// order (rank 2).
type openForm struct {
	name, decl, field, ctorField, op, opFunc, what string
	rank                                           int
}

var openForms = []openForm{
	{name: "literal", decl: "func mk() P { P{s: 2, xs: []Small{}FIELDS} }", what: "a literal builds one", rank: 2},
	{name: "update", decl: "func upd(p P) P { P{..p, s: 1} }", what: "a literal builds one", rank: 2},
	{name: "write", decl: "func poke(p *P) { p.s = 3 }", what: "an assignment writes its field", rank: 2},
	{name: "nested write", decl: "func pokeXs(p *P) { p.xs[0] = 1 }", what: "an assignment writes its field", rank: 2},
	{name: "address", decl: "func set(x *Small) {}\n\nfunc poke2(p *P) { set(&p.s) }", what: "a borrow of its field", rank: 2},
	{name: "dot borrow", decl: "func nudge(x *Small) {}\n\nfunc poke3(p *P) { p.s.nudge() }", what: "a call may write its field", rank: 2},
	{name: "conversion", decl: "type Q P\n\nfunc conv(q Q) P { P(q) }", what: "a conversion makes one", rank: 2},
	{name: "exported field", field: ", S int", ctorField: ", S: 0", what: "its field S is exported", rank: 0},
	{name: "self field", field: ", more []P", ctorField: ", more: []P{}", what: "its field more holds a P", rank: 0},
	{name: "operation parameter", op: "Mix(o P),", opFunc: "func Mix(p *P, o P) {}", what: "its operation Mix takes a P besides its receiver", rank: 1},
	{name: "operation result", op: "Fork() P,", opFunc: "func Fork(p P) P { p }", what: "its operation Fork returns a P", rank: 1},
}

// closedDecoys read, copy and change P only through its constructor and
// operations, or write it inside them: none opens it.
var closedDecoys = []string{
	"func peek(p P) int { int(p.s) }",
	"func use() int {\n\tlet mut p = NewP()\n\tp.Inc()\n\tp.Reset()\n\tp.Read()\n}",
	"func pass(p P) Small { step(p.s) }",
	"func count(p P) int { len(p.xs) }",
	"func fresh() P { NewP() }",
	"func copyOf(p P) P {\n\tlet q = p\n\tq\n}",
}

// constructionProgram is the program of seed: a provider P with a
// constructor, the operations Inc, Read and Reset (Reset writes a literal
// inside an operation), the decoys, and a random set of open forms in a
// random order; want is the report the model expects, "" when closed, and form the name of the form it names.
func constructionProgram(seed int64) (src, want, form string) {
	rng := rand.New(rand.NewSource(seed))
	var forms []openForm
	for _, f := range rng.Perm(len(openForms)) {
		if rng.Intn(4) == 0 {
			forms = append(forms, openForms[f])
		}
	}
	field, ctorField, ops, opFuncs := "", "", "", ""
	var decls []string
	best := -1
	for i, f := range forms {
		field += f.field
		ctorField += f.ctorField
		ops += f.op
		if f.opFunc != "" {
			opFuncs += f.opFunc + "\n\n"
		}
		if f.decl != "" {
			decls = append(decls, f.decl)
		}
		// The model's order: fields in declaration order (this loop's),
		// then operations by name (the conversion's table), then
		// declarations in source order (this loop's).
		if best < 0 || f.rank < forms[best].rank || f.rank == 1 && forms[best].rank == 1 && f.op < forms[best].op {
			best = i
		}
	}
	if best >= 0 {
		want, form = forms[best].what, forms[best].name
	}
	for _, d := range closedDecoys {
		if rng.Intn(2) == 0 {
			at := rng.Intn(len(decls) + 1)
			decls = append(decls[:at], append([]string{d}, decls[at:]...)...)
		}
	}
	var b strings.Builder
	b.WriteString("type Small int where 0 <= it && it < 4\n\n")
	fmt.Fprintf(&b, "type P { s Small, xs []Small%s } implements Ops\n\n", field)
	fmt.Fprintf(&b, "func NewP() P { P{s: 0, xs: []Small{}%s} }\n\n", ctorField)
	b.WriteString("func step(n Small) Small {\n\tmatch int(n) {\n\tcase 0:\n\t\t1\n\tcase 1:\n\t\t2\n\tcase 2:\n\t\t3\n\tcase _:\n\t\t0\n\t}\n}\n\n")
	b.WriteString("func Inc(p *P) { p.s = step(p.s) }\n\nfunc Read(p P) int { int(p.s) }\n\n")
	fmt.Fprintf(&b, "func Reset(p *P) { *p = P{s: 0, xs: []Small{}%s} }\n\n", ctorField)
	b.WriteString(opFuncs)
	fmt.Fprintf(&b, "type Ops {\n\tInc(),\n\tRead() int,\n\tReset(),\n\t%s\n\tlaw Same() { Inc(); Reset() } == { Reset() },\n}\n\n", ops)
	for _, d := range decls {
		b.WriteString(strings.ReplaceAll(d, "FIELDS", ctorField) + "\n\n")
	}
	b.WriteString("func main() {}\n")
	return b.String(), want, form
}

// TestConstructionModel is the closed-construction rule's property
// (types.md, Laws 14, Proved states; v1 audit 3, G1): over generated
// providers with a random set of the ways to build or change a value
// outside the constructor and the operations (a literal, a record update,
// a field write, a borrow of a field, a conversion, an exported field, a
// field that holds the provider, an operation that takes or returns one),
// mixed with reads, copies and operation calls that keep it closed,
// constructionOpen reports open exactly when the generator added a form,
// and names the one the model expects first. In process: the checker's
// result.
//
// ERGO_CONSTRUCTION_SEED=N replays one seed.
func TestConstructionModel(t *testing.T) {
	seeds := []int64{}
	if s := os.Getenv("ERGO_CONSTRUCTION_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range int64(256) {
			seeds = append(seeds, i+1)
		}
	}
	reached := map[string]bool{"closed": false}
	for _, seed := range seeds {
		src, want, form := constructionProgram(seed)
		u := checkPackage(t, "main", []pkgSource{{testSourcePath, src}}, nil)
		var got string
		found := false
		for prov, rules := range newRewriter(u.Package, nil).rules {
			if prov.Obj.Name != "P" || len(rules) == 0 {
				continue
			}
			found = true
			if o := constructionOpen(prov, rules[0].ctor, rules[0].conv); o != nil {
				got = o.what
			}
		}
		if !found {
			t.Fatalf("seed %d: P has no rewrite rule\n%s", seed, src)
		}
		if !strings.Contains(got, want) || (got == "") != (want == "") {
			t.Fatalf("seed %d: constructionOpen reports %q, the model %q\nsource:\n%s\nreplay: ERGO_CONSTRUCTION_SEED=%d go test -run TestConstructionModel ./internal/sketch/lower", seed, got, want, src, seed)
		}
		if want == "" {
			reached["closed"] = true
		} else {
			reached[form] = true
		}
	}
	if len(seeds) > 1 {
		for _, f := range openForms {
			if !reached[f.name] {
				t.Errorf("no seed reports %q (%s)", f.what, f.name)
			}
		}
		if !reached["closed"] {
			t.Error("no seed is closed")
		}
	}
}
