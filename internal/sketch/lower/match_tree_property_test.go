package lower

import (
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestMatchTreeAgrees (M1, check B) is a generated property: the decision
// tree selects the same arm with the same bindings as the flat lowering,
// and both select the first arm whose pattern matches, for every value.
//
// A seeded generator writes groups of types (treeGen.group): unions of 2
// to 4 variants, records that are not variants, and named tuples whose two
// first elements, and whose two last, often share a type; a field is int
// ({0, 1, 2}), string ({"a", "b"}), bool, or an earlier type of the group
// with at most 64 values, so unions, records and tuples nest. Matches have
// 1 to 3 subjects of these types (or bool, or int; a third of them repeat
// a subject's type) and 2 to 8 arms: wildcards, binds, type tests, `as`,
// record patterns with 0 to 2 written fields, tuple patterns with `_`
// mixed in, constants, and or-patterns of 2 alternatives that bind the
// same names, often from different columns, sometimes two same-typed names
// swapped between the alternatives. About half the matches end
// with `case _`; the others end with one arm per variant (or bool value)
// of their first subject. The checker filters them: an arm it reports
// unreachable (S108) is dropped, a match it reports not total (S103) is
// generated again. Each arm yields its index and its bindings as text, so
// the output shows the arm and where each binding came from. The arms
// yield to each destination: a function's tail, a let, an assignment in
// a statement match, and a return.
//
// A reference model in Go walks the arms in source order. One program
// holds every match and prints one line per (match, value), the values
// written out; it is lowered twice, with the tree and flat (the test hook
// lowerer.matchTest), built once, and both outputs must equal the model's.
// Fixed probes add a match in a loop whose arms break and continue (inline
// in nested switches and shared), an expression match whose shared arm
// does not terminate, and a match the tree gives up on
// (treeFallbackProbes). The hook's counts must show that the trees split a
// constructor column, a constant column, a tuple and a record, and that
// the tree gave up on the fallback probes.
//
// -match.seed=N runs the seed N alone, and checks the drop rate of the
// fixed set only. A failure prints the replay
// command, the match's Ergo source, and its lines from the model, the tree
// and the flat program side by side.

var treeSeed = flag.Uint64("match.seed", 0, "run TestMatchTreeAgrees on this seed alone")

// treeSeeds are the fixed seeds; each writes treeGroups groups.
var treeSeeds = []uint64{1, 2, 3, 4, 5, 6}

const (
	treeGroups       = 3   // groups per seed
	treeMatches      = 3   // matches per group
	treeSampleValues = 256 // plan §1.3 B: 256 seeded samples past 256 values
)

type gKind int

const (
	gInt gKind = iota
	gString
	gBool
	gUnionKind
	gTupleKind  // a named tuple: union holds one variant, its elements
	gRecordKind // a record that is not a variant: union holds one variant, its fields
)

// gType is a type. A tuple or a record is modelled as a union of one
// variant named as the type, so values, patterns and the model share one
// form; only its Ergo text differs.
type gType struct {
	kind  gKind
	union *gUnion
}

// product reports a tuple or a record: a value the tree destructures
// without a test (matchtree.go, expand).
func (t *gType) product() bool { return t.kind == gTupleKind || t.kind == gRecordKind }

// size is the number of values of t.
func (t *gType) size() int {
	switch t.kind {
	case gInt:
		return 3
	case gString, gBool:
		return 2
	}
	n := 0
	for _, vr := range t.union.variants {
		p := 1
		for _, f := range vr.fields {
			p *= f.size()
		}
		n += p
	}
	return n
}

type gUnion struct {
	name     string
	variants []*gVariant
}

type gVariant struct {
	name   string
	fields []*gType // field k is named F<k>
}

// gVal is a value: a variant index and its fields, an int, a string index
// or a bool (0 or 1).
type gVal struct {
	t      *gType
	n      int
	fields []gVal
}

type gPatKind int

const (
	pWild gPatKind = iota
	pBind
	pTest
	pAs
	pRecord
	pConst
)

type gPat struct {
	kind    gPatKind
	t       *gType
	name    string // pBind, pAs
	variant int    // pTest, pRecord
	fields  map[int]*gPat
	inner   *gPat // pAs
	val     gVal  // pConst
	// joined: a pAs of an or-pattern whose alternatives test different
	// variants, so its name binds at their join (rows.md, Totality 1).
	joined bool
}

// gJoinedAs is `V as name` for the variant v of t, in an or-pattern whose
// alternatives bind name at their join.
func gJoinedAs(t *gType, v int, name string) *gPat {
	return &gPat{kind: pAs, t: t, name: name, variant: v, joined: true, inner: &gPat{kind: pTest, t: t, variant: v}}
}

// gTwoVariants is two different variants of the union t, in either order.
func gTwoVariants(g *treeGen, t *gType) (int, int) {
	n := len(t.union.variants)
	v := g.r.IntN(n)
	return v, (v + 1 + g.r.IntN(n-1)) % n
}

// gArm is an arm: one or two alternatives, each a pattern per subject.
type gArm struct {
	alts  [][]*gPat
	names []string // its bindings, in output order
	wild  bool     // case _
	line  int      // its case line in the match's function
	alt2  int      // the column of its second alternative
}

type gMatch struct {
	id       string
	subjects []*gType
	arms     []*gArm
	dest     int
}

type gGroup struct {
	prefix string
	types  []*gType // unions, records and tuples, in declaration order
}

type treeGen struct {
	r    *rand.Rand
	seed uint64
}

var (
	gIntT    = &gType{kind: gInt}
	gStringT = &gType{kind: gString}
	gBoolT   = &gType{kind: gBool}
)

// group writes a group's types, each over the ones before it: 0 to 2 leaf
// unions of basic fields; a record R0 over them; a tuple T0 over them and
// R0, whose two first elements often share a type; the union U0, whose
// variants may hold one of them; and a record R1 that may hold T0, R0 or
// U0. A compound field's type has at most 64 values, so a value domain
// stays small enough to enumerate or sample.
func (g *treeGen) group(prefix string) *gGroup {
	gr := &gGroup{prefix: prefix}
	var compound []*gType
	for u := 1; u < 1+g.r.IntN(3); u++ {
		compound = append(compound, g.union(gr, fmt.Sprintf("%sU%d", prefix, u), nil))
	}
	r0 := g.productType(gr, gRecordKind, prefix+"R0", compound)
	compound = append(compound, r0)
	t0 := g.productType(gr, gTupleKind, prefix+"T0", compound)
	compound = append(compound, t0)
	u0 := g.union(gr, prefix+"U0", compound)
	g.productType(gr, gRecordKind, prefix+"R1", []*gType{r0, t0, u0})
	return gr
}

// field is a field type: basic, or one of compound with at most 64 values.
func (g *treeGen) field(compound []*gType) *gType {
	if len(compound) > 0 && g.r.IntN(5) < 2 {
		if t := compound[g.r.IntN(len(compound))]; t.size() <= 64 {
			return t
		}
	}
	return []*gType{gIntT, gIntT, gStringT, gBoolT}[g.r.IntN(4)]
}

// union adds a union of 2 to 4 variants of 0 to 2 fields; a variant holds
// at most one field of a compound type.
func (g *treeGen) union(gr *gGroup, name string, compound []*gType) *gType {
	un := &gUnion{name: name}
	for v := 0; v < 2+g.r.IntN(3); v++ {
		vr := &gVariant{name: fmt.Sprintf("%sV%d", name, v)}
		for f := 0; f < g.r.IntN(3); f++ {
			ft := g.field(compound)
			if ft.kind == gUnionKind || ft.product() {
				compound = nil // one compound field per variant
			}
			vr.fields = append(vr.fields, ft)
		}
		un.variants = append(un.variants, vr)
	}
	t := &gType{kind: gUnionKind, union: un}
	gr.types = append(gr.types, t)
	return t
}

// productType adds a record of 1 or 2 fields or a tuple of 2 or 3
// elements; half the tuples repeat their first element's type.
func (g *treeGen) productType(gr *gGroup, kind gKind, name string, compound []*gType) *gType {
	vr := &gVariant{name: name}
	n := 1 + g.r.IntN(2)
	if kind == gTupleKind {
		n++
	}
	for range n {
		vr.fields = append(vr.fields, g.field(compound))
	}
	if kind == gTupleKind && g.r.IntN(2) == 0 {
		vr.fields[1] = vr.fields[0]
	}
	if kind == gTupleKind && n == 3 && g.r.IntN(2) == 0 {
		// Elements 1 and 2 share a type too, so a swap of the two later
		// columns is visible (verifier mutant V3b).
		vr.fields[2] = vr.fields[1]
	}
	t := &gType{kind: kind, union: &gUnion{name: name, variants: []*gVariant{vr}}}
	gr.types = append(gr.types, t)
	return t
}

func (g *treeGen) subjectType(gr *gGroup) *gType {
	switch k := g.r.IntN(20); {
	case k < 18:
		return gr.types[g.r.IntN(len(gr.types))]
	case k == 18:
		return gBoolT
	default:
		return gIntT
	}
}

// pat is a pattern of type t at depth d; names allocates bindings, or is
// nil where the pattern binds nothing.
func (g *treeGen) pat(t *gType, d int, names *[]string) *gPat {
	bind := func() string {
		name := "b" + strconv.Itoa(len(*names))
		*names = append(*names, name)
		return name
	}
	k := g.r.IntN(100)
	if t.product() {
		switch {
		case k < 5 || d >= 3:
			return &gPat{kind: pWild, t: t}
		case k < 10 && names != nil:
			return &gPat{kind: pBind, t: t, name: bind()}
		case k < 14 && t.kind == gRecordKind:
			return &gPat{kind: pTest, t: t}
		case k < 26 && names != nil:
			return &gPat{kind: pAs, t: t, inner: g.record(t, 0, d, names), name: bind()}
		}
		return g.record(t, 0, d, names)
	}
	if t.kind != gUnionKind {
		switch {
		case k < 20:
			return &gPat{kind: pWild, t: t}
		case k < 28 && names != nil:
			return &gPat{kind: pBind, t: t, name: bind()}
		}
		return &gPat{kind: pConst, t: t, val: g.value(t)}
	}
	v := g.r.IntN(len(t.union.variants))
	switch {
	case k < 8:
		return &gPat{kind: pWild, t: t}
	case k < 15 && names != nil:
		return &gPat{kind: pBind, t: t, name: bind()}
	case k < 45 || d >= 2:
		return &gPat{kind: pTest, t: t, variant: v}
	case k < 55 && names != nil:
		inner := &gPat{kind: pTest, t: t, variant: v}
		if g.r.IntN(2) == 0 {
			inner = g.record(t, v, d, names)
		}
		return &gPat{kind: pAs, t: t, inner: inner, name: bind()}
	}
	return g.record(t, v, d, names)
}

func (g *treeGen) record(t *gType, v, d int, names *[]string) *gPat {
	p := &gPat{kind: pRecord, t: t, variant: v, fields: map[int]*gPat{}}
	fs := t.union.variants[v].fields
	written := min(len(fs), g.r.IntN(3))
	if t.kind == gTupleKind {
		written = min(len(fs), 1+g.r.IntN(3)) // a tuple pattern writes `_` for the rest
	}
	for _, f := range g.r.Perm(len(fs))[:written] {
		p.fields[f] = g.pat(fs[f], d+1, names)
	}
	if len(p.fields) == 0 && t.kind != gTupleKind {
		// The checker asks for a bare type test: write case V, not V{}.
		return &gPat{kind: pTest, t: t, variant: v}
	}
	return p
}

// value is a random value of t.
func (g *treeGen) value(t *gType) gVal {
	switch t.kind {
	case gInt:
		return gVal{t: t, n: g.r.IntN(3)}
	case gString, gBool:
		return gVal{t: t, n: g.r.IntN(2)}
	}
	v := g.r.IntN(len(t.union.variants))
	return g.variant(t, v)
}

func (g *treeGen) variant(t *gType, v int) gVal {
	out := gVal{t: t, n: v}
	for _, ft := range t.union.variants[v].fields {
		out.fields = append(out.fields, g.value(ft))
	}
	return out
}

// witness is a value that p matches, random elsewhere.
func (g *treeGen) witness(p *gPat) gVal {
	switch p.kind {
	case pConst:
		return p.val
	case pTest:
		return g.variant(p.t, p.variant)
	case pAs:
		return g.witness(p.inner)
	case pRecord:
		out := g.variant(p.t, p.variant)
		for f, fp := range p.fields {
			out.fields[f] = g.witness(fp)
		}
		return out
	}
	return g.value(p.t)
}

// match generates a match over gr. Its arms are filtered by the checker
// later (treeFilter).
func (g *treeGen) match(gr *gGroup, id string) *gMatch {
	m := &gMatch{id: id, dest: g.r.IntN(4)}
	for range 1 + g.r.IntN(3) {
		m.subjects = append(m.subjects, g.subjectType(gr))
	}
	if n := len(m.subjects); n > 1 && g.r.IntN(3) == 0 {
		// Two subjects of one type, so an or-pattern can bind two
		// same-typed names (arm1).
		m.subjects[n-1] = m.subjects[g.r.IntN(n-1)]
	}
	for range 2 + g.r.IntN(7) {
		m.arms = append(m.arms, g.arm(m))
	}
	if g.r.IntN(2) == 0 {
		m.arms = append(m.arms, &gArm{wild: true})
		return m
	}
	// One arm per variant (or bool value) of the first subject that some
	// value the arms so far miss holds; an int subject needs a wildcard.
	first := m.subjects[0]
	missed := g.missed(m)
	tail := func(p *gPat) {
		needed := false
		for _, vs := range missed {
			needed = needed || p.matches(vs[0], map[string]gVal{})
		}
		if !needed {
			return
		}
		alt := []*gPat{p}
		for _, t := range m.subjects[1:] {
			alt = append(alt, &gPat{kind: pWild, t: t})
		}
		m.arms = append(m.arms, &gArm{alts: [][]*gPat{alt}})
	}
	switch first.kind {
	case gUnionKind:
		for v := range first.union.variants {
			tail(&gPat{kind: pTest, t: first, variant: v})
		}
	case gBool:
		tail(&gPat{kind: pConst, t: first, val: gVal{t: first, n: 0}})
		tail(&gPat{kind: pConst, t: first, val: gVal{t: first, n: 1}})
	default:
		tail(&gPat{kind: pWild, t: first})
	}
	return m
}

// arm is a random arm whose every alternative tests something: an
// irrefutable arm would make every later arm unreachable, and a lone `_`
// is the match's one case _ (the generator adds it last, or not at all).
func (g *treeGen) arm(m *gMatch) *gArm {
	for {
		a := g.arm1(m)
		ok := true
		for _, alt := range a.alts {
			ok = ok && slices.ContainsFunc(alt, (*gPat).refutable)
		}
		if ok {
			return a
		}
	}
}

// refutable reports a pattern that some value of its type fails.
func (p *gPat) refutable() bool {
	switch p.kind {
	case pConst:
		return true
	case pTest:
		return !p.t.product()
	case pAs:
		return p.inner.refutable()
	case pRecord:
		if !p.t.product() {
			return true
		}
		for _, f := range p.fields {
			if f.refutable() {
				return true
			}
		}
	}
	return false
}

func (g *treeGen) arm1(m *gMatch) *gArm {
	a := &gArm{}
	if g.r.IntN(4) != 0 {
		var alt []*gPat
		for _, t := range m.subjects {
			alt = append(alt, g.pat(t, 0, &a.names))
		}
		a.alts = [][]*gPat{alt}
		return a
	}
	// An or-pattern: both alternatives bind the same names, maybe from
	// different columns.
	var alts [2][]*gPat
	for k := range alts {
		for _, t := range m.subjects {
			alts[k] = append(alts[k], g.pat(t, 0, nil))
		}
	}
	i := g.r.IntN(len(m.subjects))
	t := m.subjects[i]
	var same []int
	for j, u := range m.subjects {
		if gSameType(t, u) {
			same = append(same, j)
		}
	}
	j := same[g.r.IntN(len(same))]
	other := slices.DeleteFunc(slices.Clone(same), func(x int) bool { return x == i })
	switch k := g.r.IntN(4); {
	case k == 3 && len(other) > 0:
		// Two same-typed names, swapped between the alternatives: a
		// shared leaf that gives a name the other's value is visible
		// (verifier mutant V9).
		j = other[g.r.IntN(len(other))]
		mk := func(name string) *gPat { return &gPat{kind: pBind, t: t, name: name} }
		if t.kind == gUnionKind {
			v := g.r.IntN(len(t.union.variants))
			mk = func(name string) *gPat {
				return &gPat{kind: pAs, t: t, name: name, inner: &gPat{kind: pTest, t: t, variant: v}}
			}
		}
		alts[0][i], alts[0][j] = mk("b0"), mk("b1")
		alts[1][i], alts[1][j] = mk("b1"), mk("b0")
		a.names = []string{"b0", "b1"}
	case k == 1:
		alts[0][i] = &gPat{kind: pBind, t: t, name: "b0"}
		alts[1][j] = &gPat{kind: pBind, t: t, name: "b0"}
		a.names = []string{"b0"}
	case k == 2 && t.kind == gUnionKind && g.r.IntN(2) == 0 && len(t.union.variants) > 1:
		// Two variants bound by one name: it binds at their union
		// (rows.md, Totality 1; fu8a).
		v, w := gTwoVariants(g, t)
		alts[0][i], alts[1][j] = gJoinedAs(t, v, "b0"), gJoinedAs(t, w, "b0")
		a.names = []string{"b0"}
	case k == 2 && t.kind == gUnionKind:
		v := g.r.IntN(len(t.union.variants))
		alts[0][i] = &gPat{kind: pAs, t: t, name: "b0", inner: &gPat{kind: pTest, t: t, variant: v}}
		alts[1][j] = &gPat{kind: pAs, t: t, name: "b0", inner: &gPat{kind: pTest, t: t, variant: v}}
		a.names = []string{"b0"}
	}
	a.alts = [][]*gPat{alts[0], alts[1]}
	return a
}

func gSameType(a, b *gType) bool {
	return a.kind == b.kind && a.union == b.union
}

// Ergo text.

func (t *gType) ergo() string {
	switch t.kind {
	case gInt:
		return "int"
	case gString:
		return "string"
	case gBool:
		return "bool"
	}
	return t.union.name
}

func (v gVal) ergo() string {
	switch v.t.kind {
	case gInt:
		return strconv.Itoa(v.n)
	case gString:
		return strconv.Quote([]string{"a", "b"}[v.n])
	case gBool:
		return []string{"false", "true"}[v.n]
	}
	vr := v.t.union.variants[v.n]
	if v.t.kind == gTupleKind {
		var es []string
		for _, f := range v.fields {
			es = append(es, f.ergo())
		}
		return vr.name + "((" + strings.Join(es, ", ") + "))"
	}
	if len(vr.fields) == 0 {
		return vr.name
	}
	var fs []string
	for k, f := range v.fields {
		fs = append(fs, fmt.Sprintf("F%d: %s", k, f.ergo()))
	}
	return vr.name + "{" + strings.Join(fs, ", ") + "}"
}

// show is the model's text of a value; the generated show functions write
// the same.
func (v gVal) show() string {
	switch v.t.kind {
	case gInt:
		return strconv.Itoa(v.n)
	case gString:
		return []string{"a", "b"}[v.n]
	case gBool:
		return []string{"false", "true"}[v.n]
	}
	var fs []string
	for _, f := range v.fields {
		fs = append(fs, f.show())
	}
	if v.t.kind == gTupleKind {
		return "(" + strings.Join(fs, ",") + ")"
	}
	return v.t.union.variants[v.n].name + "{" + strings.Join(fs, ",") + "}"
}

func (p *gPat) ergo() string {
	switch p.kind {
	case pWild:
		return "_"
	case pBind:
		return p.name
	case pTest:
		return p.t.union.variants[p.variant].name
	case pAs:
		return p.inner.ergo() + " as " + p.name
	case pRecord:
		if p.t.kind == gTupleKind {
			es := make([]string, len(p.t.union.variants[0].fields))
			for k := range es {
				es[k] = "_"
				if f, ok := p.fields[k]; ok {
					es[k] = f.ergo()
				}
			}
			return "(" + strings.Join(es, ", ") + ")"
		}
		var fs []string
		for _, f := range slices.Sorted(maps.Keys(p.fields)) {
			fs = append(fs, fmt.Sprintf("F%d: %s", f, p.fields[f].ergo()))
		}
		return p.t.union.variants[p.variant].name + "{" + strings.Join(fs, ", ") + "}"
	}
	return p.val.ergo()
}

// showCall is the Ergo text that shows the binding x of p's bound type.
func showCall(p *gPat, x string) string {
	if p.kind == pAs && !p.joined {
		return "show" + p.t.union.variants[p.inner.variant].name + "(" + x + ")"
	}
	switch p.t.kind {
	case gInt:
		return "strconv.Itoa(" + x + ")"
	case gString:
		return x
	case gBool:
		return "showBool(" + x + ")"
	}
	return "show" + p.t.union.name + "(" + x + ")"
}

// binders lists the bind and as patterns of an alternative by name.
func binders(alt []*gPat) map[string]*gPat {
	out := map[string]*gPat{}
	var walk func(p *gPat)
	walk = func(p *gPat) {
		switch p.kind {
		case pBind:
			out[p.name] = p
		case pAs:
			out[p.name] = p
			walk(p.inner)
		case pRecord:
			for _, f := range p.fields {
				walk(f)
			}
		}
	}
	for _, p := range alt {
		walk(p)
	}
	return out
}

// groupDecls is the Ergo text of a group's types and show functions.
func (gr *gGroup) decls() string {
	var b strings.Builder
	for _, t := range gr.types {
		un := t.union
		switch t.kind {
		case gTupleKind:
			fs := un.variants[0].fields
			var types, xs, parts []string
			for k, ft := range fs {
				types = append(types, ft.ergo())
				x := fmt.Sprintf("x%d", k)
				xs = append(xs, x)
				parts = append(parts, showCall(&gPat{kind: pBind, t: ft}, x))
			}
			fmt.Fprintf(&b, "type %s (%s)\n\n", un.name, strings.Join(types, ", "))
			fmt.Fprintf(&b, "func show%s(t %s) string {\n\tmatch t {\n\tcase (%s):\n\t\t\"(\" + %s + \")\"\n\t}\n}\n\n",
				un.name, un.name, strings.Join(xs, ", "), strings.Join(parts, " + \",\" + "))
			continue
		case gRecordKind:
			vr := un.variants[0]
			var fs, parts []string
			for f, ft := range vr.fields {
				fs = append(fs, fmt.Sprintf("F%d %s", f, ft.ergo()))
				parts = append(parts, showCall(&gPat{kind: pBind, t: ft}, fmt.Sprintf("r.F%d", f)))
			}
			fmt.Fprintf(&b, "type %s { %s }\n\n", vr.name, strings.Join(fs, ", "))
			fmt.Fprintf(&b, "func show%s(r %s) string {\n\t%s + %s + \"}\"\n}\n\n", vr.name, vr.name, strconv.Quote(vr.name+"{"), strings.Join(parts, " + \",\" + "))
			continue
		}
		var names []string
		for _, vr := range un.variants {
			var fs []string
			for f, ft := range vr.fields {
				fs = append(fs, fmt.Sprintf("F%d %s", f, ft.ergo()))
			}
			fmt.Fprintf(&b, "type %s { %s }\n", vr.name, strings.Join(fs, ", "))
			names = append(names, vr.name)
		}
		fmt.Fprintf(&b, "type %s = %s\n\n", un.name, strings.Join(names, " | "))
		fmt.Fprintf(&b, "func show%s(v %s) string {\n\tmatch v {\n", un.name, un.name)
		for _, vr := range un.variants {
			if len(vr.fields) == 0 {
				fmt.Fprintf(&b, "\tcase %s:\n\t\t\"%s{}\"\n", vr.name, vr.name)
			} else {
				fmt.Fprintf(&b, "\tcase %s as r:\n\t\tshow%s(r)\n", vr.name, vr.name)
			}
		}
		b.WriteString("\t}\n}\n\n")
		for _, vr := range un.variants {
			var parts []string
			for f, ft := range vr.fields {
				parts = append(parts, showCall(&gPat{kind: pBind, t: ft}, fmt.Sprintf("r.F%d", f)))
			}
			body := strconv.Quote(vr.name + "{}")
			if len(parts) > 0 {
				body = strconv.Quote(vr.name+"{") + " + " + strings.Join(parts, " + \",\" + ") + " + \"}\""
			}
			fmt.Fprintf(&b, "func show%s(r %s) string {\n\t%s\n}\n\n", vr.name, vr.name, body)
		}
	}
	return b.String()
}

// fn is the Ergo text of the match's function. It records each arm's
// case line, counted from the function's first line.
func (m *gMatch) fn() string {
	var b strings.Builder
	line := 1
	w := func(format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		line += strings.Count(s, "\n")
		b.WriteString(s)
	}
	var params, subjects []string
	for i, t := range m.subjects {
		params = append(params, fmt.Sprintf("a%d %s", i, t.ergo()))
		subjects = append(subjects, fmt.Sprintf("a%d", i))
	}
	w("func %s(%s) string {\n", m.id, strings.Join(params, ", "))
	head := "match " + subjects[0] + " {\n"
	if len(subjects) > 1 {
		head = "match (" + strings.Join(subjects, ", ") + ") {\n"
	}
	switch m.dest {
	case 0, 3:
		w("\t%s", head)
	case 1:
		w("\tlet r = %s", head)
	case 2:
		w("\tlet mut r = \"\"\n\t%s", head)
	}
	for k, a := range m.arms {
		a.line = line
		if a.wild {
			w("\tcase _:\n")
		} else {
			var alts []string
			for _, alt := range a.alts {
				var ps []string
				for _, p := range alt {
					ps = append(ps, p.ergo())
				}
				if len(ps) == 1 {
					alts = append(alts, ps[0])
				} else {
					alts = append(alts, "("+strings.Join(ps, ", ")+")")
				}
			}
			if len(alts) == 2 {
				a.alt2 = len("\tcase "+alts[0]+", ") + 1
			}
			w("\tcase %s:\n", strings.Join(alts, ", "))
		}
		val := strconv.Quote(strconv.Itoa(k))
		if !a.wild {
			bs := binders(a.alts[0])
			for _, n := range a.names {
				val += " + \":\" + " + showCall(bs[n], n)
			}
		}
		switch m.dest {
		case 0, 1:
			w("\t\t%s\n", val)
		case 2:
			w("\t\tr = %s\n", val)
		case 3:
			w("\t\treturn %s\n", val)
		}
	}
	w("\t}\n")
	if m.dest == 1 || m.dest == 2 {
		w("\tr\n")
	}
	w("}\n")
	return b.String()
}

// Model.

func (p *gPat) matches(v gVal, binds map[string]gVal) bool {
	switch p.kind {
	case pWild:
		return true
	case pBind:
		binds[p.name] = v
		return true
	case pTest:
		return v.n == p.variant
	case pAs:
		if !p.inner.matches(v, binds) {
			return false
		}
		binds[p.name] = v
		return true
	case pRecord:
		if v.n != p.variant {
			return false
		}
		for f, fp := range p.fields {
			if !fp.matches(v.fields[f], binds) {
				return false
			}
		}
		return true
	}
	return v.n == p.val.n
}

// model is the line the match yields for the values vs: the first arm
// that matches, with its bindings; ok is false when no arm matches.
func (m *gMatch) model(vs []gVal) (string, bool) {
	for k, a := range m.arms {
		if a.wild {
			return strconv.Itoa(k), true
		}
		for _, alt := range a.alts {
			binds := map[string]gVal{}
			ok := true
			for i, p := range alt {
				if !p.matches(vs[i], binds) {
					ok = false
					break
				}
			}
			if ok {
				out := strconv.Itoa(k)
				for _, n := range a.names {
					out += ":" + binds[n].show()
				}
				return out, true
			}
		}
	}
	return "", false
}

// domain is every value of t.
func domain(t *gType) []gVal {
	switch t.kind {
	case gInt:
		return []gVal{{t: t, n: 0}, {t: t, n: 1}, {t: t, n: 2}}
	case gString, gBool:
		return []gVal{{t: t, n: 0}, {t: t, n: 1}}
	}
	var out []gVal
	for v, vr := range t.union.variants {
		vals := [][]gVal{nil}
		for _, ft := range vr.fields {
			var next [][]gVal
			for _, prefix := range vals {
				for _, x := range domain(ft) {
					next = append(next, append(slices.Clone(prefix), x))
				}
			}
			vals = next
		}
		for _, fs := range vals {
			out = append(out, gVal{t: t, n: v, fields: fs})
		}
	}
	return out
}

// missed is the values the arms so far match none of, among the whole
// domain or, past 4,096 values, 1,024 samples.
func (g *treeGen) missed(m *gMatch) [][]gVal {
	var all [][]gVal
	if domainSize(m, 4096) <= 4096 {
		all = product(m)
	} else {
		for range 1024 {
			var vs []gVal
			for _, t := range m.subjects {
				vs = append(vs, g.value(t))
			}
			all = append(all, vs)
		}
	}
	var out [][]gVal
	for _, vs := range all {
		if _, ok := m.model(vs); !ok {
			out = append(out, vs)
		}
	}
	return out
}

// domainSize is the size of the subjects' value domain, or a number over
// limit.
func domainSize(m *gMatch, limit int) int {
	size := 1
	for _, t := range m.subjects {
		size *= len(domain(t))
		if size > limit {
			break
		}
	}
	return size
}

// product is the subjects' whole value domain.
func product(m *gMatch) [][]gVal {
	out := [][]gVal{nil}
	for _, t := range m.subjects {
		var next [][]gVal
		for _, prefix := range out {
			for _, x := range domain(t) {
				next = append(next, append(slices.Clone(prefix), x))
			}
		}
		out = next
	}
	return out
}

// values is the whole value domain of the subjects when it has 256 values
// or fewer; otherwise seeded samples and a witness of each alternative.
func (g *treeGen) values(m *gMatch) [][]gVal {
	if domainSize(m, 256) <= 256 {
		return product(m)
	}
	var out [][]gVal
	for range treeSampleValues {
		var vs []gVal
		for _, t := range m.subjects {
			vs = append(vs, g.value(t))
		}
		out = append(out, vs)
	}
	for _, a := range m.arms {
		for _, alt := range a.alts {
			var vs []gVal
			for _, p := range alt {
				vs = append(vs, g.witness(p))
			}
			out = append(out, vs)
		}
	}
	return out
}

// treeShowPrelude opens every generated source: the import and the show
// functions of the basic types.
const treeShowPrelude = "import strconv\n\nfunc showBool(b bool) string {\n\tif b {\n\t\t\"true\"\n\t} else {\n\t\t\"false\"\n\t}\n}\n\nfunc showInt(n int) string {\n\tstrconv.Itoa(n)\n}\n\n"

// treeFilter checks each match with its group's declarations: it drops
// an arm the checker reports unreachable and reports a match the checker
// reports not total. Any other diagnostic is a generator defect.
func treeFilter(t *testing.T, decls string, m *gMatch) (dropped int, total bool) {
	for {
		pre := treeShowPrelude + decls
		fn := m.fn()
		src := pre + fn + "\nfunc main() {}\n"
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("generator defect (seed in %s): %v\n%s", m.id, err, src)
		}
		_, diags := check.Check("main", []*syntax.File{file}, nil)
		if len(diags) == 0 {
			return dropped, true
		}
		first := strings.Count(pre, "\n") + 1
		drop, alt := -1, -1
		for _, d := range diags {
			switch {
			case d.Code == check.CodeNotTotal:
				return dropped, false
			case d.Code == "S108" && strings.Contains(d.Message, "unreachable"):
				if drop >= 0 {
					continue // one at a time: dropping it may change the rest
				}
				for k, a := range m.arms {
					if first+a.line-1 == d.Line {
						drop = k
						if len(a.alts) == 2 && d.Column >= a.alt2 {
							alt = 1
						} else if len(a.alts) == 2 {
							alt = 0
						}
					}
				}
				if drop < 0 {
					t.Fatalf("generator defect (%s): S108 at line %d is on no case: %v\n%s", m.id, d.Line, d, src)
				}
			default:
				t.Fatalf("generator defect (%s): %v\n%s", m.id, d, src)
			}
		}
		if alt >= 0 {
			// An or-pattern's unreachable alternative: keep the other.
			a := m.arms[drop]
			a.alts = slices.Delete(a.alts, alt, alt+1)
			continue
		}
		m.arms = slices.Delete(m.arms, drop, drop+1)
		dropped++
		if len(m.arms) < 2 {
			return dropped, false
		}
	}
}

// treeProbes are the fixed probes: break and continue in arms inside a
// loop, inline in nested switches and shared, and an expression match in a
// loop whose shared arm does not terminate. treeProbeOut is their output.
const treeProbes = `type LClosed { Failures int64 }
type LOpen { Endpoint string }
type LState = LClosed | LOpen
type LFailure {}
type LRetry {}
type LSignal = LFailure | LRetry

func breakProbe(xs []LState, sig LSignal) int {
	let mut n = 0
	for x in xs {
		match (x, sig) {
		case (LOpen, LFailure):
			n = n + 1
			break
		case (LClosed{Failures: 3}, LRetry):
			n = n + 100
			continue
		case _:
			n = n + 10
		}
		n = n + 1000
	}
	n
}

func sharedProbe(xs []LState, sig LSignal) int {
	let mut n = 0
	for x in xs {
		match (x, sig) {
		case (LClosed{Failures: 3}, _), (_, LRetry):
			n = n + 100
			break
		case (LOpen, LFailure):
			n = n + 1
			continue
		case _:
			n = n + 10
		}
		n = n + 1000
	}
	n
}

func exprProbe(xs []LState, sig LSignal) string {
	let mut out = ""
	for x in xs {
		let v = match (x, sig) {
		case (LClosed{Failures: 1}, _), (LOpen, LRetry):
			out = out + "a"
			"A"
		case (LOpen, LFailure):
			continue
		case _:
			"B"
		}
		out = out + v
	}
	out
}

func probes() needs Console {
	Console.Println("probe|" + strconv.Itoa(breakProbe([]LState{LClosed{0}, LClosed{3}, LOpen{"x"}, LClosed{0}}, LFailure)))
	Console.Println("probe|" + strconv.Itoa(breakProbe([]LState{LClosed{3}, LClosed{0}}, LRetry)))
	Console.Println("probe|" + strconv.Itoa(sharedProbe([]LState{LClosed{1}, LOpen{"x"}, LClosed{3}, LClosed{0}}, LFailure)))
	Console.Println("probe|" + strconv.Itoa(sharedProbe([]LState{LClosed{0}, LClosed{0}}, LRetry)))
	Console.Println("probe|" + strconv.Itoa(sharedProbe([]LState{LOpen{"x"}, LClosed{0}}, LFailure)))
	Console.Println("probe|" + exprProbe([]LState{LClosed{1}, LOpen{"x"}, LClosed{0}}, LFailure))
	Console.Println("probe|" + exprProbe([]LState{LOpen{"x"}}, LRetry))
}
`

const treeProbeOut = "probe|2021\nprobe|1110\nprobe|1111\nprobe|100\nprobe|1011\nprobe|aAB\nprobe|aA\n"

// treeFallbackProbes is a match the tree gives up on, so the program also
// runs the fallback to the flat lowering: patho (match_tree_shape_test.go),
// whose tree passes the node bound, called on 24 seeded inputs (the first
// reaches its last arm); big, a match the bound must not stop; and ord and
// ordU, record patterns that name fields out of declaration order. It
// returns their source, with the calls in a function fallbacks, and the
// expected output. The other causes of a
// fallback (a type test of an interface type, a constant on an interface
// column, an arm no leaf reaches) are shapes the checker refuses or that
// Ergo cannot write; a union cannot hold a union (types.md, Unions 1).
func treeFallbackProbes(r *rand.Rand) (src, want string) {
	var b, w strings.Builder
	b.WriteString(treePathoFunc())
	// big is a match whose tree has about 80 nodes: over the floor 64 of
	// the node bound, under 4 × its 144 pattern nodes, so it keeps the tree
	// only when patternSize counts.
	b.WriteString(treeCommonTypes + "\nfunc big(a A, b B, c int) string {\n\tmatch (a, b, c) {\n")
	for i := range 6 {
		for j := range 6 {
			fmt.Fprintf(&b, "\tcase (A%d, B%d, %d):\n\t\t\"%d.%d\"\n", i, j, (i+j)%3, i, j)
		}
	}
	b.WriteString("\tcase _:\n\t\t\"_\"\n\t}\n}\n")
	// ord and ordU name a record's fields against its declaration order
	// across rows (F1, then F0), on a record and on a variant: the columns
	// keep the declaration order.
	b.WriteString(`
type ROrd { F0 int, F1 int }
type VOrd { F0 int, F1 int }
type WOrd {}
type UOrd = VOrd | WOrd

func ord(r ROrd) string {
	match r {
	case ROrd{F1: 0}:
		"a"
	case ROrd{F0: 0}:
		"b"
	case _:
		"c"
	}
}

func ordU(u UOrd) string {
	match u {
	case VOrd{F1: 0}:
		"a"
	case VOrd{F0: 0}:
		"b"
	case _:
		"c"
	}
}
`)
	b.WriteString("\nfunc fallbacks() needs Console {\n")
	b.WriteString("\tConsole.Println(\"ord|\" + ord(ROrd{0, 1}) + ord(ROrd{1, 0}) + ord(ROrd{1, 1}) + ord(ROrd{0, 0}))\n")
	b.WriteString("\tConsole.Println(\"ord|\" + ordU(VOrd{0, 1}) + ordU(VOrd{1, 0}) + ordU(VOrd{1, 1}) + ordU(VOrd{0, 0}) + ordU(WOrd))\n")
	w.WriteString("ord|baca\nord|bacac\n")
	for range 16 {
		i, j, k := r.IntN(6), r.IntN(6), r.IntN(3)
		c := k == (i+j)%3
		bv := fmt.Sprintf("B%d", j)
		if j == 2 {
			bv = `B2{"s"}`
		}
		av := fmt.Sprintf("A%d", i)
		if i == 2 {
			av = "A2{1}"
		}
		fmt.Fprintf(&b, "\tConsole.Println(\"big|\" + big(%s, %s, %d))\n", av, bv, k)
		if c {
			fmt.Fprintf(&w, "big|%d.%d\n", i, j)
		} else {
			w.WriteString("big|_\n")
		}
	}
	k := treePathoPairs
	for n := range 24 {
		xs := make([]bool, 2*k)
		var args []string
		for i := range xs {
			xs[i] = n > 0 && r.IntN(2) == 0 // the first call reaches the last arm
			args = append(args, strconv.FormatBool(xs[i]))
		}
		arm := k
		for i := range k {
			if xs[i] && xs[k+i] {
				arm = i
				break
			}
		}
		fmt.Fprintf(&b, "\tConsole.Println(\"patho|\" + patho(%s))\n", strings.Join(args, ", "))
		fmt.Fprintf(&w, "patho|arm%d\n", arm)
	}
	b.WriteString("}\n\n")
	return b.String(), w.String()
}

// loweredMode is a source lowered in one mode, with the test hook it ran
// with.
type loweredMode struct {
	*lowered
	hook *matchTreeTest
}

// lowerSourceMode is lowerSource with its entry, flat or with the tree.
func lowerSourceMode(name, src string, flat bool) loweredMode {
	out := &lowered{name: name, src: src}
	hook := &matchTreeTest{flat: flat}
	file, err := syntax.Parse(src)
	if err != nil {
		out.err = err
		return loweredMode{out, hook}
	}
	out.file = file
	out.pkg, out.diags = check.Check("main", []*syntax.File{file}, nil)
	if len(out.diags) > 0 {
		return loweredMode{out, hook}
	}
	func() {
		defer func() { out.panicked = recover() }()
		l := newLowerer(Unit{Package: out.pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
		l.matchTest = hook
		code, err := l.program()
		var refused *Refused
		switch {
		case errors.As(err, &refused):
			out.refused = refused.Refusals
		case err != nil:
			out.err = err
		default:
			out.files = []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: code}}
		}
	}()
	return loweredMode{out, hook}
}

func TestMatchTreeAgrees(t *testing.T) {
	if matchLowering == "flat" {
		t.Skip("matchLowering=flat: the build has no tree to compare")
	}
	seeds := treeSeeds
	if *treeSeed != 0 {
		seeds = []uint64{*treeSeed}
	}
	var src, want strings.Builder
	src.WriteString(treeShowPrelude)
	src.WriteString(treeProbes)
	want.WriteString(treeProbeOut)
	fbSrc, fbWant := treeFallbackProbes(rand.New(rand.NewPCG(seeds[0], 0x6662)))
	src.WriteString(fbSrc)
	want.WriteString(fbWant)
	var runs []string
	fns := map[string]string{} // match id to its function, for failures
	generated, dropped, matches, regenerated := 0, 0, 0, 0
	joinedArms := 0 // kept or-patterns that bind one name at two variants
	for _, seed := range seeds {
		g := &treeGen{r: rand.New(rand.NewPCG(seed, 0x6d31)), seed: seed}
		for gi := range treeGroups {
			gr := g.group(fmt.Sprintf("S%dG%d", seed, gi))
			decls := gr.decls()
			src.WriteString(decls)
			for mi := range treeMatches {
				id := fmt.Sprintf("s%dg%dm%d", seed, gi, mi)
				var m *gMatch
				for attempt := 0; ; attempt++ {
					if attempt == 50 {
						t.Fatalf("seed %d: no total match in 50 attempts for %s", seed, id)
					}
					m = g.match(gr, id)
					n := len(m.arms)
					generated += n
					d, total := treeFilter(t, decls, m)
					if total {
						dropped += d
						break
					}
					dropped += n
					regenerated++
				}
				matches++
				for _, a := range m.arms {
					if len(a.alts) == 2 && slices.ContainsFunc(a.alts[0], func(p *gPat) bool { return p.joined }) {
						joinedArms++
					}
				}
				fn := m.fn()
				fns[id] = fn
				src.WriteString(fn + "\n")
				run := "run" + id
				runs = append(runs, run)
				fmt.Fprintf(&src, "func %s() needs Console {\n", run)
				for _, vs := range g.values(m) {
					line, ok := m.model(vs)
					if !ok {
						t.Fatalf("seed %d: the checker reports %s total, but no arm matches %v\n%s", seed, id, vs, fn)
					}
					var args []string
					for _, v := range vs {
						args = append(args, v.ergo())
					}
					// The line names the values, so a failure shows its input.
					in := strings.Join(args, ", ")
					fmt.Fprintf(&src, "\tConsole.Println(%s + %s(%s))\n", strconv.Quote(id+"|"+in+" => "), id, in)
					want.WriteString(id + "|" + in + " => " + line + "\n")
				}
				src.WriteString("}\n\n")
			}
		}
	}
	src.WriteString("func main() needs Console {\n\tprobes()\n\tfallbacks()\n")
	for _, r := range runs {
		src.WriteString("\t" + r + "()\n")
	}
	src.WriteString("}\n")
	rate := float64(dropped) / float64(generated)
	t.Logf("seeds %v: %d matches (%d generated again: not total, or under 2 arms), %d of %d generated arms dropped by the checker (%.0f%%), %d lines", seeds, matches, regenerated, dropped, generated, 100*rate, strings.Count(want.String(), "\n"))
	// The drop rate is a property of the fixed set; one seed alone can
	// pass 50% (a replay with -match.seed checks the outputs only).
	if rate > 0.5 && *treeSeed == 0 {
		t.Errorf("drop rate %.0f%% is over 50%%: a generator defect", 100*rate)
	}
	t.Logf("or-patterns that bind one name at two variants: %d", joinedArms)
	if joinedArms == 0 && *treeSeed == 0 {
		t.Errorf("no kept or-pattern binds one name at two variants (their union)")
	}

	tree := lowerSourceMode("tree", src.String(), false)
	flat := lowerSourceMode("flat", src.String(), true)
	for _, o := range []*lowered{tree.lowered, flat.lowered} {
		if o.err != nil || len(o.diags) > 0 || o.panicked != nil || len(o.refused) > 0 {
			t.Fatalf("%s: err %v, diags %v, panic %v, refused %v", o.name, o.err, firstN(o.diags, 5), o.panicked, o.refused)
		}
	}
	// Every generated match takes the tree in the tree program, and none
	// in the flat one (a single-subject match of type tests takes the
	// type-switch fast path in both).
	treeFlat, flatFlat := treeFlatFuncs(t, tree.mainGo()), treeFlatFuncs(t, flat.mainGo())
	if len(treeFlat) > 0 {
		t.Errorf("the tree program lowers %d generated matches flat: %v", len(treeFlat), firstN(treeFlat, 5))
	}
	t.Logf("the flat program lowers %d of %d generated matches flat (the rest by the type-switch fast path)", len(flatFlat), matches)
	if len(flatFlat) == 0 {
		t.Errorf("flatMatch does not force the flat lowering")
	}
	// Every kind of column the tree splits on is reached: a type switch, a
	// value switch, and the expansion of a tuple and of a record.
	for _, k := range []string{"ctor", "const", "split tuple", "split record"} {
		if tree.hook.splits[k] == 0 {
			t.Errorf("no generated match makes the tree split a %s column (splits %v)", k, tree.hook.splits)
		}
	}
	t.Logf("tree splits: %v", tree.hook.splits)
	// The fallback probes make the tree give up (treeBail) and take the
	// flat lowering.
	// patho only: big keeps its tree, and so does every generated match.
	if tree.hook.fallbacks["bail"] != 1 || tree.hook.fallbacks["unreached"] != 0 {
		t.Errorf("the tree gives up on %v, not on patho alone", tree.hook.fallbacks)
	}
	t.Logf("tree fallbacks: %v", tree.hook.fallbacks)
	exes := buildAll(t, []*lowered{tree.lowered, flat.lowered}, true)
	if exes == nil {
		t.FailNow()
	}
	outs := map[string]string{}
	for _, o := range []*lowered{tree.lowered, flat.lowered} {
		stdout, stderr, code := runExe(t, exes[o.name])
		outs[o.name] = stdout
		if code != 0 {
			t.Errorf("%s exited %d: %s", o.name, code, firstLines(stderr, 20))
		}
	}
	for _, name := range []string{"tree", "flat"} {
		if outs[name] == want.String() {
			continue
		}
		got, exp := strings.Split(outs[name], "\n"), strings.Split(want.String(), "\n")
		for i := range max(len(got), len(exp)) {
			var gl, el string
			if i < len(got) {
				gl = got[i]
			}
			if i < len(exp) {
				el = exp[i]
			}
			if gl == el {
				continue
			}
			id, _, _ := strings.Cut(el, "|")
			seed := strings.TrimPrefix(strings.SplitN(id, "g", 2)[0], "s")
			t.Errorf("%s: line %d is %q, the model's %q.\nReplay: go test -run '^TestMatchTreeAgrees$' ./internal/sketch/lower -args -match.seed=%s\nThe match:\n%s\nIts lines (model | tree | flat):\n%s",
				name, i+1, gl, el, seed, fns[id], treeLinesOf(id, want.String(), outs["tree"], outs["flat"]))
			break
		}
	}
}

// treeLinesOf puts the model's, the tree's and the flat program's lines of
// the match id side by side.
func treeLinesOf(id string, outs ...string) string {
	var cols [][]string
	for _, o := range outs {
		var lines []string
		for _, l := range strings.Split(o, "\n") {
			if strings.HasPrefix(l, id+"|") {
				lines = append(lines, strings.TrimPrefix(l, id+"|"))
			}
		}
		cols = append(cols, lines)
	}
	var b strings.Builder
	for i := range cols[0] {
		row := make([]string, len(cols))
		for k, c := range cols {
			if i < len(c) {
				row[k] = c[i]
			}
		}
		mark := "  "
		if row[0] != row[1] || row[0] != row[2] {
			mark = "≠ "
		}
		b.WriteString(mark + strings.Join(row, " | ") + "\n")
	}
	return b.String()
}

// treeFlatFuncs lists the generated match functions (s…g…m…) of the
// emitted Go that hold an expressionless switch: the flat lowering.
func treeFlatFuncs(t *testing.T, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatalf("emitted Go does not parse: %v", err)
	}
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || !treeMatchFunc.MatchString(fd.Name.Name) {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			if s, ok := n.(*ast.SwitchStmt); ok && s.Tag == nil {
				out = append(out, fd.Name.Name)
				return false
			}
			return true
		})
	}
	return out
}

var treeMatchFunc = regexp.MustCompile(`^s\d+g\d+m\d+$`)

func firstN[T any](xs []T, n int) []T {
	return xs[:min(n, len(xs))]
}
