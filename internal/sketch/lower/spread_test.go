package lower

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// spField is one field of a record after inclusion, in the reference
// model: its name, its type and its declaration ("Rec.Name").
type spField struct{ name, typ, origin string }

// spType is a record type a spread can name or take from: its source
// text, its flat fields, the name of its show function and, for a
// variant, the union it belongs to.
type spType struct {
	text   string
	fields []spField
	show   string
	union  string
}

// spPool is the field names a generated record draws from, each with one
// type, so that two records often have a field of one name from two
// declarations.
var spPool = []spField{{name: "A", typ: "int"}, {name: "B", typ: "string"}, {name: "C", typ: "int"}, {name: "Tags", typ: "[]int"}, {name: "D", typ: "string"}, {name: "E", typ: "int"}}

// spSpec is one generated package: records that include records (to depth
// two), plain records, two variants of a union declared in place, and the
// generic Box[T] with two instances.
type spSpec struct {
	prefix string
	src    strings.Builder
	types  []*spType // every non-generic record type
	boxes  []*spType // Box[int], Box[string]
	unions map[string]string
}

func (sp *spSpec) has(fields []spField, name string) bool {
	return slices.ContainsFunc(fields, func(f spField) bool { return f.name == name })
}

// record generates the members of one record: up to maxInc includes of
// earlier included records with disjoint names, then own fields from the
// pool, in a shuffled order. It returns the member source and the flat
// fields.
func (sp *spSpec) record(rng *rand.Rand, name string, incs []*spType, maxInc int) (string, []spField) {
	type member struct {
		src    string
		fields []spField
	}
	var members []member
	var flat []spField
	for _, i := range rng.Perm(len(incs)) {
		if len(members) >= maxInc || rng.Intn(2) == 0 {
			continue
		}
		inc := incs[i]
		if slices.ContainsFunc(inc.fields, func(f spField) bool { return sp.has(flat, f.name) }) {
			continue
		}
		members = append(members, member{".." + inc.text, inc.fields})
		flat = append(flat, inc.fields...)
	}
	own := rng.Intn(3)
	if len(flat) == 0 && own == 0 {
		own = 1
	}
	for _, k := range rng.Perm(len(spPool)) {
		if own == 0 {
			break
		}
		f := spPool[k]
		if sp.has(flat, f.name) {
			continue
		}
		f.origin = name + "." + f.name
		members = append(members, member{f.name + " " + f.typ, []spField{f}})
		flat = append(flat, f)
		own--
	}
	rng.Shuffle(len(members), func(i, j int) { members[i], members[j] = members[j], members[i] })
	var srcs []string
	flat = nil
	for _, m := range members {
		srcs = append(srcs, m.src)
		flat = append(flat, m.fields...)
	}
	return strings.Join(srcs, ", "), flat
}

func genSpread(rng *rand.Rand, prefix string) *spSpec {
	sp := &spSpec{prefix: prefix, unions: map[string]string{}}
	var incs []*spType
	for i := range 2 + rng.Intn(2) {
		name := fmt.Sprintf("%sI%d", prefix, i)
		members, flat := sp.record(rng, name, incs, 1)
		fmt.Fprintf(&sp.src, "type %s { %s }\n", name, members)
		incs = append(incs, &spType{text: name, fields: flat})
	}
	sp.types = append(sp.types, incs...)
	for i := range 2 + rng.Intn(3) {
		name := fmt.Sprintf("%sR%d", prefix, i)
		members, flat := sp.record(rng, name, incs, 2)
		fmt.Fprintf(&sp.src, "type %s { %s }\n", name, members)
		sp.types = append(sp.types, &spType{text: name, fields: flat})
	}
	union := prefix + "U"
	var variants []string
	for i := range 2 {
		name := fmt.Sprintf("%sV%d", prefix, i)
		members, flat := sp.record(rng, name, incs, 2)
		variants = append(variants, name+"{"+members+"}")
		sp.types = append(sp.types, &spType{text: name, fields: flat, union: union})
	}
	fmt.Fprintf(&sp.src, "type %s = %s\n", union, strings.Join(variants, " | "))
	fmt.Fprintf(&sp.src, "type %sBox[T] { V T, N int }\n", prefix)
	for _, arg := range []string{"int", "string"} {
		sp.boxes = append(sp.boxes, &spType{
			text:   prefix + "Box[" + arg + "]",
			fields: []spField{{"V", arg, "Box.V"}, {"N", "int", "Box.N"}},
		})
	}
	for i, t := range append(slices.Clone(sp.types), sp.boxes...) {
		t.show = fmt.Sprintf("%sshow%d", prefix, i)
		var parts []string
		for _, f := range t.fields {
			if f.typ == "[]int" {
				parts = append(parts, `\(spsh(x.`+f.name+`))`)
			} else {
				parts = append(parts, `\(x.`+f.name+`)`)
			}
		}
		fmt.Fprintf(&sp.src, "func %s(x %s) string { \"%s\" }\n", t.show, t.text, strings.Join(parts, " "))
	}
	return sp
}

// spGlobals are the helpers every generated program shares: the text of a
// two-element slice, and a value that prints its tag first (the effect log).
const spGlobals = `func spsh(xs []int) string { "\(xs[0]),\(xs[1])" }

func ti(tag string, v int) int needs Console {
	Console.Println(tag)
	v
}

func ts(tag string, v string) string needs Console {
	Console.Println(tag)
	v
}

func tl(tag string, v []int) []int needs Console {
	Console.Println(tag)
	v
}
`

// spValue is a field value's source and its text in a show line: distinct
// per case, field and role (base or named), so a field read from the wrong
// place prints another value.
func spValue(typ string, i, k int, named bool) (src, shown string, tags [2]int) {
	n := 10000*i + 100*k
	if named {
		n += 50
	}
	switch typ {
	case "int":
		return strconv.Itoa(n), strconv.Itoa(n), tags
	case "string":
		s := fmt.Sprintf("v%d", n)
		return strconv.Quote(s), s, tags
	}
	tags = [2]int{n, n + 1}
	return fmt.Sprintf("[]int{%d, %d}", n, n+1), fmt.Sprintf("%d,%d", n, n+1), tags
}

// spCase is one generated spread T{..s, named} and the model's verdict.
type spCase struct {
	t, s  *spType
	union bool     // s is typed as its union
	named []int    // indexes of T's fields, in source order
	diags []string // the texts the model expects, in T's field order
}

// verdict is the reference model of types.md, Structs 3 (owner q43 = A):
// with a base of exactly T, every unnamed field is the base's and one
// field must be named; with another record type, each unnamed field of T
// is copied from the base's field of the same declaration and type, and
// is an S108 naming the field otherwise; a union value is not a record.
func (c *spCase) verdict() {
	if c.union {
		c.diags = []string{"union type"}
		return
	}
	if c.t == c.s {
		if len(c.named) == 0 {
			c.diags = []string{"the copy is s itself"}
		}
		return
	}
	for k, f := range c.t.fields {
		if slices.Contains(c.named, k) {
			continue
		}
		i := slices.IndexFunc(c.s.fields, func(g spField) bool { return g.name == f.name })
		switch {
		case i < 0:
			c.diags = append(c.diags, "has no "+f.name+";")
		case c.s.fields[i].origin != f.origin:
			c.diags = append(c.diags, "does not copy "+f.name+": "+"\x00declared apart")
		case c.s.fields[i].typ != f.typ:
			c.diags = append(c.diags, "does not copy "+f.name+": "+"\x00's "+f.name+" is ")
		}
	}
}

// literal is a full keyed literal of t, with case i's base values.
func spLiteral(t *spType, i int) string {
	var parts []string
	for k, f := range t.fields {
		src, _, _ := spValue(f.typ, i, k, false)
		parts = append(parts, f.name+": "+src)
	}
	return t.text + "{" + strings.Join(parts, ", ") + "}"
}

// TestSpreadMatchesModel is the property of types.md, Structs 3 for a
// spread (owner q43 = A, sp-spread). For generated records that include
// records (to depth two), variants declared in place, and the generic Box
// at two type arguments, and generated spreads T{..s, named} between any
// two of them (the same type, another instance, a sibling variant, an
// unrelated record, a projection to an included record, an injection from
// one, a base typed as a union): the checker's verdict equals the model's,
// one S108 per field it cannot copy, with its reason. Every
// spread that checks then runs in one Go-built program, against the
// hand-desugared meaning: the base evaluated once and first (a call that
// prints its tag), then the named values in source order (each prints its
// tag), every named field the value written and every other field the
// base's. A carried slice field is a value of its own: written through
// the result after the spread, and through a base that stays alive, each
// write is seen through its own record only. ERGO_SEED=<n> replays one
// seed.
func TestSpreadMatchesModel(t *testing.T) {
	needsBuild(t)
	seeds := make([]int64, 30)
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
	decls.WriteString(spGlobals)
	valid, refused, carried := 0, 0, 0
	for _, seed := range seeds {
		rng := rand.New(rand.NewSource(seed))
		sp := genSpread(rng, fmt.Sprintf("P%d", seed))
		if _, ds := check.CheckSource(spGlobals + sp.src.String() + "func main() {}\n"); len(ds) != 0 {
			t.Fatalf("seed %d: the declarations do not check: %v\n%s", seed, ds, sp.src.String())
		}
		decls.WriteString(sp.src.String())
		for n := range 12 {
			i := int(seed)*100 + n
			c := &spCase{}
			if rng.Intn(5) == 0 {
				c.t, c.s = sp.boxes[rng.Intn(2)], sp.boxes[rng.Intn(2)]
			} else {
				c.t, c.s = sp.types[rng.Intn(len(sp.types))], sp.types[rng.Intn(len(sp.types))]
				c.union = c.s.union != "" && rng.Intn(4) == 0
			}
			for _, k := range rng.Perm(len(c.t.fields)) {
				if rng.Intn(3) == 0 {
					c.named = append(c.named, k)
				}
			}
			c.verdict()
			if len(c.diags) > 0 {
				refused++
				spRefused(t, seed, sp, c, i)
				continue
			}
			valid++
			if spRun(rng, c, i, &decls, &body, &want) {
				carried++
			}
		}
	}
	if t.Failed() {
		return
	}
	if valid == 0 || refused == 0 || carried == 0 {
		t.Fatalf("%d spreads check, %d are refused and %d copy a slice from a base alive after: the generator must make each", valid, refused, carried)
	}
	t.Logf("%d spreads run (%d copy a slice from a base alive after), %d refused", valid, carried, refused)
	wantRun(t, decls.String()+"\nfunc main() needs Console {\n"+body.String()+"}\n", want.String())
}

// TestSpreadBaseBeforeValues: the base of a spread is read before the
// named values run, also when it is a variable that a named value
// reassigns through a function literal (the lowering may read a variable
// base in place only when no named value runs code first).
func TestSpreadBaseBeforeValues(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Pp { A int }

type P { ..Pp, B int }

type Q { ..Pp, C int }

func main() needs Console {
	let mut p = P{A: 1, B: 2}
	let bump = func() int {
		p = P{A: 9, B: 9}
		5
	}
	let q = Q{..p, C: bump()}
	Console.Println("\(q.A) \(q.C) \(p.A)")
}
`, "1 5 9\n")
}

// spRefused checks case c alone and compares its diagnostics with the
// model's: each an S108 on the spread's line.
func spRefused(t *testing.T, seed int64, sp *spSpec, c *spCase, i int) {
	t.Helper()
	var named []string
	for _, k := range c.named {
		src, _, _ := spValue(c.t.fields[k].typ, i, k, true)
		named = append(named, ", "+c.t.fields[k].name+": "+src)
	}
	decl := "let s = "
	if c.union {
		decl = "let s " + c.s.union + " = "
	}
	head := spGlobals + sp.src.String()
	src := head + "func main() {\n\t" + decl + spLiteral(c.s, i) + "\n\tlet r = " + c.t.text + "{..s" + strings.Join(named, "") + "}\n\t_ = r\n}\n"
	line := strings.Count(head, "\n") + 3
	_, ds := check.CheckSource(src)
	// The checker sorts diagnostics at one position by their text, so each
	// expected text matches one diagnostic, in any order.
	ok := len(ds) == len(c.diags)
	used := make([]bool, len(ds))
	for k := 0; ok && k < len(c.diags); k++ {
		first, second, _ := strings.Cut(c.diags[k], "\x00")
		ok = false
		for j, d := range ds {
			if !used[j] && d.Code == check.CodeType && d.Line == line && strings.Contains(d.Message, first) && strings.Contains(d.Message, second) {
				used[j], ok = true, true
				break
			}
		}
	}
	if !ok {
		t.Errorf("seed %d (replay: ERGO_SEED=%d): %s{..s %v} from %s: want S108s at line %d with %q, got %v\n%s", seed, seed, c.t.text, c.named, c.s.text, line, c.diags, ds, src)
	}
}

// spRun writes case c's statements and the lines the model says they
// print. It reports whether the result copies a slice field from a base
// that is written and read after the spread.
func spRun(rng *rand.Rand, c *spCase, i int, decls, body, want *strings.Builder) bool {
	local := rng.Intn(2) == 0
	alive := local && rng.Intn(2) == 0 // the base is read after the spread
	// The base is a let mut written after the spread, or a plain let.
	sTags := slices.IndexFunc(c.s.fields, func(f spField) bool { return f.typ == "[]int" })
	write := alive && sTags >= 0 && rng.Intn(2) == 0
	tTags := slices.IndexFunc(c.t.fields, func(f spField) bool { return f.typ == "[]int" })
	let := func(mut bool) string {
		if mut {
			return "let mut "
		}
		return "let "
	}
	base := fmt.Sprintf("s%d", i)
	if local {
		fmt.Fprintf(body, "\t%s%s = %s\n", let(write), base, spLiteral(c.s, i))
	} else {
		// The base is a call that prints its tag: evaluated once, first.
		fmt.Fprintf(decls, "func base%d() %s needs Console {\n\tConsole.Println(\"b%d\")\n\t%s\n}\n", i, c.s.text, i, spLiteral(c.s, i))
		base = fmt.Sprintf("base%d()", i)
		fmt.Fprintf(want, "b%d\n", i)
	}
	// The result's fields: each named value, or the base's field of the
	// same name (the model's verdict made it the same declaration).
	shown := make([]string, len(c.t.fields))
	tags := make([][2]int, len(c.t.fields))
	for k, f := range c.t.fields {
		j := slices.IndexFunc(c.s.fields, func(g spField) bool { return g.name == f.name })
		if j >= 0 {
			_, shown[k], tags[k] = spValue(f.typ, i, j, false)
		}
	}
	var named []string
	for _, k := range c.named {
		f := c.t.fields[k]
		src, s, tg := spValue(f.typ, i, k, true)
		shown[k], tags[k] = s, tg
		tag := fmt.Sprintf("n%d%s", i, f.name)
		fn := map[string]string{"int": "ti", "string": "ts", "[]int": "tl"}[f.typ]
		named = append(named, fmt.Sprintf(", %s: %s(%q, %s)", f.name, fn, tag, src))
		fmt.Fprintf(want, "%s\n", tag)
	}
	r := fmt.Sprintf("r%d", i)
	fmt.Fprintf(body, "\t%s%s = %s{..%s%s}\n", let(tTags >= 0), r, c.t.text, base, strings.Join(named, ""))
	// A carried slice is a value of its own: a write through either
	// record is not seen through the other.
	if tTags >= 0 {
		fmt.Fprintf(body, "\t%s.%s[0] = %d\n", r, c.t.fields[tTags].name, 900+i)
		tags[tTags][0] = 900 + i
		shown[tTags] = fmt.Sprintf("%d,%d", tags[tTags][0], tags[tTags][1])
	}
	line := strings.Join(shown, " ")
	if alive {
		baseShown := make([]string, len(c.s.fields))
		for j, f := range c.s.fields {
			_, baseShown[j], _ = spValue(f.typ, i, j, false)
		}
		if write {
			fmt.Fprintf(body, "\t%s.%s[1] = %d\n", base, c.s.fields[sTags].name, 800+i)
			_, _, tg := spValue("[]int", i, sTags, false)
			baseShown[sTags] = fmt.Sprintf("%d,%d", tg[0], 800+i)
		}
		fmt.Fprintf(body, "\tConsole.Println(%s(%s) + \" | \" + %s(%s))\n", c.t.show, r, c.s.show, base)
		line += " | " + strings.Join(baseShown, " ")
	} else {
		fmt.Fprintf(body, "\tConsole.Println(%s(%s))\n", c.t.show, r)
	}
	fmt.Fprintf(want, "%s\n", line)
	return alive && tTags >= 0 && sTags >= 0 && !slices.Contains(c.named, tTags)
}
