package lower

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A refinement's pattern (types.md, Refinements 11). The checker compiled
// matches(it, `…`) to a minimal automaton (check.Matcher); the lowering
// writes it once per pattern as a Go function of the value, a loop over
// its runes with a switch on the state and the rune spans of each state as
// comparisons: no regexp is built or run, and a match allocates nothing.
//
//	func ergoMatch(s string) bool {
//		q := 0
//		for _, r := range s {
//			switch q {
//			case 0:
//				switch {
//				case r >= 'a' && r <= 'z':
//					q = 1
//				default:
//					return false
//				}
//			…
//		}
//		return q == 1
//	}
//
// Prior art: re2c and Ragel write the same shape at build time; Go's
// regexp builds its program when MustCompile runs and interprets it.

// patternKey is the naming pass's key of the matcher of m.
func patternKey(m *check.Matcher) pkgKey { return pkgKey{"pattern", m.Source} }

// reservePatterns reserves the name of the matcher of every pattern call
// of pkgs, in the naming pass.
func reservePatterns(pkgs []*check.Package, reserve func(key pkgKey, base string)) {
	for _, p := range pkgs {
		for _, c := range p.Info.Calls {
			if c.Kind == check.CallPattern {
				reserve(patternKey(c.Pattern), "ergoMatch")
			}
		}
	}
}

// patternCall is the Go text of the pattern call x.
func (fc *funcCtx) patternCall(x *syntax.CallExpr, c *check.Call) operand {
	arg := fc.value(x.Args[0])
	if !isPlainString(fc.typeOf(x.Args[0])) {
		arg = "string(" + arg + ")"
	}
	return operand{val: fc.l.patternMatcher(c.Pattern) + "(" + arg + ")"}
}

// isPlainString reports the predeclared string type.
func isPlainString(t check.Type) bool {
	b, ok := t.(*check.Basic)
	return ok && b.Kind == check.String
}

// patternMatcher is the name of the Go function that matches m, written
// once.
func (l *lowerer) patternMatcher(m *check.Matcher) string {
	name := l.pkgName(patternKey(m), "ergoMatch")
	if l.patternsDone == nil {
		l.patternsDone = map[string]bool{}
	}
	if !l.patternsDone[name] {
		l.patternsDone[name] = true
		l.user.WriteString(MatcherText(name, m))
	}
	return name
}

// MatcherText is the Go function name that matches m (see above).
func MatcherText(name string, m *check.Matcher) string {
	var b strings.Builder
	b.WriteString("// " + name + " matches the whole of s against the pattern " + strconv.Quote(m.Source) + " (types.md, Refinements 11).\n")
	b.WriteString("func " + name + "(s string) bool {\n")
	var accepting []string
	for q, st := range m.States {
		if st.Accept {
			accepting = append(accepting, strconv.Itoa(q))
		}
	}
	final := "false"
	switch len(accepting) {
	case 0:
	case 1:
		final = "q == " + accepting[0]
	default:
		final = ""
	}
	moves := false
	for _, st := range m.States {
		moves = moves || len(st.Edges) > 0
	}
	if !moves {
		// Only the empty value can match.
		if m.States[0].Accept {
			b.WriteString("\treturn len(s) == 0\n}\n\n")
		} else {
			b.WriteString("\treturn false\n}\n\n")
		}
		return b.String()
	}
	b.WriteString("\tq := 0\n\tfor _, r := range s {\n\t\tswitch q {\n")
	for q, st := range m.States {
		b.WriteString("\t\tcase " + strconv.Itoa(q) + ":\n")
		if len(st.Edges) == 0 {
			b.WriteString("\t\t\treturn false\n")
			continue
		}
		// The spans of each target, in the order of their first span.
		var targets []int
		spans := map[int][]string{}
		for _, e := range st.Edges {
			if _, ok := spans[e.To]; !ok {
				targets = append(targets, e.To)
			}
			spans[e.To] = append(spans[e.To], spanTest(e.Lo, e.Hi))
		}
		b.WriteString("\t\t\tswitch {\n")
		for _, to := range targets {
			b.WriteString("\t\t\tcase " + strings.Join(spans[to], ", ") + ":\n")
			b.WriteString("\t\t\t\tq = " + strconv.Itoa(to) + "\n")
		}
		b.WriteString("\t\t\tdefault:\n\t\t\t\treturn false\n\t\t\t}\n")
	}
	b.WriteString("\t\t}\n\t}\n")
	if final != "" {
		b.WriteString("\treturn " + final + "\n}\n\n")
		return b.String()
	}
	b.WriteString("\tswitch q {\n\tcase " + strings.Join(accepting, ", ") + ":\n\t\treturn true\n\t}\n\treturn false\n}\n\n")
	return b.String()
}

// spanTest is the Go condition that the rune r lies in lo..hi.
func spanTest(lo, hi rune) string {
	switch {
	case lo == hi:
		return "r == " + runeLit(lo)
	case lo == 0:
		return "r <= " + runeLit(hi)
	case hi == utf8.MaxRune:
		return "r >= " + runeLit(lo)
	}
	return "r >= " + runeLit(lo) + " && r <= " + runeLit(hi)
}

// runeLit is r as a Go rune literal: quoted when it prints, else in hex.
func runeLit(r rune) string {
	if r < utf8.RuneSelf && unicode.IsPrint(r) {
		return strconv.QuoteRune(r)
	}
	return "0x" + strconv.FormatInt(int64(r), 16)
}

// patternTable is m's automaton as a Go literal the harness's generator
// walks ($GenMatch): per state, its edges {lo, hi, to}, and whether it
// accepts.
func patternTable(m *check.Matcher) (edges, accept string) {
	var es, as []string
	for _, st := range m.States {
		var e []string
		for _, x := range st.Edges {
			e = append(e, "{"+strconv.Itoa(int(x.Lo))+", "+strconv.Itoa(int(x.Hi))+", "+strconv.Itoa(x.To)+"}")
		}
		es = append(es, "{"+strings.Join(e, ", ")+"}")
		as = append(as, strconv.FormatBool(st.Accept))
	}
	return "[][][3]int32{" + strings.Join(es, ", ") + "}", "[]bool{" + strings.Join(as, ", ") + "}"
}
