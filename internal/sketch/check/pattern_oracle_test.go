package check_test

import (
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestPatternOracle (spec D-B4): the checker's totality and reachability
// verdicts on generated matches agree with brute-force enumeration of the
// subjects' values. Each trial declares a few unions whose variants are
// records of bool, int and later unions (so patterns nest), and matches one
// to three subjects with random cases: wildcards, bindings, bool and integer
// constants, bare type tests and keyed record patterns, tuples for two
// subjects, tuple values and defined tuples, non-union records, as patterns,
// cases of several patterns, and sometimes a final `case _`. An
// int ranges over 0, 1, 2 and "any other value", so constants never cover
// it. The oracle expects S108 at exactly the patterns the patterns before
// them cover, a final `case _` included (D-B4: a dead default is S108), S103 at
// the match exactly when some value matches no pattern, and nothing else.

type oKind int

const (
	oBool oKind = iota
	oInt
	oSum
	oTuple
)

type oType struct {
	kind  oKind
	u     *oUnion
	elems []oType
	def   string // a defined type over a tuple
}

type oUnion struct {
	name     string
	variants []oVariant
	record   bool // one non-union record shape
}

type oVariant struct {
	name   string
	fields []oField
}

type oField struct {
	name string
	t    oType
}

// oVal is an abstract value: a bool's tag is 0 (false) or 1 (true); an
// int's 0, 1, 2 or 3 (any other value); a union's the variant index, with
// the variant's field values.
type oVal struct {
	tag  int
	kids []oVal
}

type oPatKind int

const (
	pWild oPatKind = iota
	pBind
	pConst
	pType
	pRecord
	pTuple
	pAs
)

type oPat struct {
	kind   oPatKind
	tag    int
	name   string
	fields map[int]oPat // a record pattern's keyed fields
	elems  []oPat
	inner  *oPat
}

func (t oType) String() string {
	switch t.kind {
	case oBool:
		return "bool"
	case oInt:
		return "int"
	case oTuple:
		if t.def != "" {
			return t.def
		}
		var parts []string
		for _, e := range t.elems {
			parts = append(parts, e.String())
		}
		return "(" + strings.Join(parts, ", ") + ")"
	}
	return t.u.name
}

func oValues(t oType) []oVal {
	switch t.kind {
	case oBool:
		return []oVal{{tag: 0}, {tag: 1}}
	case oInt:
		return []oVal{{tag: 0}, {tag: 1}, {tag: 2}, {tag: 3}}
	case oTuple:
		prods := [][]oVal{nil}
		for _, e := range t.elems {
			var next [][]oVal
			for _, p := range prods {
				for _, v := range oValues(e) {
					next = append(next, append(append([]oVal(nil), p...), v))
				}
			}
			prods = next
		}
		var out []oVal
		for _, p := range prods {
			out = append(out, oVal{kids: p})
		}
		return out
	}
	var out []oVal
	for vi, v := range t.u.variants {
		prods := [][]oVal{nil}
		for _, f := range v.fields {
			var next [][]oVal
			for _, p := range prods {
				for _, fv := range oValues(f.t) {
					next = append(next, append(append([]oVal(nil), p...), fv))
				}
			}
			prods = next
		}
		for _, p := range prods {
			out = append(out, oVal{tag: vi, kids: p})
		}
	}
	return out
}

// Bound the value space before enumerating it. The representative "other
// integer" is distinct from every generated constant (0, 1 and 2).
func oCount(t oType) int {
	capCount := func(n int) int { return min(n, 4001) }
	switch t.kind {
	case oBool:
		return 2
	case oInt:
		return 4
	case oTuple:
		n := 1
		for _, e := range t.elems {
			n = capCount(n * oCount(e))
		}
		return n
	}
	n := 0
	for _, v := range t.u.variants {
		p := 1
		for _, f := range v.fields {
			p = capCount(p * oCount(f.t))
		}
		n = capCount(n + p)
	}
	return n
}

func oMatches(p oPat, v oVal) bool {
	switch p.kind {
	case pWild, pBind:
		return true
	case pConst, pType:
		return v.tag == p.tag
	case pAs:
		return oMatches(*p.inner, v)
	case pTuple:
		for i, e := range p.elems {
			if !oMatches(e, v.kids[i]) {
				return false
			}
		}
		return true
	}
	if v.tag != p.tag {
		return false
	}
	for f, fp := range p.fields {
		if !oMatches(fp, v.kids[f]) {
			return false
		}
	}
	return true
}

type oGen struct {
	rng   *rand.Rand
	names int
	binds bool // bindings allowed (cases of one pattern)
}

func (g *oGen) pattern(t oType, depth int) oPat {
	k := g.rng.Intn(12)
	switch {
	case k < 2:
		return oPat{kind: pWild}
	case k < 3 && g.binds:
		g.names++
		return oPat{kind: pBind, name: fmt.Sprintf("x%d", g.names)}
	case k < 4 && g.binds:
		inner := g.pattern(t, depth+1)
		g.names++
		return oPat{kind: pAs, name: fmt.Sprintf("x%d", g.names), inner: &inner}
	}
	switch t.kind {
	case oBool:
		return oPat{kind: pConst, tag: g.rng.Intn(2)}
	case oInt:
		return oPat{kind: pConst, tag: g.rng.Intn(3)}
	case oTuple:
		p := oPat{kind: pTuple}
		for _, e := range t.elems {
			p.elems = append(p.elems, g.pattern(e, depth+1))
		}
		return p
	}
	vi := g.rng.Intn(len(t.u.variants))
	v := t.u.variants[vi]
	if len(v.fields) == 0 || depth > 2 || g.rng.Intn(3) == 0 {
		return oPat{kind: pType, tag: vi}
	}
	p := oPat{kind: pRecord, tag: vi, fields: map[int]oPat{}}
	for f := range v.fields {
		if len(p.fields) == 0 && f == len(v.fields)-1 || g.rng.Intn(2) == 0 {
			p.fields[f] = g.pattern(v.fields[f].t, depth+1)
		}
	}
	return p
}

func (p oPat) text(t oType) string {
	switch p.kind {
	case pWild:
		return "_"
	case pBind:
		return p.name
	case pAs:
		return "(" + p.inner.text(t) + " as " + p.name + ")"
	case pTuple:
		var parts []string
		for i, e := range p.elems {
			parts = append(parts, e.text(t.elems[i]))
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case pConst:
		if t.kind == oBool {
			return []string{"false", "true"}[p.tag]
		}
		return fmt.Sprint(p.tag)
	case pType:
		return t.u.variants[p.tag].name
	}
	v := t.u.variants[p.tag]
	var keys []int
	for f := range p.fields {
		keys = append(keys, f)
	}
	sort.Ints(keys)
	var parts []string
	for _, f := range keys {
		parts = append(parts, v.fields[f].name+": "+p.fields[f].text(v.fields[f].t))
	}
	return v.name + "{" + strings.Join(parts, ", ") + "}"
}

func (p oPat) bindings(out *[]string) {
	if p.kind == pBind || p.kind == pAs {
		*out = append(*out, p.name)
	}
	if p.inner != nil {
		p.inner.bindings(out)
	}
	for _, e := range p.elems {
		e.bindings(out)
	}
	for _, f := range p.fields {
		f.bindings(out)
	}
}

// oUnions declares 1 to 3 unions or records and defined tuples. Fields may
// name an earlier declaration, so nesting stays finite.
func oUnions(rng *rand.Rand) ([]*oUnion, []oType) {
	n := 1 + rng.Intn(3)
	us := make([]*oUnion, n)
	var defs []oType
	pool := []oType{{kind: oBool}, {kind: oInt}}
	leaf := func() oType { return pool[rng.Intn(len(pool))] }
	for i := n - 1; i >= 0; i-- {
		u := &oUnion{name: fmt.Sprintf("U%d", i)}
		variants := 2 + rng.Intn(2)
		if rng.Intn(3) == 0 {
			u.name, u.record, variants = fmt.Sprintf("R%d", i), true, 1
		}
		for v := range variants {
			vr := oVariant{name: fmt.Sprintf("V%d%c", i, 'a'+v)}
			if u.record {
				vr.name = u.name
			}
			for f := 0; f < rng.Intn(3); f++ {
				ft := leaf()
				if rng.Intn(4) == 0 {
					ft = oType{kind: oTuple, def: fmt.Sprintf("D%d", len(defs))}
					for range 2 + rng.Intn(2) {
						ft.elems = append(ft.elems, leaf())
					}
					defs = append(defs, ft)
				}
				vr.fields = append(vr.fields, oField{name: fmt.Sprintf("F%d", f), t: ft})
			}
			u.variants = append(u.variants, vr)
		}
		us[i] = u
		pool = append(pool, oType{kind: oSum, u: u})
	}
	return us, defs
}

func oDecls(us []*oUnion, defs []oType) string {
	var b strings.Builder
	for _, d := range defs {
		base := d
		base.def = ""
		fmt.Fprintf(&b, "type %s %s\n\n", d.def, base)
	}
	for _, u := range us {
		var vs []string
		for _, v := range u.variants {
			var fs []string
			for _, f := range v.fields {
				fs = append(fs, f.name+" "+f.t.String())
			}
			vs = append(vs, v.name+"{"+strings.Join(fs, ", ")+"}")
		}
		if u.record {
			fmt.Fprintf(&b, "type %s\n\n", vs[0])
		} else {
			fmt.Fprintf(&b, "type %s = %s\n\n", u.name, strings.Join(vs, " | "))
		}
	}
	return b.String()
}

func TestPatternOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(1964))
	verdicts := map[string]int{}
	for trial := 0; trial < 6000; trial++ {
		us, defs := oUnions(rng)
		pool := []oType{{kind: oBool}, {kind: oInt}}
		for _, u := range us {
			pool = append(pool, oType{kind: oSum, u: u}, oType{kind: oSum, u: u})
		}
		pool = append(pool, defs...)
		pool = append(pool, oType{kind: oTuple, elems: []oType{pool[rng.Intn(len(pool))], pool[rng.Intn(len(pool))]}})
		subj := []oType{pool[rng.Intn(len(pool))]}
		for k := rng.Intn(3); k > 0; k-- {
			subj = append(subj, pool[rng.Intn(len(pool))])
		}
		count := 1
		for _, s := range subj {
			count *= oCount(s)
			if count > 4000 {
				break
			}
		}
		if count > 4000 {
			continue
		}
		// The value vectors of the subjects.
		vecs := [][]oVal{nil}
		for _, s := range subj {
			var next [][]oVal
			for _, v := range vecs {
				for _, sv := range oValues(s) {
					next = append(next, append(append([]oVal(nil), v...), sv))
				}
			}
			vecs = next
		}
		if len(vecs) > 4000 {
			continue
		}
		g := &oGen{rng: rng}
		type item struct {
			pats []oPat
			line int
		}
		var src strings.Builder
		src.WriteString(oDecls(us, defs))
		var params, names []string
		for i, s := range subj {
			params = append(params, fmt.Sprintf("s%d %s", i, s))
			names = append(names, fmt.Sprintf("s%d", i))
		}
		head := names[0]
		if len(names) > 1 {
			head = "(" + strings.Join(names, ", ") + ")"
		}
		fmt.Fprintf(&src, "func f(%s) {\n", strings.Join(params, ", "))
		line := strings.Count(src.String(), "\n") + 1
		matchLine := line
		fmt.Fprintf(&src, "\tmatch %s {\n", head)
		line++
		var items []item
		for range 1 + rng.Intn(6) {
			several := rng.Intn(5) == 0
			g.binds = !several
			count := 1
			if several {
				count = 2
			}
			var texts []string
			var binds []string
			for range count {
				it := item{line: line}
				var parts []string
				for _, s := range subj {
					p := g.pattern(s, 0)
					for len(subj) == 1 && p.kind == pWild {
						p = g.pattern(s, 0) // `case _` alone is the default case
					}
					it.pats = append(it.pats, p)
					parts = append(parts, p.text(s))
					p.bindings(&binds)
				}
				text := parts[0]
				if len(parts) > 1 {
					text = "(" + strings.Join(parts, ", ") + ")"
				}
				texts = append(texts, text)
				items = append(items, it)
			}
			fmt.Fprintf(&src, "\tcase %s:\n", strings.Join(texts, ", "))
			line++
			sort.Strings(binds)
			for _, b := range binds {
				fmt.Fprintf(&src, "\t\t_ = %s\n", b)
				line++
			}
		}
		withDefault := rng.Intn(5) == 0
		if withDefault {
			src.WriteString("\tcase _:\n")
		}
		src.WriteString("\t}\n}\n")
		// The oracle.
		matched := make([]bool, len(vecs))
		want := map[string]bool{}
		for _, it := range items {
			useful := false
			for vi, v := range vecs {
				all := true
				for j := range subj {
					if !oMatches(it.pats[j], v[j]) {
						all = false
						break
					}
				}
				if all && !matched[vi] {
					useful = true
					matched[vi] = true
				}
			}
			if !useful {
				want[fmt.Sprintf("S108:%d", it.line)] = true
			}
		}
		covered := true
		for _, m := range matched {
			covered = covered && m
		}
		if withDefault && covered {
			want[fmt.Sprintf("S108:%d", line)] = true // a dead case _
		}
		total := withDefault
		if !total {
			total = true
			for _, m := range matched {
				total = total && m
			}
		}
		if !total {
			want[fmt.Sprintf("S103:%d", matchLine)] = true
		}
		_, diags := check.CheckSource(src.String())
		got := map[string]bool{}
		for _, d := range diags {
			got[fmt.Sprintf("%s:%d", d.Code, d.Line)] = true
			if d.Code == "S103" && strings.Contains(d.Message, "too many cases") {
				t.Fatalf("trial %d: a small match reached the budget:\n%s", trial, src.String())
			}
			if text, ok := strings.CutPrefix(d.Message, "this match is not total: no case handles "); ok {
				text, _, _ = strings.Cut(text, "; name every variant")
				oCheckWitness(t, text, subj, vecs, matched)
			}
		}
		if fmt.Sprint(sortedKeys(got)) != fmt.Sprint(sortedKeys(want)) {
			t.Fatalf("trial %d: got %v, the oracle says %v\n%s\n%s", trial, sortedKeys(got), sortedKeys(want), describe(diags), src.String())
		}
		switch {
		case !total:
			verdicts["not total"]++
		case len(want) > 0:
			verdicts["total with unreachable"]++
		default:
			verdicts["clean"]++
			if gaps := check.InfoGaps(src.String()); len(gaps) > 0 {
				t.Fatalf("trial %d: accepted program has Info gaps: %v\n%s", trial, gaps, src.String())
			}
		}
	}
	t.Logf("verdicts: %v", verdicts)
	for _, v := range []string{"not total", "total with unreachable", "clean"} {
		if verdicts[v] < 50 {
			t.Errorf("only %d trials gave %q; the generator lost its spread", verdicts[v], v)
		}
	}
}

// Read the reported witness as a pattern and check it against the oracle's
// values. The printer may leave fields unconstrained, so at least one
// represented value must be a value the cases miss. No solver fact is used.
func oCheckWitness(t *testing.T, text string, subjects []oType, values [][]oVal, covered []bool) {
	t.Helper()
	text = strings.ReplaceAll(text, "main.", "")
	f, err := syntax.Parse("func f() { match x { case " + text + ": } }\n")
	if err != nil {
		t.Fatalf("witness %q is not a pattern: %v", text, err)
	}
	var pattern syntax.Expr
	syntax.Inspect(f, func(n syntax.Node) bool {
		if cl, ok := n.(*syntax.CaseClause); ok && len(cl.List) > 0 {
			pattern = cl.List[0]
		}
		return true
	})
	for i, vector := range values {
		if covered[i] {
			continue
		}
		if len(subjects) == 1 && oWitnessMatches(pattern, subjects[0], vector[0]) || len(subjects) > 1 && oWitnessMatches(pattern, oType{kind: oTuple, elems: subjects}, oVal{kids: vector}) {
			return
		}
	}
	t.Fatalf("witness %q represents no value the cases miss", text)
}

func oWitnessMatches(e syntax.Expr, typ oType, v oVal) bool {
	switch x := e.(type) {
	case *syntax.ParenExpr:
		return oWitnessMatches(x.X, typ, v)
	case *syntax.Ident:
		switch x.Name {
		case "_":
			return true
		case "true", "false":
			return typ.kind == oBool && (x.Name == "true") == (v.tag == 1)
		}
		return typ.kind == oSum && x.Name == typ.u.variants[v.tag].name
	case *syntax.BasicLit:
		n, err := strconv.Atoi(x.Value)
		return err == nil && typ.kind == oInt && n == v.tag
	case *syntax.TupleExpr:
		if typ.kind != oTuple || len(x.Elts) != len(v.kids) {
			return false
		}
		for i, p := range x.Elts {
			if !oWitnessMatches(p, typ.elems[i], v.kids[i]) {
				return false
			}
		}
		return true
	case *syntax.CompositeLit:
		name, ok := x.Type.(*syntax.Ident)
		if !ok || typ.kind != oSum || name.Name != typ.u.variants[v.tag].name {
			return false
		}
		fields := typ.u.variants[v.tag].fields
		for _, p := range x.Elts {
			kv, ok := p.(*syntax.KeyValueExpr)
			if !ok {
				return false
			}
			id, ok := kv.Key.(*syntax.Ident)
			if !ok {
				return false
			}
			found := false
			for i, f := range fields {
				if id.Name == f.name {
					found = oWitnessMatches(kv.Value, f.t, v.kids[i])
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestMatchArmsLinear (D16): a match over a union of n variants, one case
// per variant, nested one level through a bool field, checks in counted
// work linear in n: the solvers' steps and the bytes the check allocates
// grow about 4x when n does (stage E's scale shape "match").
func testUnionArmsLinear(t *testing.T) {
	src := func(n int) string {
		var b strings.Builder
		var vs []string
		for i := range n {
			vs = append(vs, fmt.Sprintf("V%d{On bool}", i))
		}
		fmt.Fprintf(&b, "type U = %s\n\nfunc f(u U) int {\n\tmatch u {\n", strings.Join(vs, " | "))
		for i := range n {
			fmt.Fprintf(&b, "\tcase V%d{On: true}:\n\t\treturn %d\n\tcase V%d{On: false}:\n\t\treturn 0\n", i, i, i)
		}
		b.WriteString("\t}\n}\n")
		return b.String()
	}
	sizes := check.GrowthSizes(t, check.GrowthMaxNodes, func(n int) int { return check.SourceNodes(t, src(n)) })
	small, big := sizes[0], sizes[len(sizes)-1]
	steps := map[int]int{}
	var per []float64
	for _, n := range sizes {
		ts, us, d := check.SolverSteps(src(n))
		if len(d) > 0 || len(ts) != 1 {
			t.Fatalf("n=%d: diagnostics %s", n, describe(d))
		}
		steps[n] = ts[0] + us[0]
		per = append(per, float64(steps[n])/float64(n))
	}
	check.GrowthBounded(t, "solver steps per variant", sizes, per)
	bytes := func(s string) uint64 {
		best := uint64(0)
		for range 3 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			check.CheckSource(s)
			runtime.ReadMemStats(&after)
			if b := after.TotalAlloc - before.TotalAlloc; best == 0 || b < best {
				best = b
			}
		}
		return best
	}
	smallBytes, largeBytes := bytes(src(small)), bytes(src(big))
	ratio := float64(largeBytes) / float64(smallBytes)
	t.Logf("steps %v; allocated bytes: n=%d %d, n=%d %d, ratio %.2f", steps, small, smallBytes, big, largeBytes, ratio)
	if ratio > 1.5*float64(big)/float64(small) {
		t.Errorf("%.1fx the cases allocate %.2fx the bytes (%d vs %d); the check is superlinear", float64(big)/float64(small), ratio, largeBytes, smallBytes)
	}
}
