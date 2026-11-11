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

// A generated pattern: a record of items (plain atoms and flat captures,
// each capture required, optional or repeated), or a union of named
// captures. Flat captures keep the reference projection small; nested
// records and their shapes are TestPatternShapes' (internal/sketch/derive).
type patAtom struct{ set, quant string }

type patItem struct {
	name   string // "" for a plain atom
	suffix string // a capture's repeat: "", "?", "*" or "+"
	atoms  []patAtom
}

type genPat struct {
	typ   string
	union bool
	items []patItem // the variants, for a union
}

func (a patAtom) text() string {
	if len(a.set) == 1 {
		return a.set + a.quant
	}
	return "[" + a.set + "]" + a.quant
}

func (p *genPat) text() string {
	var parts []string
	for _, it := range p.items {
		var body strings.Builder
		for _, a := range it.atoms {
			body.WriteString(a.text())
		}
		if it.name == "" {
			parts = append(parts, body.String())
		} else {
			parts = append(parts, "(?<"+it.name+">"+body.String()+")"+it.suffix)
		}
	}
	if p.union {
		return strings.Join(parts, "|")
	}
	return strings.Join(parts, "")
}

var patSets = []string{"a", "b", "0", "ab", "a0", "abc", "01"}

func genAtom(r *rand.Rand, nullable bool) patAtom {
	quants := []string{"", "+", "{1,2}"}
	if nullable {
		quants = append(quants, "?", "*")
	}
	return patAtom{patSets[r.IntN(len(patSets))], quants[r.IntN(len(quants))]}
}

func genCaptureBody(r *rand.Rand) []patAtom {
	atoms := []patAtom{genAtom(r, false)}
	if r.IntN(2) == 0 {
		atoms = append(atoms, genAtom(r, false))
	}
	return atoms
}

func genPattern(r *rand.Rand, i int) *genPat {
	p := &genPat{typ: fmt.Sprintf("P%d", i)}
	if r.IntN(4) == 0 {
		p.union = true
		for k := range 2 + r.IntN(2) {
			p.items = append(p.items, patItem{name: fmt.Sprintf("V%dn%d", i, k), atoms: genCaptureBody(r)})
		}
		return p
	}
	captures := 0
	for k := range 1 + r.IntN(4) {
		if r.IntN(5) < 3 {
			p.items = append(p.items, patItem{name: fmt.Sprintf("C%dn%d", i, k), suffix: []string{"", "?", "*", "+"}[r.IntN(4)], atoms: genCaptureBody(r)})
			captures++
		} else {
			p.items = append(p.items, patItem{atoms: []patAtom{genAtom(r, true)}})
		}
	}
	if captures == 0 {
		p.items = append(p.items, patItem{name: fmt.Sprintf("C%dn9", i), suffix: "?", atoms: genCaptureBody(r)})
	}
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

// witness is a word of p's language.
func (p *genPat) witness(r *rand.Rand) string {
	var b strings.Builder
	items := p.items
	if p.union {
		items = []patItem{items[r.IntN(len(items))]}
	}
	for _, it := range items {
		for range count(r, it.suffix) {
			for _, a := range it.atoms {
				for range count(r, a.quant) {
					b.WriteByte(a.set[r.IntN(len(a.set))])
				}
			}
		}
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

// source is p's derive and the functions that print its parse and find.
func (p *genPat) source() string {
	var b strings.Builder
	fmt.Fprintf(&b, "type %s derive Pattern(`%s`)\n\n", p.typ, p.text())
	fmt.Fprintf(&b, "func show%s(v %s) string {\n", p.typ, p.typ)
	if p.union {
		b.WriteString("\tmatch v {\n")
		for _, it := range p.items {
			fmt.Fprintf(&b, "\tcase %s as x: \"%s=<\" + x.Value + \">\"\n", it.name, it.name)
		}
		b.WriteString("\t}\n")
	} else {
		var parts []string
		for _, it := range p.items {
			switch it.suffix {
			case "":
				if it.name != "" {
					parts = append(parts, fmt.Sprintf("\"%s=<\" + v.%s + \">\"", it.name, it.name))
				}
			case "?":
				parts = append(parts, fmt.Sprintf("\"%s=\" + optS(v.%s)", it.name, it.name))
			default:
				parts = append(parts, fmt.Sprintf("\"%s=\" + listS(v.%s) + optS(slices.Last(v.%s))", it.name, it.name, it.name))
			}
		}
		b.WriteString("\t" + strings.Join(parts, " + \" \" + ") + "\n")
	}
	b.WriteString("}\n\n")
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

// occurrences are the spans of each capture tag of events, in input order.
func occurrences(events []regexpplan.Event, shift int) map[int][][2]int {
	open := map[int]int{}
	occ := map[int][][2]int{}
	for _, e := range events {
		if e.Tag > 0 {
			open[e.Tag] = e.Pos + shift
		} else {
			occ[-e.Tag] = append(occ[-e.Tag], [2]int{open[-e.Tag], e.Pos + shift})
		}
	}
	return occ
}

// expectShow is what show prints for the value the reference events give.
func (p *genPat) expectShow(s string, names []string, occ map[int][][2]int) string {
	tag := map[string]int{}
	for i, n := range names {
		tag[n] = i + 1
	}
	text := func(span [2]int) string { return "<" + s[span[0]:span[1]] + ">" }
	if p.union {
		for _, it := range p.items {
			if spans := occ[tag[it.name]]; len(spans) == 1 {
				return it.name + "=" + text(spans[0])
			}
		}
		return "no variant took part"
	}
	var parts []string
	for _, it := range p.items {
		if it.name == "" {
			continue
		}
		spans := occ[tag[it.name]]
		switch it.suffix {
		case "":
			if len(spans) != 1 {
				return fmt.Sprintf("%s took part %d times", it.name, len(spans))
			}
			parts = append(parts, it.name+"="+text(spans[0]))
		case "?":
			v := "-"
			if len(spans) == 1 {
				v = text(spans[0])
			}
			parts = append(parts, it.name+"="+v)
		default:
			var list strings.Builder
			last := "-"
			for _, sp := range spans {
				list.WriteString(text(sp))
				last = text(sp)
			}
			parts = append(parts, it.name+"=["+list.String()+"]"+last)
		}
	}
	return strings.Join(parts, " ")
}

// TestPatternLoweringAgrees holds the derived bundle of generated patterns
// (patterns.md, The pattern bundle 1 and 3; Lowering 3 and 6) to the
// reference: for each generated pattern and input, ParseT and FindT print,
// under the selected engines and under the reference lowering, the value
// that the canonical NFA's events project by Capture shapes 2 and 7, and
// the failure position the plan computes. Patterns are records of flat
// captures (required, optional and repeated, read in full and by
// slices.Last) and unions of named captures, so the typed adapters of both
// result forms (fixed slots and events) and every variant are reached.
//
// It builds Go because the question is what the emitted matchers and the
// Ergo adapters over them compute at run time: the adapters are lowered
// Ergo, which no in-process interpreter runs. Both lowerings link into one
// binary.
func TestPatternLoweringAgrees(t *testing.T) {
	needsBuild(t)
	seeds := patternSeeds
	if *patternSeed != 0 {
		seeds = []uint64{*patternSeed}
	}
	// Each branch reached over the seeds: the selected direct engine (the
	// reference lowering is the NFA for every pattern), both result forms,
	// matches and failures.
	engines := map[regexpplan.Engine]int{}
	fixed, events, matched, failed := 0, 0, 0, 0
	defer func() {
		if !t.Failed() && *patternSeed == 0 && (engines[regexpplan.Direct] == 0 || fixed == 0 || events == 0 || matched == 0 || failed == 0) {
			t.Errorf("the generator reached engines %v, %d fixed and %d event plans, %d matches and %d failures", engines, fixed, events, matched, failed)
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

		prev := SetPatternLoweringForTest("")
		selected := mustLower(t, fmt.Sprintf("patternselected%d", seed), program)
		SetPatternLoweringForTest("reference")
		reference := mustLower(t, fmt.Sprintf("patternreference%d", seed), program)
		SetPatternLoweringForTest(prev)

		for i, p := range pats {
			var parse, find *regexpplan.Plan
			for _, d := range selected.pkg.Derive.Derived {
				if d.Type == p.typ {
					parse, find = d.Parse, d.Find
				}
			}
			if parse == nil || find == nil {
				t.Fatalf("seed %d: %s has no derive record\n%s", seed, p.typ, program)
			}
			engines[parse.Inspect().Engine]++
			if parse.Fixed() {
				fixed++
			} else {
				events++
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
					want.WriteString(p.expectShow(s, names, occurrences(ref.Events, 0)) + "\n")
				} else {
					failed++
					fmt.Fprintf(&want, "no %d\n", res.At)
				}
				from := froms[i][k]
				m := find.Reference(s[from:])
				if m.OK {
					fmt.Fprintf(&want, "%d %d %s\n", m.Start+from, m.End+from, p.expectShow(s, find.Names(), occurrences(m.Events, from)))
				} else {
					want.WriteString("none\n")
				}
			}
		}
		exes := buildAll(t, []*lowered{selected, reference}, true)
		for _, o := range []*lowered{selected, reference} {
			out, stderr, code := runExe(t, exes[o.name])
			if code != 0 || out != want.String() {
				t.Fatalf("seed %d: %s differs from the reference (exit %d, stderr %s)\nreplay: go test -run TestPatternLoweringAgrees ./internal/sketch/lower -pattern.seed=%d\nsource:\n%s\ngot:\n%s\nwant:\n%s", seed, o.name, code, stderr, seed, program, out, want.String())
			}
		}
	}
}
