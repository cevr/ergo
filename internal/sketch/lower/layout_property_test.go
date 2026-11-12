package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
)

// The seeded property of pointer-first layouts (layout.go; NOSCAN1). Each
// seed declares records R0…Rk-1, each with two to six fields drawn from
// int, int8, int32, bool, string, []int and an earlier record (nested by
// value). main builds a value of each with a keyed literal whose keys come
// in a random order, prints each scalar field, encodes the value with
// std json (a codec walks the declared fields), updates one field with a
// spread literal R{..v, F: x} and encodes the update, and compares the two
// with == where the record is comparable. The program runs under the new
// lowering and under the reference (layoutLowering "declared"); stdout
// must equal the model's, a Go fold of the same values, under both.
//
// The layout report is held to a second model, written here apart from
// layout.go: the scanned prefix of each record in pointer-first order
// (pointer-holding fields first, then the rest by alignment) on amd64. The
// new lowering's report must give it and reorder some record; the
// reference's must keep every record's declared prefix.
//
// A failing seed prints the Ergo source, both outputs and its replay
// command.
//
// Mutants it kills (testdata/mutants.json):
// layout-codec-reads-reordered-field, layout-scalars-first,
// layout-string-not-a-pointer.

var layoutSeed = flag.Uint64("layout.seed", 0, "run TestLayoutsAgree on this seed alone")

// layoutPaths are the generator's branches; the fixed seeds reach each.
var layoutPaths = []string{"int", "int8", "int32", "bool", "string", "slice", "nested", "equal", "reordered"}

// lfield is one generated field: its Ergo type and the model's layout.
type lfield struct {
	name, typ string
	rec       int // the nested record, or -1
}

type lrecord struct {
	fields     []lfield
	comparable bool
}

// layoutGen writes one program and its model.
type layoutGen struct {
	r       *rand.Rand
	recs    []lrecord
	reached map[string]bool
}

// mlayout is the model's layout of a type: size, alignment and the end of
// its last pointer word.
type mlayout struct{ size, align, ptrEnd int }

func (g *layoutGen) typeLayout(f lfield) mlayout {
	switch f.typ {
	case "int":
		return mlayout{8, 8, 0}
	case "int8", "bool":
		return mlayout{1, 1, 0}
	case "int32":
		return mlayout{4, 4, 0}
	case "string":
		return mlayout{16, 8, 8}
	case "[]int":
		return mlayout{24, 8, 8}
	}
	return g.recLayout(f.rec, true)
}

// recLayout is a record's layout in pointer-first order, or in declared
// order.
func (g *layoutGen) recLayout(i int, ptrFirst bool) mlayout {
	fs := append([]lfield{}, g.recs[i].fields...)
	if ptrFirst {
		sort.SliceStable(fs, func(a, b int) bool {
			la, lb := g.typeLayout(fs[a]), g.typeLayout(fs[b])
			if (la.ptrEnd > 0) != (lb.ptrEnd > 0) {
				return la.ptrEnd > 0
			}
			return la.ptrEnd == 0 && la.align > lb.align
		})
	}
	off, out := 0, mlayout{align: 1}
	for _, f := range fs {
		l := g.typeLayout(f)
		off = (off + l.align - 1) / l.align * l.align
		if l.ptrEnd > 0 {
			out.ptrEnd = off + l.ptrEnd
		}
		off += l.size
		out.align = max(out.align, l.align)
	}
	out.size = (off + out.align - 1) / out.align * out.align
	return out
}

// value is a random value of f's type: its Ergo literal, its json text and
// its printed text ("" for a field main does not print).
func (g *layoutGen) value(f lfield) (lit, js, text string) {
	n := g.r.IntN(200) - 60
	switch f.typ {
	case "int":
		return fmt.Sprint(n), fmt.Sprint(n), fmt.Sprint(n)
	case "int8":
		n = n % 100
		return fmt.Sprint(n), fmt.Sprint(n), fmt.Sprint(n)
	case "int32":
		n *= 1000
		return fmt.Sprint(n), fmt.Sprint(n), fmt.Sprint(n)
	case "bool":
		b := fmt.Sprint(n%2 == 0)
		return b, b, b
	case "string":
		s := string(rune('a'+(n+60)%26)) + fmt.Sprint(n+60)
		return fmt.Sprintf("%q", s), fmt.Sprintf("%q", s), s
	case "[]int":
		xs := make([]string, g.r.IntN(3))
		for i := range xs {
			xs[i] = fmt.Sprint(g.r.IntN(9))
		}
		return "[]int{" + strings.Join(xs, ", ") + "}", "[" + strings.Join(xs, ",") + "]", fmt.Sprint(len(xs))
	}
	lit, js = g.recValue(f.rec)
	return lit, js, ""
}

// recValue is a random value of record i: a keyed literal with its keys
// in a random order, and its json text in declared order.
func (g *layoutGen) recValue(i int) (lit, js string) {
	rec := g.recs[i]
	lits := make([]string, len(rec.fields))
	jss := make([]string, len(rec.fields))
	for k, f := range rec.fields {
		l, j, _ := g.value(f)
		lits[k] = f.name + ": " + l
		jss[k] = fmt.Sprintf("%q:%s", jsonKey(f.name), j)
	}
	g.r.Shuffle(len(lits), func(a, b int) { lits[a], lits[b] = lits[b], lits[a] })
	return fmt.Sprintf("R%d{%s}", i, strings.Join(lits, ", ")), "{" + strings.Join(jss, ",") + "}"
}

var layoutTypes = []string{"int", "int8", "int32", "bool", "string", "[]int"}

// layoutProgram is the program of seed, the model's stdout and the
// model's scanned prefix of each record.
func layoutProgram(seed uint64, reached map[string]bool) (src, want string, scanned []int, g *layoutGen) {
	g = &layoutGen{r: rand.New(rand.NewPCG(seed, 0x4c41594f)), reached: reached}
	var b, out strings.Builder
	b.WriteString("import json\n\n")
	k := 2 + g.r.IntN(3)
	for i := 0; i < k; i++ {
		rec := lrecord{comparable: true}
		m := 2 + g.r.IntN(5)
		var decl []string
		for j := 0; j < m; j++ {
			f := lfield{name: fmt.Sprintf("F%d", j), rec: -1}
			if i > 0 && g.r.IntN(5) == 0 {
				f.rec, f.typ = g.r.IntN(i), ""
				f.typ = fmt.Sprintf("R%d", f.rec)
				rec.comparable = rec.comparable && g.recs[f.rec].comparable
				reached["nested"] = true
			} else {
				f.typ = layoutTypes[g.r.IntN(len(layoutTypes))]
				if f.typ == "[]int" {
					rec.comparable = false
					reached["slice"] = true
				} else {
					reached[f.typ] = true
				}
			}
			rec.fields = append(rec.fields, f)
			decl = append(decl, f.name+" "+f.typ)
		}
		g.recs = append(g.recs, rec)
		fmt.Fprintf(&b, "type R%d { %s }\n\n", i, strings.Join(decl, ", "))
		fmt.Fprintf(&b, "func codec%d() json.Codec[R%d] { json.Codec[R%d]{MaxBytes: 0, MaxDepth: 0} }\n\n", i, i, i)
		scanned = append(scanned, g.recLayout(i, true).ptrEnd)
	}
	b.WriteString("func main() needs Console {\n")
	for i, rec := range g.recs {
		lit, js := g.recValue(i)
		fmt.Fprintf(&b, "\tlet v%d = %s\n", i, lit)
		// The printed fields are the literal's: read them back from the
		// json text's order, which is the declared one.
		vals := g.fieldTexts(i, js)
		for k, f := range rec.fields {
			if vals[k] == "" {
				continue
			}
			if f.typ == "[]int" {
				fmt.Fprintf(&b, "\tConsole.Println(\"\\(len(v%d.%s))\")\n", i, f.name)
			} else {
				fmt.Fprintf(&b, "\tConsole.Println(\"\\(v%d.%s)\")\n", i, f.name)
			}
			out.WriteString(vals[k] + "\n")
		}
		fmt.Fprintf(&b, "\tConsole.Println(json.Encode(codec%d(), v%d))\n", i, i)
		out.WriteString(js + "\n")
		// The update: one field replaced.
		k := g.r.IntN(len(rec.fields))
		f := rec.fields[k]
		l, j, _ := g.value(f)
		fmt.Fprintf(&b, "\tlet u%d = R%d{..v%d, %s: %s}\n", i, i, i, f.name, l)
		fmt.Fprintf(&b, "\tConsole.Println(json.Encode(codec%d(), u%d))\n", i, i)
		ujs := replaceField(js, rec, k, j, g)
		out.WriteString(ujs + "\n")
		if rec.comparable {
			reached["equal"] = true
			fmt.Fprintf(&b, "\tConsole.Println(\"\\(v%d == u%d) \\(u%d == u%d)\")\n", i, i, i, i)
			fmt.Fprintf(&out, "%v true\n", ujs == js)
		}
	}
	b.WriteString("}\n")
	return b.String(), out.String(), scanned, g
}

// fieldTexts splits record i's json text into its fields' printed texts,
// in declared order ("" for a nested record).
func (g *layoutGen) fieldTexts(i int, js string) []string {
	parts := splitTop(js[1 : len(js)-1])
	out := make([]string, len(parts))
	for k, p := range parts {
		v := p[strings.Index(p, ":")+1:]
		switch g.recs[i].fields[k].typ {
		case "string":
			out[k] = strings.Trim(v, "\"")
		case "[]int":
			out[k] = fmt.Sprint(len(splitTop(v[1 : len(v)-1])))
		case "int", "int8", "int32", "bool":
			out[k] = v
		}
	}
	return out
}

// replaceField is the json text js of a record value with its k-th field's
// value replaced by j.
func replaceField(js string, rec lrecord, k int, j string, _ *layoutGen) string {
	parts := splitTop(js[1 : len(js)-1])
	parts[k] = fmt.Sprintf("%q:%s", jsonKey(rec.fields[k].name), j)
	return "{" + strings.Join(parts, ",") + "}"
}

// jsonKey is std json's key of a field: its name with a lower first letter.
func jsonKey(name string) string { return strings.ToLower(name[:1]) + name[1:] }

// splitTop splits s at its top-level commas (none inside [], {} or "").
func splitTop(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	depth, inStr, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// lowerLayoutMode lowers src under the new lowering, or under the
// reference (ref: layoutLowering "declared"), and returns its report.
func lowerLayoutMode(t *testing.T, name, src string, ref bool) (*lowered, []LayoutRow) {
	t.Helper()
	saved, savedHook := layoutLowering, statsHook
	defer func() { layoutLowering, statsHook = saved, savedHook }()
	layoutLowering = ""
	if ref {
		layoutLowering = "declared"
	}
	var stats knownStats
	statsHook = func(s knownStats) { stats = s }
	return mustLower(t, name, src), stats.Layouts
}

// TestLayoutsAgree is the seeded property (see above).
//
// It builds Go because the question is what the program computes with its
// fields in the new order: go/types says only that the reordered structs
// compile, not that a codec, a spread literal and == read the same values.
func TestLayoutsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *layoutSeed != 0 {
		seeds = []uint64{*layoutSeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want, scanned, _ := layoutProgram(seed, reached)
		runs = append(runs, run{seed, src, want})
		replay := fmt.Sprintf("Replay: go test -run '^TestLayoutsAgree$' ./internal/sketch/lower -args -layout.seed=%d", seed)
		p, rows := lowerLayoutMode(t, fmt.Sprintf("layout%d", seed), src, false)
		pr, refRows := lowerLayoutMode(t, fmt.Sprintf("layout%dref", seed), src, true)
		programs = append(programs, p, pr)
		byName := func(rows []LayoutRow) map[string]LayoutRow {
			m := map[string]LayoutRow{}
			for _, r := range rows {
				m[r.Type] = r
			}
			return m
		}
		got, ref := byName(rows), byName(refRows)
		for i, want := range scanned {
			name := fmt.Sprintf("R%d", i)
			if got[name].Scanned != want {
				t.Errorf("seed %d: %s is scanned for %d bytes in the new layout, the model says %d (report %+v)\n%s\n%s", seed, name, got[name].Scanned, want, got[name], replay, src)
			}
			if got[name].Reordered {
				reached["reordered"] = true
			}
			if r := ref[name]; r.Reordered || r.Scanned != r.ScannedDeclared {
				t.Errorf("seed %d: the reference reorders %s: %+v\n%s", seed, name, r, replay)
			}
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the layout programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		name := fmt.Sprintf("layout%d", r.seed)
		out, stderr, code := runExe(t, exes[name])
		refOut, refErr, refCode := runExe(t, exes[name+"ref"])
		if out != r.want || refOut != r.want || code != 0 || refCode != 0 {
			t.Errorf("seed %d: the layouts and the model differ\nReplay: go test -run '^TestLayoutsAgree$' ./internal/sketch/lower -args -layout.seed=%d\n%s\n--- pointer-first (exit %d)\n%s%s\n--- declared (exit %d)\n%s%s\n--- model\n%s", r.seed, r.seed, r.src, code, out, stderr, refCode, refOut, refErr, r.want)
		}
	}
	if *layoutSeed != 0 {
		return
	}
	for _, path := range layoutPaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q: %v", path, reached)
		}
	}
}
