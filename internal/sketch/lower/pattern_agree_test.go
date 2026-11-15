package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/regexpplan"
)

var patternSeed = flag.Uint64("pattern.seed", 0, "run TestPatternLoweringAgrees on this seed alone")

// patternSeeds are the seeds TestPatternLoweringAgrees runs by default.
var patternSeeds = []uint64{1, 2}

// A generated pattern is a record of elements (plain atoms and named
// captures) or a union of named captures. A capture is required, optional
// or repeated (Capture shapes 2), and a capture of the record may hold
// inner captures of its own (a nested record, Capture shapes 5), each
// required, optional or repeated in its scope, under a parent that is
// itself required, optional or repeated. No repeat holds two captures of
// one scope (Capture shapes 4), and every repeated body consumes a byte.
type patAtom struct{ set, quant string }

type patElem struct {
	atom   *patAtom // a plain atom; else a capture
	name   string
	suffix string // a capture's repeat: "", "?", "*" or "+"
	body   []patElem
}

type genPat struct {
	typ   string
	union bool
	elems []patElem // the variants, for a union
}

func (a patAtom) text() string {
	if len(a.set) == 1 {
		return a.set + a.quant
	}
	return "[" + a.set + "]" + a.quant
}

func (e patElem) text() string {
	if e.atom != nil {
		return e.atom.text()
	}
	var b strings.Builder
	for _, c := range e.body {
		b.WriteString(c.text())
	}
	return "(?<" + e.name + ">" + b.String() + ")" + e.suffix
}

// nested reports a capture with captures inside it: a record field.
func (e patElem) nested() bool {
	for _, c := range e.body {
		if c.atom == nil {
			return true
		}
	}
	return false
}

func (p *genPat) text() string {
	var parts []string
	for _, e := range p.elems {
		parts = append(parts, e.text())
	}
	if p.union {
		return strings.Join(parts, "|")
	}
	return strings.Join(parts, "")
}

var patSets = []string{"a", "b", "0", "ab", "a0", "abc", "01"}

var captureSuffixes = []string{"", "?", "*", "+"}

// patGen draws a pattern. In separated mode each atom takes the next of
// patSingles, so adjacent atoms never share a byte and most plans have a
// deterministic candidate (direct and table); otherwise atoms draw from
// overlapping sets, and most plans need the NFA.
type patGen struct {
	r         *rand.Rand
	separated bool
	next      int
}

var patSingles = []string{"a", "b", "0", "c", "1"}

func (g *patGen) atom(nullable bool) *patAtom {
	quants := []string{"", "+", "{1,2}"}
	if nullable {
		quants = append(quants, "?", "*")
	}
	set := patSets[g.r.IntN(len(patSets))]
	if g.separated {
		set = patSingles[g.next%len(patSingles)]
		g.next++
	}
	return &patAtom{set, quants[g.r.IntN(len(quants))]}
}

// body is a capture's body: a first atom that consumes a byte, then up to
// two atoms or, when inner is set, inner captures with flat bodies.
func (g *patGen) body(name string, inner bool) []patElem {
	body := []patElem{{atom: g.atom(false)}}
	for k := range g.r.IntN(3) {
		if inner && g.r.IntN(3) > 0 {
			body = append(body, patElem{name: fmt.Sprintf("%sk%d", name, k), suffix: captureSuffixes[g.r.IntN(4)], body: g.body("", false)})
		} else {
			body = append(body, patElem{atom: g.atom(false)})
		}
	}
	if g.separated && inner {
		// A byte after the inner captures closes the record at one place.
		body = append(body, patElem{atom: &patAtom{set: g.atom(false).set}})
	}
	return body
}

// terminal is the byte that ends a separated pattern: an optional or
// repeated element at the end leaves the construction check two
// acceptance histories (regexpplan, validate.go), and the plan the NFA.
func (g *patGen) terminal(p *genPat) {
	if g.separated {
		p.elems = append(p.elems, patElem{atom: &patAtom{set: g.atom(false).set}})
	}
}

// genPattern draws pattern i. Pattern 1 of each seed is a repeated record
// with a repeated inner capture, (?<P>a(?<K>b)+)*, whose inner list each
// occurrence of P starts again (Capture shapes 2 and 5).
func genPattern(r *rand.Rand, i int) *genPat {
	g := &patGen{r: r, separated: i%2 == 0 || i == 1}
	p := &genPat{typ: fmt.Sprintf("P%d", i)}
	if i == 1 {
		name := "C1n0"
		first := g.atom(false)
		inner := patElem{name: name + "k0", suffix: "+", body: g.body("", false)}
		p.elems = []patElem{{name: name, suffix: "*", body: []patElem{{atom: first}, inner, {atom: &patAtom{set: g.atom(false).set}}}}}
		g.terminal(p)
		return p
	}
	if r.IntN(5) == 0 {
		p.union = true
		for k := range 2 + r.IntN(2) {
			p.elems = append(p.elems, patElem{name: fmt.Sprintf("V%dn%d", i, k), body: g.body("", false)})
		}
		return p
	}
	captures := 0
	for k := range 1 + r.IntN(4) {
		if r.IntN(5) < 3 {
			name := fmt.Sprintf("C%dn%d", i, k)
			p.elems = append(p.elems, patElem{name: name, suffix: captureSuffixes[r.IntN(4)], body: g.body(name, r.IntN(2) == 0)})
			captures++
		} else {
			p.elems = append(p.elems, patElem{atom: g.atom(true)})
		}
	}
	if captures == 0 {
		name := fmt.Sprintf("C%dn9", i)
		p.elems = append(p.elems, patElem{name: name, suffix: "?", body: g.body(name, true)})
	}
	g.terminal(p)
	return p
}

func count(r *rand.Rand, quant string) int {
	switch quant {
	case "?":
		return r.IntN(2)
	case "*":
		return r.IntN(3)
	case "+":
		return 1 + r.IntN(3)
	case "{1,2}":
		return 1 + r.IntN(2)
	}
	return 1
}

func witnessOf(r *rand.Rand, b *strings.Builder, elems []patElem) {
	for _, e := range elems {
		if e.atom != nil {
			for range count(r, e.atom.quant) {
				b.WriteByte(e.atom.set[r.IntN(len(e.atom.set))])
			}
			continue
		}
		for range count(r, e.suffix) {
			witnessOf(r, b, e.body)
		}
	}
}

// witness is a word of p's language.
func (p *genPat) witness(r *rand.Rand) string {
	var b strings.Builder
	if p.union {
		witnessOf(r, &b, p.elems[r.IntN(len(p.elems))].body)
	} else {
		witnessOf(r, &b, p.elems)
	}
	return b.String()
}

func randomText(r *rand.Rand) string {
	var b strings.Builder
	for range r.IntN(6) {
		b.WriteByte("ab0c1"[r.IntN(5)])
	}
	return b.String()
}

// showFunc writes the Ergo function show<typ> of a record type whose
// fields are the captures of elems, and those of its nested records.
func showFunc(b *strings.Builder, typ string, elems []patElem) {
	var parts []string
	for _, e := range elems {
		if e.atom != nil {
			continue
		}
		if e.nested() {
			rt := typ + e.name
			showFunc(b, rt, e.body)
			fmt.Fprintf(b, "func opt%s(o Option[%s]) string {\n\tmatch o {\n\tcase Some as s: \"{\" + show%s(s.Value) + \"}\"\n\tcase None: \"-\"\n\t}\n}\n\n", rt, rt, rt)
			fmt.Fprintf(b, "func list%s(xs []%s) string {\n\tlet mut out = \"[\"\n\tfor x in xs {\n\t\tout = out + \"{\" + show%s(x) + \"}\"\n\t}\n\tout + \"]\"\n}\n\n", rt, rt, rt)
			switch e.suffix {
			case "":
				parts = append(parts, fmt.Sprintf("\"%s={\" + show%s(v.%s) + \"}\"", e.name, rt, e.name))
			case "?":
				parts = append(parts, fmt.Sprintf("\"%s=\" + opt%s(v.%s)", e.name, rt, e.name))
			default:
				parts = append(parts, fmt.Sprintf("\"%s=\" + list%s(v.%s)", e.name, rt, e.name))
			}
			continue
		}
		switch e.suffix {
		case "":
			parts = append(parts, fmt.Sprintf("\"%s=<\" + v.%s + \">\"", e.name, e.name))
		case "?":
			parts = append(parts, fmt.Sprintf("\"%s=\" + optS(v.%s)", e.name, e.name))
		default:
			parts = append(parts, fmt.Sprintf("\"%s=\" + listS(v.%s) + optS(slices.Last(v.%s))", e.name, e.name, e.name))
		}
	}
	fmt.Fprintf(b, "func show%s(v %s) string {\n\t%s\n}\n\n", typ, typ, strings.Join(parts, " + \" \" + "))
}

// source is p's derive and the functions that print its parse and find.
func (p *genPat) source() string {
	var b strings.Builder
	fmt.Fprintf(&b, "type %s derive Pattern(`%s`)\n\n", p.typ, p.text())
	if p.union {
		fmt.Fprintf(&b, "func show%s(v %s) string {\n\tmatch v {\n", p.typ, p.typ)
		for _, e := range p.elems {
			fmt.Fprintf(&b, "\tcase %s as x: \"%s=<\" + x.Value + \">\"\n", e.name, e.name)
		}
		b.WriteString("\t}\n}\n\n")
	} else {
		showFunc(&b, p.typ, p.elems)
	}
	fmt.Fprintf(&b, "func parse%s(s string) string fails NoMatch { show%s(try Parse%s(s)) }\n\n", p.typ, p.typ, p.typ)
	fmt.Fprintf(&b, "func runParse%s(s string) string {\n\tlet r = parse%s(s) catch {\n\tcase NoMatch as m: \"no \\(m.At)\"\n\t}\n\tr\n}\n\n", p.typ, p.typ)
	fmt.Fprintf(&b, "func runFind%s(s string, from int) string {\n\tmatch Find%s(s, from) {\n\tcase Some as m:\n\t\tlet (v, start, end) = m.Value\n\t\t\"\\(start) \\(end) \" + show%s(v)\n\tcase None: \"none\"\n\t}\n}\n\n", p.typ, p.typ, p.typ)
	return b.String()
}

const patternHelpers = `import slices

func optS(o Option[string]) string {
	match o {
	case Some as s: "<" + s.Value + ">"
	case None: "-"
	}
}

func listS(xs []string) string {
	let mut out = "["
	for x in xs {
		out = out + "<" + x + ">"
	}
	out + "]"
}

`

// occ is one occurrence of a capture in the reference's events, with the
// occurrences of the captures inside it.
type occ struct {
	tag, start, end int
	kids            []*occ
}

// occurrenceTree nests the reference events: an open starts an occurrence
// inside the innermost open one, a close ends it.
func occurrenceTree(events []regexpplan.Event, shift int) *occ {
	root := &occ{}
	stack := []*occ{root}
	for _, e := range events {
		if e.Tag > 0 {
			o := &occ{tag: e.Tag, start: e.Pos + shift}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, o)
			stack = append(stack, o)
		} else {
			stack[len(stack)-1].end = e.Pos + shift
			stack = stack[:len(stack)-1]
		}
	}
	return root
}

// expectRecord is what show prints for the record that elems project from
// the occurrence at.
func expectRecord(s string, tag map[string]int, elems []patElem, at *occ) string {
	var parts []string
	for _, e := range elems {
		if e.atom != nil {
			continue
		}
		var spans []*occ
		for _, k := range at.kids {
			if k.tag == tag[e.name] {
				spans = append(spans, k)
			}
		}
		value := func(o *occ) string {
			if e.nested() {
				return "{" + expectRecord(s, tag, e.body, o) + "}"
			}
			return "<" + s[o.start:o.end] + ">"
		}
		switch e.suffix {
		case "":
			if len(spans) != 1 {
				return fmt.Sprintf("%s took part %d times", e.name, len(spans))
			}
			parts = append(parts, e.name+"="+value(spans[0]))
		case "?":
			v := "-"
			if len(spans) == 1 {
				v = value(spans[0])
			}
			parts = append(parts, e.name+"="+v)
		default:
			var list strings.Builder
			last := "-"
			for _, o := range spans {
				list.WriteString(value(o))
				last = value(o)
			}
			if e.nested() {
				last = ""
			}
			parts = append(parts, e.name+"=["+list.String()+"]"+last)
		}
	}
	return strings.Join(parts, " ")
}

// expectShow is what show prints for the value the reference events give.
func (p *genPat) expectShow(s string, names []string, events []regexpplan.Event, shift int) string {
	tag := map[string]int{}
	for i, n := range names {
		tag[n] = i + 1
	}
	root := occurrenceTree(events, shift)
	if p.union {
		for _, e := range p.elems {
			for _, k := range root.kids {
				if k.tag == tag[e.name] {
					return e.name + "=<" + s[k.start:k.end] + ">"
				}
			}
		}
		return "no variant took part"
	}
	return expectRecord(s, tag, p.elems, root)
}

// patternLowerings are the parse lowerings the agreement compares: the
// selected engine, the reference NFA, and the direct and table forms
// forced on every plan with a deterministic candidate.
var patternLowerings = []string{"", "reference", "direct", "table"}

// TestPatternLoweringAgrees holds the derived bundle of generated patterns
// (patterns.md, Capture shapes 1-5 and 7; The pattern bundle 1 and 3;
// Lowering 3 and 6) to the reference: for each generated pattern and
// input, ParseT and FindT print, under each parse lowering, the value that
// the canonical NFA's events project and the failure position the plan
// computes. The lowerings are the selected engine, the reference NFA, and
// the direct and table forms forced on every plan that has a
// deterministic candidate, so each engine runs on the same patterns.
// Patterns are records of required, optional and repeated captures, with
// nested records under required, optional and repeated parents, and
// unions of named captures, so the typed adapters of both result forms
// (fixed slots and events) are reached.
//
// It also counts, at run time, the allocations of each emitted parse
// matcher (testing.AllocsPerRun on every input): a fixed plan under the
// direct or table engine allocates nothing (Lowering 3).
//
// It builds Go because the question is what the emitted matchers and the
// lowered Ergo adapters over them compute and allocate at run time: no
// in-process interpreter runs lowered Ergo, and allocations are counted
// only by running the compiled matcher. All programs link into one binary.
func TestPatternLoweringAgrees(t *testing.T) {
	needsBuild(t)
	seeds := patternSeeds
	if *patternSeed != 0 {
		seeds = []uint64{*patternSeed}
	}
	// Each branch reached over the seeds.
	deterministic, nfaOnly, fixed, events, nested, nestedUnderRepeat := 0, 0, 0, 0, 0, 0
	matched, failed, zeroChecked := 0, 0, 0
	defer func() {
		t.Logf("reached %d deterministic and %d NFA-only plans, %d fixed and %d event plans, %d nested records (%d repeated with a repeated inner capture), %d matches, %d failures, %d zero-allocation checks", deterministic, nfaOnly, fixed, events, nested, nestedUnderRepeat, matched, failed, zeroChecked)
		if !t.Failed() && *patternSeed == 0 && (deterministic == 0 || nfaOnly == 0 || fixed == 0 || events == 0 || nested == 0 || nestedUnderRepeat == 0 || matched == 0 || failed == 0 || zeroChecked == 0) {
			t.Errorf("the generator reached %d deterministic and %d NFA-only plans, %d fixed and %d event plans, %d nested records (%d repeated with a repeated inner capture), %d matches, %d failures and %d zero-allocation checks", deterministic, nfaOnly, fixed, events, nested, nestedUnderRepeat, matched, failed, zeroChecked)
		}
	}()
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x7061))
		var src, calls, want strings.Builder
		src.WriteString(patternHelpers)
		var pats []*genPat
		var inputs [][]string
		var froms [][]int
		for i := range 14 {
			p := genPattern(r, i)
			pats = append(pats, p)
			src.WriteString(p.source())
			for _, e := range p.elems {
				if e.atom == nil && e.nested() {
					nested++
					for _, c := range e.body {
						if (e.suffix == "*" || e.suffix == "+") && (c.suffix == "*" || c.suffix == "+") {
							nestedUnderRepeat++
						}
					}
				}
			}
			var in []string
			var fr []int
			for k := range 8 {
				var s string
				switch k % 4 {
				case 0, 1:
					s = p.witness(r)
				case 2:
					s = randomText(r)
				default:
					s = randomText(r) + p.witness(r) + randomText(r)
				}
				in = append(in, s)
				fr = append(fr, r.IntN(len(s)+1))
				fmt.Fprintf(&calls, "\tConsole.Println(runParse%s(%q))\n", p.typ, s)
				fmt.Fprintf(&calls, "\tConsole.Println(runFind%s(%q, %d))\n", p.typ, s, fr[k])
			}
			inputs = append(inputs, in)
			froms = append(froms, fr)
		}
		src.WriteString("func main() needs Console {\n" + calls.String() + "}\n")
		program := src.String()

		var programs []*lowered
		prev := SetPatternLoweringForTest("")
		for _, mode := range patternLowerings {
			SetPatternLoweringForTest(mode)
			name := "selected"
			if mode != "" {
				name = mode
			}
			programs = append(programs, mustLower(t, fmt.Sprintf("pattern%s%d", name, seed), program))
		}
		SetPatternLoweringForTest(prev)
		// The forced forms are in the Go: the table form alone reads a
		// table (regexpplan, emit.go tableStep).
		hasTable := func(o *lowered) bool { return strings.Contains(o.mainGo(), "const offsets =") }
		if !hasTable(programs[3]) || hasTable(programs[2]) || hasTable(programs[1]) {
			t.Fatalf("seed %d: the forced lowerings do not emit their forms: table form in reference %v, direct %v, table %v", seed, hasTable(programs[1]), hasTable(programs[2]), hasTable(programs[3]))
		}

		// The allocation harness: each emitted parse matcher under each
		// engine it can take, counted on every input.
		var harness, measure strings.Builder
		harness.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nvar sink int\n\n")
		var allocRows []string // the expected fixed, deterministic rows
		for i, p := range pats {
			var parse, find *regexpplan.Plan
			for _, d := range programs[0].pkg.Derive.Derived {
				if d.Type == p.typ {
					parse, find = d.Parse, d.Find
				}
			}
			if parse == nil || find == nil {
				t.Fatalf("seed %d: %s has no derive record\n%s", seed, p.typ, program)
			}
			engines := []regexpplan.Engine{regexpplan.NFA}
			if parse.Deterministic() {
				deterministic++
				engines = append(engines, regexpplan.Direct, regexpplan.Table)
			} else {
				nfaOnly++
			}
			if parse.Fixed() {
				fixed++
			} else {
				events++
			}
			for _, e := range engines {
				fn := fmt.Sprintf("match%s%s", p.typ, e)
				code, err := parse.EmitParse(fn, e)
				if err != nil {
					t.Fatalf("seed %d: %s %s: %v", seed, p.typ, e, err)
				}
				harness.WriteString(code + "\n")
				for _, s := range inputs[i] {
					fmt.Fprintf(&measure, "\tfmt.Println(%q, %q, testing.AllocsPerRun(20, func() { sink += %s(%q)[0] }))\n", fn, s, fn, s)
					if parse.Fixed() && e != regexpplan.NFA {
						allocRows = append(allocRows, fn+" "+s)
					}
				}
			}
			names := parse.Names()
			for k, s := range inputs[i] {
				ref := parse.Reference(s)
				res := parse.ParseResult(s)
				if ref.OK != res.OK {
					t.Fatalf("seed %d: %s on %q: reference ok %v, plan ok %v", seed, p.typ, s, ref.OK, res.OK)
				}
				if ref.OK {
					matched++
					want.WriteString(p.expectShow(s, names, ref.Events, 0) + "\n")
				} else {
					failed++
					fmt.Fprintf(&want, "no %d\n", res.At)
				}
				from := froms[i][k]
				m := find.Reference(s[from:])
				if m.OK {
					fmt.Fprintf(&want, "%d %d %s\n", m.Start+from, m.End+from, p.expectShow(s, find.Names(), m.Events, from))
				} else {
					want.WriteString("none\n")
				}
			}
		}
		harness.WriteString("func main() {\n" + measure.String() + "}\n")
		allocs := &lowered{name: fmt.Sprintf("patternallocs%d", seed), src: "(the allocation harness; Go only)", files: []File{{Path: "main.go", Data: []byte(harness.String())}}}

		exes := buildAll(t, append(append([]*lowered{}, programs...), allocs), true)
		for _, o := range programs {
			out, stderr, code := runExe(t, exes[o.name])
			if code != 0 || out != want.String() {
				t.Fatalf("seed %d: %s differs from the reference (exit %d, stderr %s)\nreplay: go test -run TestPatternLoweringAgrees ./internal/sketch/lower -pattern.seed=%d\nsource:\n%s\ngot:\n%s\nwant:\n%s", seed, o.name, code, stderr, seed, program, out, want.String())
			}
		}
		out, stderr, code := runExe(t, exes[allocs.name])
		if code != 0 {
			t.Fatalf("seed %d: the allocation harness exited %d: %s", seed, code, stderr)
		}
		counted := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			// fmt.Println writes the name, the input and the count, by spaces;
			// inputs hold no space.
			parts := strings.Split(line, " ")
			counted[parts[0]+" "+strings.Join(parts[1:len(parts)-1], " ")] = parts[len(parts)-1]
		}
		for _, row := range allocRows {
			n, ok := counted[row]
			if !ok {
				t.Fatalf("seed %d: no allocation count for %s\n%s", seed, row, out)
			}
			if n != "0" {
				t.Errorf("seed %d: the fixed parse %s allocated %s times per run (want 0; Lowering 3)\nreplay: go test -run TestPatternLoweringAgrees ./internal/sketch/lower -pattern.seed=%d", seed, row, n, seed)
			}
			zeroChecked++
		}
	}
}
