package check_test

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// severalCaps are the four capabilities of the generated programs: each has
// one operation, a provider record with a field v, and a function needX
// that needs it.
var severalCaps = []string{"A", "B", "C", "D"}

func severalPrelude() string {
	var b strings.Builder
	for _, x := range severalCaps {
		fmt.Fprintf(&b, "type C%s { %s() int }\ntype P%s { v int }\nfunc %s(p P%s) int { p.v }\n", x, x, x, x, x)
		fmt.Fprintf(&b, "func need%s() int needs C%s { C%s.%s() }\n", x, x, x, x)
	}
	b.WriteString("type Tag { s string }\nfunc contextKey(t Tag) string { \"tag\" }\nfunc contextText(t Tag) string { t.s }\n")
	return b.String()
}

// The operand kinds of TestSeveralOperandsAreNesting; a paren kind is its
// base kind in parentheses.
var severalKinds = []string{"named", "local", "inline", "value", "(named)", "(local)", "(inline)", "(value)"}

// severalOperand is one drawn operand: its kind, the capabilities it binds
// (none for a value), and for a local or inline context the capability each
// provider needs while it is built ("" for none).
type severalOperand struct {
	kind  string
	binds []int
	needs map[int]int // capability bound -> capability its provider needs, -1 for none
}

// severalCase is one drawn program: its operands, the capabilities the
// block charges and the function's written row.
type severalCase struct {
	ops    []severalOperand
	charge []int
	row    []int
}

func drawSubset(r *rand.Rand, nonEmpty bool) []int {
	for {
		var s []int
		for i := range severalCaps {
			if r.Intn(2) == 0 {
				s = append(s, i)
			}
		}
		if len(s) > 0 || !nonEmpty {
			return s
		}
	}
}

func drawOperand(r *rand.Rand, kind string) severalOperand {
	op := severalOperand{kind: kind}
	base := strings.Trim(kind, "()")
	if base == "value" {
		return op
	}
	op.binds = drawSubset(r, true)
	op.needs = map[int]int{}
	for _, c := range op.binds {
		op.needs[c] = -1
		if base != "named" && r.Intn(3) == 0 {
			op.needs[c] = r.Intn(len(severalCaps))
		}
	}
	return op
}

// entries writes a context body's entries.
func (op severalOperand) entries() string {
	var parts []string
	for _, c := range op.binds {
		v := "0"
		if n := op.needs[c]; n >= 0 {
			v = "need" + severalCaps[n] + "()"
		}
		parts = append(parts, fmt.Sprintf("C%s: P%s{v: %s}", severalCaps[c], severalCaps[c], v))
	}
	return strings.Join(parts, ", ")
}

// source writes the case's program, flat (`with o1, …, on { b }`) or
// nested (`with o1 { … with on { b } … }`), with the with on one line, so
// both forms report on the same lines.
func (sc severalCase) source(nested bool) (src string, withLine int) {
	var b strings.Builder
	b.WriteString(severalPrelude())
	for i, op := range sc.ops {
		if strings.Trim(op.kind, "()") == "named" {
			fmt.Fprintf(&b, "let N%d = context { %s }\n", i, op.entries())
		}
	}
	row := ""
	if len(sc.row) > 0 {
		var names []string
		for _, c := range sc.row {
			names = append(names, "C"+severalCaps[c])
		}
		row = " needs " + strings.Join(names, " | ")
	}
	fmt.Fprintf(&b, "func F() int%s {\n", row)
	var operands []string
	for i, op := range sc.ops {
		var o string
		switch strings.Trim(op.kind, "()") {
		case "named":
			o = fmt.Sprintf("N%d", i)
		case "local":
			fmt.Fprintf(&b, "\tlet l%d = context { %s }\n", i, op.entries())
			o = fmt.Sprintf("l%d", i)
		case "inline":
			o = "context { " + op.entries() + " }"
		case "value":
			o = "(Tag{s: \"t\"})"
		}
		if strings.HasPrefix(op.kind, "(") {
			o = "(" + o + ")"
		}
		operands = append(operands, o)
	}
	var calls []string
	for _, c := range sc.charge {
		calls = append(calls, fmt.Sprintf("C%s.%s()", severalCaps[c], severalCaps[c]))
	}
	block := "0"
	if len(calls) > 0 {
		block = strings.Join(calls, " + ")
	}
	withLine = strings.Count(b.String(), "\n") + 1
	if nested {
		b.WriteString("\t")
		for _, o := range operands {
			b.WriteString("with " + o + " { ")
		}
		b.WriteString(block)
		b.WriteString(strings.Repeat(" }", len(operands)))
		b.WriteString("\n}\n")
	} else {
		fmt.Fprintf(&b, "\twith %s { %s }\n}\n", strings.Join(operands, ", "), block)
	}
	return b.String(), withLine
}

// reference computes, from the drawn sets alone, the capabilities each
// operand discharges (the innermost operand that binds a charge, among
// those before a provider's own operand for a provider's need) and the
// number of charges that no operand binds and the row does not hold, each
// an S102 at the with. A local context's needs are one set (its summary);
// an inline context's providers charge one by one.
func (sc severalCase) reference() (discharged [][]string, missing int) {
	inRow := map[int]bool{}
	for _, c := range sc.row {
		inRow[c] = true
	}
	binds := make([]map[int]bool, len(sc.ops))
	for i, op := range sc.ops {
		binds[i] = map[int]bool{}
		for _, c := range op.binds {
			binds[i][c] = true
		}
	}
	got := make([]map[int]bool, len(sc.ops))
	for i := range got {
		got[i] = map[int]bool{}
	}
	charge := func(c, below int) {
		for j := below - 1; j >= 0; j-- {
			if binds[j][c] {
				got[j][c] = true
				return
			}
		}
		if !inRow[c] {
			missing++
		}
	}
	for i, op := range sc.ops {
		base := strings.Trim(op.kind, "()")
		seen := map[int]bool{}
		for _, c := range op.binds {
			n := op.needs[c]
			if n < 0 || base == "local" && seen[n] {
				continue
			}
			seen[n] = true
			charge(n, i)
		}
	}
	for _, c := range sc.charge {
		charge(c, len(sc.ops))
	}
	discharged = make([][]string, len(sc.ops))
	for i := range got {
		for c := range got[i] {
			discharged[i] = append(discharged[i], "main.C"+severalCaps[c])
		}
		sort.Strings(discharged[i])
	}
	return discharged, missing
}

// withFacts lists the With fact of each operand of the function's with,
// outermost first: the chain of a flat with, or the nested withs by
// position.
func withFacts(info *check.Info) []*check.With {
	var xs []*syntax.CallExpr
	for x := range info.Withs {
		xs = append(xs, x)
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].Pos() < xs[j].Pos() })
	var out []*check.With
	for _, x := range xs {
		for w := info.Withs[x]; w != nil; w = w.Next {
			out = append(out, w)
		}
	}
	return out
}

// factText renders one operand's facts: what it discharges, the provider
// of each, and what it acquires, by the providers' source text.
func factText(src string, w *check.With) string {
	text := func(e *check.ContextEntry) string {
		if e == nil || e.Provider == nil {
			return "<nil>"
		}
		return src[e.Provider.Pos():e.Provider.End()]
	}
	var parts []string
	for i, d := range w.Discharged {
		p := "<none>"
		if i < len(w.Entries) {
			p = text(w.Entries[i])
		}
		parts = append(parts, d.String()+"="+p)
	}
	var acq []string
	for _, a := range w.Acquired {
		acq = append(acq, text(a.Entry))
	}
	return "discharged[" + strings.Join(parts, " ") + "] acquired[" + strings.Join(acq, " ") + "]"
}

// severalCases draws the cases of one run: the full product of operand
// kinds for lengths 1 to 3, then random lists of length 1 to 6, at least
// 2000 in all.
func severalCases(seed int64) []severalCase {
	r := rand.New(rand.NewSource(seed))
	var cases []severalCase
	draw := func(kinds []string) {
		sc := severalCase{charge: drawSubset(r, false), row: drawSubset(r, false)}
		for _, k := range kinds {
			sc.ops = append(sc.ops, drawOperand(r, k))
		}
		cases = append(cases, sc)
	}
	var product func(prefix []string, n int)
	product = func(prefix []string, n int) {
		if n == 0 {
			draw(prefix)
			return
		}
		for _, k := range severalKinds {
			product(append(append([]string(nil), prefix...), k), n-1)
		}
	}
	for n := 1; n <= 3; n++ {
		product(nil, n)
	}
	for len(cases) < 2100 {
		n := 1 + r.Intn(6)
		kinds := make([]string, n)
		for i := range kinds {
			kinds[i] = severalKinds[r.Intn(len(severalKinds))]
		}
		draw(kinds)
	}
	return cases
}

// runSeveral checks every case of a seed and returns a digest of every
// verdict, so two runs of one seed can be compared.
func runSeveral(t *testing.T, seed int64) string {
	t.Helper()
	var digest strings.Builder
	failures, sampled := 0, 0
	for n, sc := range severalCases(seed) {
		flat, line := sc.source(false)
		nested, _ := sc.source(true)
		fpkg, fdiags := check.CheckSource(flat)
		npkg, ndiags := check.CheckSource(nested)
		render := func(ds []check.Diagnostic) string {
			var out []string
			for _, d := range ds {
				out = append(out, fmt.Sprintf("%s@%d %s", d.Code, d.Line, d.Message))
			}
			return strings.Join(out, "\n\t")
		}
		fail := func(format string, args ...any) {
			failures++
			if failures <= 5 {
				t.Errorf("case %d: "+format+"\nflat:\n%s\nnested:\n%s", append([]any{n}, append(args, flat, nested)...)...)
			}
		}
		fd, nd := render(fdiags), render(ndiags)
		fmt.Fprintf(&digest, "%d|%s\n", n, fd)
		if fd != nd {
			fail("the flat and nested forms disagree:\n\t%s\nvs\n\t%s", fd, nd)
			continue
		}
		wantDischarged, missing := sc.reference()
		s102 := 0
		for _, d := range fdiags {
			if d.Code != "S102" || d.Line != line {
				fail("an unexpected diagnostic: %s", d)
			}
			s102++
		}
		if s102 != missing {
			fail("%d S102 at the with, the reference says %d:\n\t%s", s102, missing, fd)
			continue
		}
		if len(fdiags) > 0 {
			continue
		}
		ff, nf := withFacts(fpkg.Info), withFacts(npkg.Info)
		if len(ff) != len(sc.ops) || len(nf) != len(sc.ops) {
			fail("%d flat and %d nested With facts for %d operands", len(ff), len(nf), len(sc.ops))
			continue
		}
		var ffail, nfail []string
		for i := range ff {
			a, b := factText(flat, ff[i]), factText(nested, nf[i])
			if a != b {
				fail("operand %d: flat %s, nested %s", i, a, b)
			}
			var names []string
			for _, d := range ff[i].Discharged {
				names = append(names, d.String())
			}
			if strings.Join(names, " ") != strings.Join(wantDischarged[i], " ") {
				fail("operand %d discharges %v, the reference says %v", i, names, wantDischarged[i])
			}
			fmt.Fprintf(&digest, "\t%s\n", a)
		}
		for x, call := range fpkg.Info.Calls {
			if fpkg.Info.Withs[x] != nil && call.Fails != nil {
				ffail = append(ffail, call.Fails.String())
			}
		}
		for x, call := range npkg.Info.Calls {
			if npkg.Info.Withs[x] != nil && call.Fails != nil && x.Pos() == minWithPos(npkg.Info) {
				nfail = append(nfail, call.Fails.String())
			}
		}
		if strings.Join(ffail, " ") != strings.Join(nfail, " ") {
			fail("failure rows: flat %v, nested %v", ffail, nfail)
		}
		if n%5 == 0 {
			sampled++
			if gaps := check.InfoGaps(flat); len(gaps) > 0 {
				fail("Info gaps (flat):\n\t%s", strings.Join(gaps, "\n\t"))
			}
			if gaps := check.InfoGaps(nested); len(gaps) > 0 {
				fail("Info gaps (nested):\n\t%s", strings.Join(gaps, "\n\t"))
			}
		}
	}
	if failures > 5 {
		t.Errorf("%d failures in all", failures)
	}
	// A8: Info is complete over a sample of at least 200 clean programs.
	if sampled < 200 {
		t.Errorf("Info checked over %d clean programs, want at least 200", sampled)
	}
	return digest.String()
}

// minWithPos is the position of the outermost with.
func minWithPos(info *check.Info) syntax.Pos {
	min := syntax.Pos(-1)
	for x := range info.Withs {
		if min < 0 || x.Pos() < min {
			min = x.Pos()
		}
	}
	return min
}

// TestSeveralOperandsAreNesting (check-v1 E5, A4): `with o1, …, on { b }`
// and `with o1 { … with on { b } … }` give the same diagnostics and, when
// clean, the same With facts per operand and the same failure row, over
// generated operand lists of every kind (named, local, inline, context
// value, parenthesised); and the checker's discharges and S102 count equal
// a reference computed from the drawn sets alone. One seed run twice gives
// the same verdicts, and a second seed passes too.
func TestSeveralOperandsAreNesting(t *testing.T) {
	first := runSeveral(t, 1)
	if again := runSeveral(t, 1); again != first {
		t.Errorf("one seed gave two verdicts")
	}
	runSeveral(t, 2)
}
