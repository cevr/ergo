package lower

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
)

// The table form of a pattern's matcher (types.md, Refinements 11). The
// straight-line form (MatcherText) writes a case per state, so its code
// grows with the automaton; past PatternTableStates states the lowering
// writes the automaton as data instead, read by one loop:
//
//	var ergoMatchTable = struct {
//		ascii  [128]uint8 // the class of each ASCII rune
//		hiLo   [K]int32   // the first rune of each span past ASCII …
//		hiCls  [K]uint8   // … and its class
//		next   [S*C]uint8 // state*C+class: the next state + 1, 0 to reject
//		accept [S]bool
//	}{…}
//
// Two runes are of one class when every state takes them to the same
// state (the classes of a lexer's byte-class map, as re2c and RE2's DFA
// compute them). The measurement that chose the threshold is
// docs/receipts/fkre-patterns.md, "Two forms".

// PatternTableStates is the largest automaton the straight-line form
// writes; a larger one is a table. Tests set it to 0 or past every
// automaton to force one form, the other being the reference.
var PatternTableStates = 24

// patternTableKey is the naming pass's key of the table of m.
func patternTableKey(m *check.Matcher) pkgKey { return pkgKey{"patternTable", m.Source} }

// patternClasses splits the runes 0..MaxRune into the spans no state tells
// apart: each span's class, and the target of each state and class (-1
// for none).
type patternClasses struct {
	starts []rune // the first rune of each span, ascending; starts[0] is 0
	class  []int  // the class of each span
	next   [][]int
}

func classesOf(m *check.Matcher) patternClasses {
	cutSet := map[rune]bool{0: true, utf8.RuneSelf: true}
	for _, st := range m.States {
		for _, e := range st.Edges {
			cutSet[e.Lo] = true
			if e.Hi < utf8.MaxRune {
				cutSet[e.Hi+1] = true
			}
		}
	}
	var cuts []rune
	for r := range cutSet {
		cuts = append(cuts, r)
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i] < cuts[j] })
	target := func(q int, r rune) int {
		for _, e := range m.States[q].Edges {
			if r >= e.Lo && r <= e.Hi {
				return e.To
			}
		}
		return -1
	}
	var pc patternClasses
	ids := map[string]int{}
	for _, lo := range cuts {
		col := make([]int, len(m.States))
		var key strings.Builder
		for q := range m.States {
			col[q] = target(q, lo)
			key.WriteString(strconv.Itoa(col[q]) + ",")
		}
		id, ok := ids[key.String()]
		if !ok {
			id = len(ids)
			ids[key.String()] = id
			pc.next = append(pc.next, col)
		}
		// Adjacent spans of one class past ASCII merge; ASCII stays split
		// at RuneSelf, where the map ends.
		if n := len(pc.starts); n > 0 && pc.class[n-1] == id && lo != utf8.RuneSelf {
			continue
		}
		pc.starts = append(pc.starts, lo)
		pc.class = append(pc.class, id)
	}
	return pc
}

// MatcherTableText is the table form of the matcher name of m, its data
// in the package variable table.
func MatcherTableText(name, table string, m *check.Matcher) string {
	pc := classesOf(m)
	nClass := len(pc.next)
	cell := "uint8"
	if len(m.States)+1 > 255 {
		cell = "uint16"
	}
	clsType := "uint8"
	if nClass > 255 {
		clsType = "uint16"
	}
	var ascii []string
	span := 0
	for r := rune(0); r < utf8.RuneSelf; r++ {
		for span+1 < len(pc.starts) && pc.starts[span+1] <= r {
			span++
		}
		ascii = append(ascii, strconv.Itoa(pc.class[span]))
	}
	var hiLo, hiCls []string
	for i, lo := range pc.starts {
		if lo >= utf8.RuneSelf {
			hiLo = append(hiLo, strconv.Itoa(int(lo)))
			hiCls = append(hiCls, strconv.Itoa(pc.class[i]))
		}
	}
	var next, accept []string
	for q, st := range m.States {
		for c := 0; c < nClass; c++ {
			next = append(next, strconv.Itoa(pc.next[c][q]+1))
		}
		accept = append(accept, strconv.FormatBool(st.Accept))
	}
	k, s, cells := strconv.Itoa(len(hiLo)), strconv.Itoa(len(m.States)), strconv.Itoa(len(next))
	var b strings.Builder
	b.WriteString("// " + table + " is the automaton of " + name + " (types.md, Refinements 11).\n")
	b.WriteString("var " + table + " = struct {\n\tascii  [128]" + clsType + "\n\thiLo   [" + k + "]int32\n\thiCls  [" + k + "]" + clsType + "\n\tnext   [" + cells + "]" + cell + "\n\taccept [" + s + "]bool\n}{\n")
	b.WriteString("\tascii: [128]" + clsType + "{" + strings.Join(ascii, ", ") + "},\n")
	b.WriteString("\thiLo: [" + k + "]int32{" + strings.Join(hiLo, ", ") + "},\n")
	b.WriteString("\thiCls: [" + k + "]" + clsType + "{" + strings.Join(hiCls, ", ") + "},\n")
	b.WriteString("\tnext: [" + cells + "]" + cell + "{" + strings.Join(next, ", ") + "},\n")
	b.WriteString("\taccept: [" + s + "]bool{" + strings.Join(accept, ", ") + "},\n}\n\n")
	b.WriteString("// " + name + " matches the whole of s against the pattern " + strconv.Quote(m.Source) + " (types.md, Refinements 11), a walk of " + table + ".\n")
	b.WriteString("func " + name + "(s string) bool {\n")
	b.WriteString("\tt := &" + table + "\n\tq := 1\n\tfor _, r := range s {\n\t\tvar c int\n\t\tif r < 128 {\n\t\t\tc = int(t.ascii[r])\n\t\t} else {\n")
	b.WriteString("\t\t\tlo, hi := 0, len(t.hiLo)\n\t\t\tfor lo+1 < hi {\n\t\t\t\tmid := int(uint(lo+hi) >> 1)\n\t\t\t\tif t.hiLo[mid] <= r {\n\t\t\t\t\tlo = mid\n\t\t\t\t} else {\n\t\t\t\t\thi = mid\n\t\t\t\t}\n\t\t\t}\n\t\t\tc = int(t.hiCls[lo])\n\t\t}\n")
	b.WriteString("\t\tq = int(t.next[(q-1)*" + strconv.Itoa(nClass) + "+c])\n\t\tif q == 0 {\n\t\t\treturn false\n\t\t}\n\t}\n\treturn t.accept[q-1]\n}\n\n")
	return b.String()
}
