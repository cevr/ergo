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
// A seeded generator writes unions of 2 to 4 variants whose fields are
// int ({0, 1, 2}), string ({"a", "b"}), bool, or a union of the same group
// (one level, no recursion), and matches of 1 to 3 subjects with 2 to 8
// arms: wildcards, binds, type tests, `as`, records with 0 to 2 written
// fields, constants, tuples, and or-patterns of 2 alternatives that bind
// the same names, often from different columns. About half the matches end
// with `case _`; the others end with one arm per variant (or bool value)
// of their first subject. The checker filters them: an arm it reports
// unreachable (S108) is dropped, a match it reports not total (S103) is
// generated again. Each arm yields its index and its bindings as text, so
// the output shows the arm and where each binding came from. The arms
// yield to each destination: a function's tail, a let, an assignment in
// a statement match, and a return.
//
// A reference model in Go walks the arms in source order. One program
// holds every match and prints one line per (match, value); it is lowered
// twice, with the tree and flat (lowerer.flatMatch), built once, and both
// outputs must equal the model's. Fixed probes add a match in a loop whose
// arms break and continue (inline in nested switches and shared), and an
// expression match whose shared arm does not terminate.
//
// -match.seed=N runs the seed N alone; every failure names its seed.

var treeSeed = flag.Uint64("match.seed", 0, "run TestMatchTreeAgrees on this seed alone")

// treeSeeds are the fixed seeds; each writes treeGroups groups.
var treeSeeds = []uint64{1, 2, 3, 4, 5, 6}

const (
	treeGroups       = 3 // groups per seed
	treeMatches      = 3 // matches per group
	treeSampleValues = 160
)

type gKind int

const (
	gInt gKind = iota
	gString
	gBool
	gUnionKind
)

type gType struct {
	kind  gKind
	union *gUnion
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
	unions []*gUnion
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

func (g *treeGen) group(prefix string) *gGroup {
	gr := &gGroup{prefix: prefix}
	n := 1 + g.r.IntN(3)
	for u := n - 1; u >= 0; u-- {
		un := &gUnion{name: fmt.Sprintf("%sU%d", prefix, u)}
		for v := 0; v < 2+g.r.IntN(3); v++ {
			vr := &gVariant{name: fmt.Sprintf("%sV%d", un.name, v)}
			unionField := false
			for f := 0; f < g.r.IntN(3); f++ {
				switch k := g.r.IntN(4); {
				case k == 3 && u == 0 && n > 1 && !unionField:
					vr.fields = append(vr.fields, &gType{kind: gUnionKind, union: gr.unions[g.r.IntN(len(gr.unions))]})
					unionField = true
				case k == 0 || k == 3:
					vr.fields = append(vr.fields, gIntT)
				case k == 1:
					vr.fields = append(vr.fields, gStringT)
				default:
					vr.fields = append(vr.fields, gBoolT)
				}
			}
			un.variants = append(un.variants, vr)
		}
		// unions[0] is U(n-1), …; the first union written may hold the others
		gr.unions = append(gr.unions, un)
	}
	return gr
}

func (g *treeGen) subjectType(gr *gGroup) *gType {
	switch k := g.r.IntN(10); {
	case k < 8:
		return &gType{kind: gUnionKind, union: gr.unions[g.r.IntN(len(gr.unions))]}
	case k == 8:
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
		inner := g.record(t, v, d, names)
		if g.r.IntN(2) == 0 {
			inner = &gPat{kind: pTest, t: t, variant: v}
		}
		return &gPat{kind: pAs, t: t, inner: inner, name: bind()}
	}
	return g.record(t, v, d, names)
}

func (g *treeGen) record(t *gType, v, d int, names *[]string) *gPat {
	p := &gPat{kind: pRecord, t: t, variant: v, fields: map[int]*gPat{}}
	fs := t.union.variants[v].fields
	for _, f := range g.r.Perm(len(fs))[:min(len(fs), g.r.IntN(3))] {
		p.fields[f] = g.pat(fs[f], d+1, names)
	}
	if len(p.fields) == 0 {
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

func (g *treeGen) arm(m *gMatch) *gArm {
	a := g.arm1(m)
	if len(m.subjects) > 1 {
		return a
	}
	for _, alt := range a.alts {
		if alt[0].kind == pWild {
			// A lone `_` is the match's one case _ (the generator adds
			// it last, or not at all).
			return g.arm(m)
		}
	}
	return a
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
	switch k := g.r.IntN(3); {
	case k == 1:
		alts[0][i] = &gPat{kind: pBind, t: t, name: "b0"}
		alts[1][j] = &gPat{kind: pBind, t: t, name: "b0"}
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
	if p.kind == pAs {
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
	for k := len(gr.unions) - 1; k >= 0; k-- {
		un := gr.unions[k]
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

// lowerSourceMode is lowerSource with its entry, flat or with the tree.
func lowerSourceMode(name, src string, flat bool) *lowered {
	out := &lowered{name: name, src: src}
	file, err := syntax.Parse(src)
	if err != nil {
		out.err = err
		return out
	}
	out.file = file
	out.pkg, out.diags = check.Check("main", []*syntax.File{file}, nil)
	if len(out.diags) > 0 {
		return out
	}
	func() {
		defer func() { out.panicked = recover() }()
		l := newLowerer(Unit{Package: out.pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
		l.flatMatch = flat
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
	return out
}

func TestMatchTreeAgrees(t *testing.T) {
	seeds := treeSeeds
	if *treeSeed != 0 {
		seeds = []uint64{*treeSeed}
	}
	var src, want strings.Builder
	src.WriteString(treeShowPrelude)
	src.WriteString(treeProbes)
	want.WriteString(treeProbeOut)
	var runs []string
	fns := map[string]string{} // match id to its function, for failures
	generated, dropped, matches, regenerated := 0, 0, 0, 0
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
					fmt.Fprintf(&src, "\tConsole.Println(%q + %s(%s))\n", id+"|", id, strings.Join(args, ", "))
					want.WriteString(id + "|" + line + "\n")
				}
				src.WriteString("}\n\n")
			}
		}
	}
	src.WriteString("func main() needs Console {\n\tprobes()\n")
	for _, r := range runs {
		src.WriteString("\t" + r + "()\n")
	}
	src.WriteString("}\n")
	rate := float64(dropped) / float64(generated)
	t.Logf("seeds %v: %d matches (%d generated again: not total, or under 2 arms), %d of %d generated arms dropped by the checker (%.0f%%), %d lines", seeds, matches, regenerated, dropped, generated, 100*rate, strings.Count(want.String(), "\n"))
	if rate > 0.5 {
		t.Errorf("drop rate %.0f%% is over 50%%: a generator defect", 100*rate)
	}

	tree := lowerSourceMode("tree", src.String(), false)
	flat := lowerSourceMode("flat", src.String(), true)
	for _, o := range []*lowered{tree, flat} {
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
	exes := buildAll(t, []*lowered{tree, flat}, true)
	if exes == nil {
		t.FailNow()
	}
	for _, o := range []*lowered{tree, flat} {
		stdout, stderr, code := runExe(t, exes[o.name])
		if code != 0 {
			t.Errorf("%s exited %d: %s", o.name, code, firstLines(stderr, 20))
			continue
		}
		if stdout == want.String() {
			continue
		}
		got, exp := strings.Split(stdout, "\n"), strings.Split(want.String(), "\n")
		for i := range max(len(got), len(exp)) {
			var gl, el string
			if i < len(got) {
				gl = got[i]
			}
			if i < len(exp) {
				el = exp[i]
			}
			if gl != el {
				id, _, _ := strings.Cut(el, "|")
				t.Errorf("%s: line %d is %q, the model's %q (seeds %v; the match:\n%s)", o.name, i+1, gl, el, seeds, fns[id])
				break
			}
		}
	}
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
