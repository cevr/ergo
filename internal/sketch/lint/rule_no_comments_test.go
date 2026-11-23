package lint_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
)

// commentPlant writes a generated source whose comments each hold a
// marker c<N>, and records which markers the model keeps: the doc
// comment of a public declaration, nothing else.
type commentPlant struct {
	r    *rand.Rand
	b    strings.Builder
	n    int
	kept []string
	gone []string
}

// group writes a comment group of one or two lines at indent, kept or
// not, and returns its marker.
func (p *commentPlant) group(indent string, kept bool) {
	p.n++
	m := fmt.Sprintf("c%d", p.n)
	if p.r.IntN(4) == 0 {
		fmt.Fprintf(&p.b, "%s// %s says\n%s// more of it\n", indent, m, indent)
	} else {
		fmt.Fprintf(&p.b, "%s// %s says\n", indent, m)
	}
	if kept {
		p.kept = append(p.kept, m)
	} else {
		p.gone = append(p.gone, m)
	}
}

// marker is a trailing or inline comment's text, never kept.
func (p *commentPlant) marker(block bool) string {
	p.n++
	m := fmt.Sprintf("c%d", p.n)
	p.gone = append(p.gone, m)
	if block {
		return "/* " + m + " */"
	}
	return "// " + m
}

// doc writes what stands above a declaration: nothing, its doc comment
// (kept when public), a group a blank line above it (never kept), or both.
func (p *commentPlant) doc(indent string, public bool) {
	switch p.r.IntN(5) {
	case 0, 1:
		p.group(indent, public)
	case 2:
		p.group(indent, false)
		p.b.WriteString("\n")
	case 3:
		p.group(indent, false)
		p.b.WriteString("\n")
		p.group(indent, public)
	}
}

func (p *commentPlant) name(public bool, base string, i int) string {
	if public {
		return fmt.Sprintf("%s%d", strings.ToUpper(base[:1])+base[1:], i)
	}
	return fmt.Sprintf("%s%d", base, i)
}

// decl writes one top-level declaration of a random kind.
func (p *commentPlant) decl(i int) {
	public := p.r.IntN(2) == 0
	switch p.r.IntN(6) {
	case 0:
		p.doc("", public)
		fmt.Fprintf(&p.b, "func %s(a int) int {\n", p.name(public, "run", i))
		if p.r.IntN(2) == 0 {
			p.b.WriteString("\n")
		}
		for j := range 1 + p.r.IntN(3) {
			if p.r.IntN(2) == 0 {
				p.group("\t", false)
			}
			line := fmt.Sprintf("\tlet x%d = a + %d", j, j)
			switch p.r.IntN(4) {
			case 0:
				line += " " + p.marker(false)
			case 1:
				line = fmt.Sprintf("\tlet x%d = %s a + %d", j, p.marker(true), j)
			}
			p.b.WriteString(line + "\n")
			if p.r.IntN(4) == 0 {
				p.b.WriteString("\n")
			}
		}
		if p.r.IntN(3) == 0 {
			p.group("\t", false)
		}
		p.b.WriteString("\ta\n}\n")
	case 1:
		p.doc("", public)
		fmt.Fprintf(&p.b, "type %s {\n", p.name(public, "rec", i))
		for j := range 1 + p.r.IntN(3) {
			field := p.r.IntN(2) == 0
			p.doc("\t", public && field)
			fmt.Fprintf(&p.b, "\t%s int,\n", p.name(field, "f", j))
		}
		p.b.WriteString("}\n")
	case 2:
		p.doc("", public)
		fmt.Fprintf(&p.b, "type %s {\n", p.name(public, "ops", i))
		op := p.r.IntN(2) == 0
		p.doc("\t", public && op)
		fmt.Fprintf(&p.b, "\t%s() int,\n", p.name(op, "get", 0))
		if p.r.IntN(2) == 0 {
			p.b.WriteString("\n")
			p.doc("\t", public)
			p.b.WriteString("\tlaw Holds(n int) {\n\t\tn == n\n\t}\n")
		}
		p.b.WriteString("}\n")
	case 3:
		p.doc("", public)
		fmt.Fprintf(&p.b, "let %s = %d\n", p.name(public, "limit", i), i)
	case 4:
		p.doc("", public)
		fmt.Fprintf(&p.b, "law %s(n int) {\n\tn == n\n}\n", p.name(public, "same", i))
	case 5:
		p.doc("", false)
		fmt.Fprintf(&p.b, "test %s {\n}\n", p.name(true, "case", i))
	}
}

// plantComments is a formatted source of generated declarations with
// comments in every position, and the markers the model keeps and drops.
func plantComments(t *testing.T, seed uint64) (string, []string, []string) {
	t.Helper()
	p := &commentPlant{r: rand.New(rand.NewPCG(seed, 0xc0))}
	if p.r.IntN(2) == 0 {
		p.group("", false)
		p.b.WriteString("\n")
	}
	for i := range 2 + p.r.IntN(5) {
		if i > 0 {
			p.b.WriteString("\n")
			if p.r.IntN(6) == 0 {
				p.group("", false)
				p.b.WriteString("\n")
			}
		}
		p.decl(i)
	}
	if p.r.IntN(4) == 0 {
		p.b.WriteString("\n")
		p.group("", false)
	}
	src, err := hostsyntax.Source(p.b.String())
	if err != nil {
		t.Fatalf("seed %d: the generated source does not format: %v\n%s", seed, err, numbered(p.b.String()))
	}
	return src, p.kept, p.gone
}

// markers is the c<N> marker of each comment group of src, in order.
func markers(t *testing.T, src string) []string {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, numbered(src))
	}
	var out []string
	for _, g := range f.Comments {
		for _, c := range g.List {
			if w := strings.Fields(strings.Trim(c.Text, "/* ")); len(w) > 0 && w[0][0] == 'c' {
				out = append(out, w[0])
			}
		}
	}
	return out
}

// TestNoCommentsAgreesWithModel plants comments in every position of
// generated declarations, public and private, and holds the rule and its
// fix to the model: no-comments reports exactly the groups the model
// drops, at their lines; the fix keeps exactly the model's doc comments
// in order (law 5) and leaves nothing to report (law 1); a second fix
// changes nothing (law 2); the tokens are the source's (law 3, which the
// fix guards); and the result is a formatter fixpoint (law 4). Mutants:
// nocom-skip-trailing, nocom-skip-body, nocom-private-doc-kept,
// nocom-member-of-private-kept, nocom-public-doc-dropped,
// nocom-test-doc-kept, nocom-blank-pair-left, nocom-open-blank-left,
// nocom-guard-off.
func TestNoCommentsAgreesWithModel(t *testing.T) {
	for _, seed := range propertySeeds(60) {
		src, kept, gone := plantComments(t, seed)
		replay := fmt.Sprintf("replay: go test ./internal/sketch/lint -run TestNoCommentsAgreesWithModel -lint.seed=%d", seed)
		var want []int
		for i, line := range strings.Split(src, "\n") {
			for _, m := range gone {
				if strings.Contains(line, " "+m+" ") && !strings.Contains(line, "more of it") {
					want = append(want, i+1)
				}
			}
		}
		slices.Sort(want)
		want = slices.Compact(want)
		got := linesOf(t, "no-comments", syntaxInput(t, "p/main.ergo", src))
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: no-comments reported lines %v, the model drops the groups on %v\n%s\n%s", seed, got, want, numbered(src), replay)
		}
		out, removed, err := fix.Comments(src, "p/main.ergo")
		if err != nil {
			t.Fatalf("seed %d: %v\n%s\n%s", seed, err, numbered(src), replay)
		}
		if have := markers(t, out); !slices.Equal(have, kept) {
			t.Fatalf("seed %d: the fix kept %v, the model keeps %v\n%s\n%s", seed, have, kept, numbered(out), replay)
		}
		if len(removed) != len(want) {
			t.Fatalf("seed %d: the fix listed %d removed groups, the rule reported %d\n%s", seed, len(removed), len(want), replay)
		}
		f, _ := syntax.Parse(out)
		if b := lint.BannedComments(f); len(b) > 0 {
			t.Fatalf("seed %d: after the fix the rule still reports %d groups\n%s\n%s", seed, len(b), numbered(out), replay)
		}
		again, more, err := fix.Comments(out, "p/main.ergo")
		if err != nil || again != out || len(more) > 0 {
			t.Fatalf("seed %d: a second fix changed the source (%v)\n%s\n%s", seed, err, numbered(out), replay)
		}
		if syntax.TokenText(out) != syntax.TokenText(src) {
			t.Fatalf("seed %d: the fix changed the tokens\n%s\n%s", seed, numbered(out), replay)
		}
		if formatted, err := hostsyntax.Source(out); err != nil || formatted != out {
			t.Fatalf("seed %d: the fix leaves no formatter fixpoint (%v)\n%s\n%s", seed, err, numbered(out), replay)
		}
		raw := applyRaw(t, src)
		if formatted, err := hostsyntax.Source(raw); err != nil || formatted != raw {
			t.Fatalf("seed %d: the removal edits alone leave no formatter fixpoint (%v)\n%s\n%s", seed, err, numbered(raw), replay)
		}
	}
}

// applyRaw is src with the rule's own edits applied, as ergo fix --lint
// applies them, without the formatter: the edits keep the layout.
func applyRaw(t *testing.T, src string) string {
	t.Helper()
	for range len(src) {
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("the edited source does not parse: %v\n%s", err, numbered(src))
		}
		banned := lint.BannedComments(f)
		if len(banned) == 0 {
			return src
		}
		b := banned[len(banned)-1]
		src = src[:b.From] + b.Text + src[b.To:]
	}
	return src
}
