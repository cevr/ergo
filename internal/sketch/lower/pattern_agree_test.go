package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var patternSeed = flag.Uint64("pattern.seed", 0, "run TestPatternLoweringAgrees on this seed alone")

var patternSeeds = []uint64{1, 2}

type patAtom struct{ set, quant string }

type patElem struct {
	atom   *patAtom
	name   string
	suffix string
	body   []patElem
}

type genPat struct {
	typ   string
	union bool
	elems []patElem
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
		body = append(body, patElem{atom: &patAtom{set: g.atom(false).set}})
	}
	return body
}

func (g *patGen) terminal(p *genPat) {
	if g.separated {
		p.elems = append(p.elems, patElem{atom: &patAtom{set: g.atom(false).set}})
	}
}

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

func (p *genPat) source() string {
	var b strings.Builder
	fmt.Fprintf(&b, "type %s derive regex.Pattern(`%s`)\n\n", p.typ, p.text())
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

const patternHelpers = `import regex
import slices

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

type refEvent struct{ tag, pos int }

type occ struct {
	tag, start, end int
	kids            []*occ
}

func occurrenceTree(events []refEvent, shift int) *occ {
	root := &occ{}
	stack := []*occ{root}
	for _, e := range events {
		if e.tag > 0 {
			o := &occ{tag: e.tag, start: e.pos + shift}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, o)
			stack = append(stack, o)
		} else {
			stack[len(stack)-1].end = e.pos + shift
			stack = stack[:len(stack)-1]
		}
	}
	return root
}

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

type refOp uint8

const (
	refAccept refOp = iota
	refByte
	refSplit
	refTag
)

type refInst struct {
	op   refOp
	x, y int
	tag  int
	set  string
}

type refNFA struct {
	code  []refInst
	start int
	names []string
}

func buildRefNFA(p *genPat) *refNFA {
	nfa := &refNFA{}
	tagByName := map[string]int{}
	add := func(i refInst) int {
		nfa.code = append(nfa.code, i)
		return len(nfa.code) - 1
	}
	var compileElems func([]patElem, int) int
	compileAtom := func(a *patAtom, next int) int {
		start := next
		switch a.quant {
		case "?":
			start = add(refInst{op: refSplit, x: add(refInst{op: refByte, set: a.set, x: next}), y: next})
		case "*":
			sp := add(refInst{op: refSplit, y: next})
			nfa.code[sp].x = add(refInst{op: refByte, set: a.set, x: sp})
			start = sp
		case "+":
			sp := add(refInst{op: refSplit, y: next})
			body := add(refInst{op: refByte, set: a.set, x: sp})
			nfa.code[sp].x = body
			start = body
		case "{1,2}":
			opt := add(refInst{op: refSplit, x: add(refInst{op: refByte, set: a.set, x: next}), y: next})
			start = add(refInst{op: refByte, set: a.set, x: opt})
		default:
			start = add(refInst{op: refByte, set: a.set, x: next})
		}
		return start
	}
	compileElem := func(e patElem, next int) int {
		if e.atom != nil {
			return compileAtom(e.atom, next)
		}
		tag := len(nfa.names) + 1
		nfa.names = append(nfa.names, e.name)
		tagByName[e.name] = tag
		wrapBody := func(nxt int) int {
			cl := add(refInst{op: refTag, tag: -tag, x: nxt})
			return add(refInst{op: refTag, tag: tag, x: compileElems(e.body, cl)})
		}
		switch e.suffix {
		case "?":
			return add(refInst{op: refSplit, x: wrapBody(next), y: next})
		case "*":
			sp := add(refInst{op: refSplit, y: next})
			nfa.code[sp].x = wrapBody(sp)
			return sp
		case "+":
			sp := add(refInst{op: refSplit, y: next})
			body := wrapBody(sp)
			nfa.code[sp].x = body
			return body
		default:
			return wrapBody(next)
		}
	}
	compileElems = func(elems []patElem, next int) int {
		for i := len(elems) - 1; i >= 0; i-- {
			next = compileElem(elems[i], next)
		}
		return next
	}
	acc := add(refInst{op: refAccept})
	if p.union {
		cur := compileElem(p.elems[len(p.elems)-1], acc)
		for i := len(p.elems) - 2; i >= 0; i-- {
			cur = add(refInst{op: refSplit, x: compileElem(p.elems[i], acc), y: cur})
		}
		nfa.start = cur
	} else {
		nfa.start = compileElems(p.elems, acc)
	}
	return nfa
}

type refHist struct {
	ev   refEvent
	prev *refHist
}

type refThread struct {
	pc int
	h  *refHist
}

func (nfa *refNFA) closure(in []refThread, pos int) []refThread {
	seen := make([]bool, len(nfa.code))
	var out []refThread
	var visit func(refThread)
	visit = func(t refThread) {
		if seen[t.pc] {
			return
		}
		seen[t.pc] = true
		inst := nfa.code[t.pc]
		switch inst.op {
		case refSplit:
			visit(refThread{inst.x, t.h})
			visit(refThread{inst.y, t.h})
		case refTag:
			visit(refThread{inst.x, &refHist{refEvent{inst.tag, pos}, t.h}})
		default:
			out = append(out, t)
		}
	}
	for _, t := range in {
		visit(t)
	}
	return out
}

func (nfa *refNFA) run(s string, find bool) (ok bool, start, end, failAt int, events []refEvent) {
	last := 0
	if find {
		last = len(s)
	}
	for st := 0; st <= last; st++ {
		cur := []refThread{{nfa.start, nil}}
		bestOK := false
		var bestEnd int
		var bestH *refHist
		for pos := st; ; pos++ {
			ready := nfa.closure(cur, pos)
			var next []refThread
			for _, t := range ready {
				inst := nfa.code[t.pc]
				if inst.op == refAccept && (find || pos == len(s)) {
					bestOK, bestEnd, bestH = true, pos, t.h
					break
				}
				if inst.op == refByte && pos < len(s) && strings.IndexByte(inst.set, s[pos]) >= 0 {
					next = append(next, refThread{inst.x, t.h})
				}
			}
			if pos == len(s) || len(next) == 0 {
				failAt = pos
				break
			}
			cur = next
		}
		if bestOK {
			var rev []refEvent
			for p := bestH; p != nil; p = p.prev {
				rev = append(rev, p.ev)
			}
			events = make([]refEvent, len(rev))
			for i := range rev {
				events[i] = rev[len(rev)-1-i]
			}
			return true, st, bestEnd, bestEnd, events
		}
	}
	return false, 0, 0, failAt, nil
}

func (p *genPat) expectShow(s string, names []string, events []refEvent, shift int) string {
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

// TestPatternLoweringAgrees compares userland std/regex ParseT and FindT
// compiled end-to-end through the Content Mapper pipeline and ordinary Ergo
// lowering against an independent Go Pike NFA reference oracle.
func TestPatternLoweringAgrees(t *testing.T) {
	needsBuild(t)
	seeds := patternSeeds
	if *patternSeed != 0 {
		seeds = []uint64{*patternSeed}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x7061))
		var src, calls, want strings.Builder
		src.WriteString(patternHelpers)
		for i := range 14 {
			p := genPattern(r, i)
			src.WriteString(p.source())
			nfa := buildRefNFA(p)
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
				from := r.IntN(len(s) + 1)
				fmt.Fprintf(&calls, "\tConsole.Println(runParse%s(%q))\n", p.typ, s)
				fmt.Fprintf(&calls, "\tConsole.Println(runFind%s(%q, %d))\n", p.typ, s, from)

				okP, _, _, failAt, evP := nfa.run(s, false)
				if okP {
					want.WriteString(p.expectShow(s, nfa.names, evP, 0) + "\n")
				} else {
					fmt.Fprintf(&want, "no %d\n", failAt)
				}
				okF, stF, endF, _, evF := nfa.run(s[from:], true)
				if okF {
					fmt.Fprintf(&want, "%d %d %s\n", stF+from, endF+from, p.expectShow(s, nfa.names, evF, from))
				} else {
					want.WriteString("none\n")
				}
			}
		}
		src.WriteString("func main() needs Console {\n" + calls.String() + "}\n")
		program := src.String()
		built := mustLower(t, fmt.Sprintf("pattern%d", seed), program)
		exes := buildAll(t, []*lowered{built}, true)
		out, stderr, code := runExe(t, exes[built.name])
		if code != 0 || out != want.String() {
			t.Fatalf("seed %d: compiled std/regex differs from oracle (exit %d, stderr %s)\nreplay: go test -run TestPatternLoweringAgrees ./internal/sketch/lower -pattern.seed=%d\nsource:\n%s\ngot:\n%s\nwant:\n%s",
				seed, code, stderr, seed, program, out, want.String())
		}
	}
}
