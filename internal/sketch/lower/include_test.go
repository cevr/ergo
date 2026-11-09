package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// incRec is one generated record: own fields and `..R` members in order.
// A generic record has one type parameter T and an own field of type T.
// A variant is declared in place in the union U.
type incRec struct {
	name    string
	generic bool
	variant bool
	members []incMember
}

// incMember is an own field (name and type: int, string or T) or an
// include of the record inc with the type argument arg.
type incMember struct {
	field, typ string
	inc        int
	arg        string
	line       int // the member's line in the source
}

// incFlat is one field of a record after inclusion, in the reference
// flattening: its name, its type with T replaced, and its declaration.
type incFlat struct{ name, typ, origin string }

// incDiag is a diagnostic the reference expects: at a line, containing a
// text.
type incDiag struct {
	line int
	text string
}

// incGraph is a generated program of records that include each other.
type incGraph struct {
	prefix string
	defs   []int // records a defined type is declared over, before them
	recs   []incRec
	src    string
}

// genIncGraph makes a graph of 2 to 6 records; each includes up to two
// earlier records (a DAG of depth up to 3 and more, with diamonds), some
// are generic and are included as ..R[int] or ..R[T], and the last ones
// may be variants of a union. With ring, every record includes the next
// and the last includes the first: an inclusion cycle.
func genIncGraph(rng *rand.Rand, prefix string, ring bool) *incGraph {
	g := &incGraph{prefix: prefix}
	n := 2 + rng.Intn(5)
	for i := range n {
		r := incRec{name: fmt.Sprintf("%sR%d", prefix, i)}
		r.generic = !ring && rng.Intn(4) == 0
		r.variant = !ring && !r.generic && i >= n-2 && rng.Intn(2) == 0
		var ms []incMember
		for j := range 1 + rng.Intn(2) {
			typ := []string{"int", "string"}[rng.Intn(2)]
			if r.generic && j == 0 {
				typ = "T"
			}
			ms = append(ms, incMember{field: fmt.Sprintf("%sF%d_%d", prefix, i, j), typ: typ})
		}
		if ring {
			ms = append(ms, incMember{inc: (i + 1) % n})
		} else {
			for range rng.Intn(3) {
				if i == 0 {
					break
				}
				j := rng.Intn(i)
				if g.recs[j].variant {
					continue
				}
				arg := ""
				if g.recs[j].generic {
					arg = []string{"int", "string"}[rng.Intn(2)]
					if r.generic && rng.Intn(2) == 0 {
						arg = "T"
					}
				}
				ms = append(ms, incMember{inc: j, arg: arg})
			}
		}
		rng.Shuffle(len(ms), func(a, b int) { ms[a], ms[b] = ms[b], ms[a] })
		r.members = ms
		g.recs = append(g.recs, r)
	}
	if !ring {
		// Most often over the last generic record: an instance's body is
		// a copy (the case expandIncludes must take again).
		generic := -1
		for i, r := range g.recs {
			if r.generic {
				generic = i
			}
		}
		for range rng.Intn(3) {
			i := rng.Intn(n)
			if generic >= 0 && rng.Intn(3) > 0 {
				i = generic
			}
			if !g.recs[i].variant {
				g.defs = append(g.defs, i)
			}
		}
	}
	g.write()
	return g
}

// recType is record i as a type: a generic one instantiated with int.
func (g *incGraph) recType(i int) string {
	if g.recs[i].generic {
		return g.recs[i].name + "[int]"
	}
	return g.recs[i].name
}

// member writes one member's source.
func (g *incGraph) member(m incMember) string {
	if m.field != "" {
		return m.field + " " + m.typ
	}
	t := g.recs[m.inc].name
	if m.arg != "" {
		t += "[" + m.arg + "]"
	}
	return ".." + t
}

// write writes the declarations, one member per line, and records each
// member's line.
func (g *incGraph) write() {
	var b strings.Builder
	line := 1
	emit := func(s string) {
		b.WriteString(s)
		line += strings.Count(s, "\n")
	}
	// A defined type resolves its base first, before the base's
	// includes are spliced in (include.go, expandIncludes).
	for k, i := range g.defs {
		emit(fmt.Sprintf("type %sD%d %s\n", g.prefix, k, g.recType(i)))
	}
	var variants []int
	for i := range g.recs {
		r := &g.recs[i]
		if r.variant {
			variants = append(variants, i)
			continue
		}
		head := "type " + r.name
		if r.generic {
			head += "[T]"
		}
		emit(head + " {\n")
		for k := range r.members {
			r.members[k].line = line
			emit("\t" + g.member(r.members[k]) + ",\n")
		}
		emit("}\n")
	}
	if len(variants) > 0 {
		emit("type " + g.prefix + "U =\n")
		for _, i := range variants {
			r := &g.recs[i]
			emit("\t| " + r.name + "{\n")
			for k := range r.members {
				r.members[k].line = line
				emit("\t\t" + g.member(r.members[k]) + ",\n")
			}
			emit("\t}\n")
		}
	}
	g.src = b.String()
}

// flatten is the reference flattening of record i with T as arg, and the
// diagnostics it expects: a name that two members give is S108 at the
// later member; a member that reaches a record being flattened is an
// inclusion cycle at that member. Records are flattened in declaration
// order, each once, as the checker does.
func (g *incGraph) flatten() (map[int][]incFlat, []incDiag) {
	done := map[int][]incFlat{}
	open := map[int]bool{}
	var diags []incDiag
	var flat func(i int) []incFlat
	flat = func(i int) []incFlat {
		if f, ok := done[i]; ok {
			return f
		}
		open[i] = true
		var out []incFlat
		seen := map[string]bool{}
		for _, m := range g.recs[i].members {
			if m.field != "" {
				seen[m.field] = true
			}
		}
		for _, m := range g.recs[i].members {
			if m.field != "" {
				out = append(out, incFlat{m.field, m.typ, m.field})
				continue
			}
			if open[m.inc] {
				diags = append(diags, incDiag{m.line, "includes itself"})
				continue
			}
			for _, f := range flat(m.inc) {
				if seen[f.name] {
					diags = append(diags, incDiag{m.line, "gives the field " + f.name + ","})
					continue
				}
				seen[f.name] = true
				if f.typ == "T" {
					f.typ = m.arg
				}
				out = append(out, f)
			}
		}
		open[i] = false
		done[i] = out
		return out
	}
	for i := range g.recs {
		flat(i)
	}
	return done, diags
}

// value is the source of a value of typ for the k-th field: distinct per
// field, so a field read in the wrong place prints another value.
func incValue(typ string, k int) string {
	if typ == "int" {
		return strconv.Itoa(100 + k)
	}
	return fmt.Sprintf("%q", "s"+strconv.Itoa(k))
}

// use writes, for each record, statements that build it with a keyed and
// with a positional literal, read every field, bind every field with a
// record pattern and compare the two literals; and the lines they print.
func (g *incGraph) use(flats map[int][]incFlat) (string, string) {
	var src, out strings.Builder
	for i, r := range g.recs {
		fs := flats[i]
		typ := r.name
		if r.generic {
			typ += "[int]"
		}
		var keyed, pos, reads, want []string
		for k, f := range fs {
			ft := f.typ
			if ft == "T" {
				ft = "int"
			}
			v := incValue(ft, k)
			keyed = append(keyed, f.name+": "+v)
			pos = append(pos, v)
			reads = append(reads, "\\(k"+r.name+"."+f.name+")")
			want = append(want, strings.Trim(v, `"`))
		}
		fmt.Fprintf(&src, "\tlet k%s = %s{%s}\n", r.name, typ, strings.Join(keyed, ", "))
		fmt.Fprintf(&src, "\tlet p%s = %s{%s}\n", r.name, typ, strings.Join(pos, ", "))
		fmt.Fprintf(&src, "\tConsole.Println(\"%s \\(k%s == p%s) %s\")\n", r.name, r.name, r.name, strings.Join(reads, " "))
		fmt.Fprintf(&out, "%s true %s\n", r.name, strings.Join(want, " "))
		for k, f := range fs {
			fmt.Fprintf(&src, "\tlet %s{%s: b%s_%d} = k%s\n", typ, f.name, r.name, k, r.name)
			fmt.Fprintf(&src, "\tConsole.Println(\"\\(b%s_%d)\")\n", r.name, k)
			fmt.Fprintf(&out, "%s\n", want[k])
		}
	}
	for k, i := range g.defs {
		name := fmt.Sprintf("%sD%d", g.prefix, k)
		var keyed, reads, want []string
		for j, f := range flats[i] {
			ft := f.typ
			if ft == "T" {
				ft = "int"
			}
			v := incValue(ft, j)
			keyed = append(keyed, f.name+": "+v)
			reads = append(reads, "\\(d"+name+"."+f.name+")")
			want = append(want, strings.Trim(v, `"`))
		}
		fmt.Fprintf(&src, "\tlet d%s = %s{%s}\n", name, name, strings.Join(keyed, ", "))
		fmt.Fprintf(&src, "\tConsole.Println(\"%s %s\")\n", name, strings.Join(reads, " "))
		fmt.Fprintf(&out, "%s %s\n", name, strings.Join(want, " "))
	}
	return src.String(), out.String()
}

// TestIncludedRecordFlattens is the property of types.md, Structs 7 (owner
// q43, sp-include): for generated include graphs (DAGs with diamonds,
// generic records included as ..R[int] and ..R[T], variants declared in
// place, defined types over a record declared before it, and rings), the checker's verdicts equal a reference flattening's:
// a duplicate name (a diamond among them) is S108 at the later member, a
// ring is one S108 "includes itself" at one of its members, and a graph
// with neither checks with each record's fields equal to the reference's
// flat list, in order, each keeping its declaration (Field.Origin). The
// valid graphs then run as one program: a keyed and a positional literal of
// every record, a read and a record pattern of every field, == and a keyed
// literal of each defined type print
// the values the reference gives. The run is a Go build because the
// lowering writes each Go struct from the checked flat field list
// (decls.go): only the built program shows a field the Go struct lost or
// misplaced. ERGO_SEED=<n> replays one seed.
func TestIncludedRecordFlattens(t *testing.T) {
	needsBuild(t)
	seeds := make([]int64, 40)
	for i := range seeds {
		seeds[i] = int64(i + 1)
	}
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = []int64{n}
	}
	var decls, body, want strings.Builder
	valid := 0
	for _, seed := range seeds {
		rng := rand.New(rand.NewSource(seed))
		ring := rng.Intn(5) == 0
		g := genIncGraph(rng, fmt.Sprintf("S%d", seed), ring)
		flats, diags := g.flatten()
		pkg, ds := check.CheckSource(g.src)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Errorf("seed %d (replay: ERGO_SEED=%d): %s\n%s", seed, seed, fmt.Sprintf(format, args...), g.src)
		}
		if ring {
			if len(ds) != 1 || !strings.Contains(ds[0].Message, "includes itself") {
				fail("a ring: want one S108 \"includes itself\", got %v", ds)
				continue
			}
			ok := false
			for _, r := range g.recs {
				for _, m := range r.members {
					ok = ok || m.field == "" && m.line == ds[0].Line
				}
			}
			if !ok {
				fail("the cycle is reported at line %d, not at a member of the ring", ds[0].Line)
			}
			continue
		}
		if len(ds) != len(diags) {
			fail("want %d diagnostics %v, got %v", len(diags), diags, ds)
			continue
		}
		// Each expected diagnostic matches one S108 at its line; the order
		// within a line is the checker's.
		used := make([]bool, len(ds))
		for _, want := range diags {
			found := false
			for k, d := range ds {
				if !used[k] && d.Line == want.line && d.Code == check.CodeType && strings.Contains(d.Message, want.text) {
					used[k], found = true, true
					break
				}
			}
			if !found {
				fail("want an S108 at line %d with %q, got %v", want.line, want.text, ds)
			}
		}
		if len(diags) > 0 {
			continue
		}
		bodies := map[string]*check.Struct{}
		own := map[string]*check.Field{} // a declaring field by its name
		for _, tn := range pkg.Types {
			if n, isNamed := tn.Type.(*check.Named); isNamed {
				s, _ := n.Underlying().(*check.Struct)
				if s == nil {
					continue
				}
				bodies[tn.Name] = s
				for _, f := range s.Fields {
					if f.Origin() == f {
						own[f.Name] = f
					}
				}
			}
		}
		for i, r := range g.recs {
			s := bodies[r.name]
			if s == nil || len(s.Fields) != len(flats[i]) {
				fail("%s: want %d fields %v, got %v", r.name, len(flats[i]), flats[i], s)
				continue
			}
			for k, f := range s.Fields {
				ref := flats[i][k]
				if f.Name != ref.name || own[ref.origin] == nil || f.Origin() != own[ref.origin] {
					fail("%s field %d: want %s (declared as %s), got %s (declared as %s)", r.name, k, ref.name, ref.origin, f.Name, f.Origin().Name)
				}
			}
		}
		valid++
		decls.WriteString(g.src)
		s, w := g.use(flats)
		body.WriteString(s)
		want.WriteString(w)
	}
	if t.Failed() {
		return
	}
	if valid == 0 {
		t.Fatal("no valid graph to run")
	}
	t.Logf("%d of %d graphs check and run", valid, len(seeds))
	wantRun(t, decls.String()+"\nfunc main() needs Console {\n"+body.String()+"}\n", want.String())
}
