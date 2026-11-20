package sketchfront

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

var contextNeedsSeed = flag.Uint64("ctxneeds.seed", 0, "run TestContextNeedsRowModel at this seed alone")

// TestContextNeedsRowModel is a seeded property of a named context's needs
// row (rows.md, Context 4; check/contextneeds.go; CTX-ROWS-1) against a
// reference model, over the three places a context is built: a with of a
// named context, a with of a named context that embeds it, and a with of
// the same entries inline.
//
// A program has four capabilities C0..C3, each with a provider whose V
// is 2^i, and a Store whose constructor NewMade needs a set N of them. The
// context's header writes H (and the embedding one's H2), the function
// run that builds it writes R, and the test that calls run provides P.
// The model's S102s, by line and capability, are
//
//   - at the entry, N∖H (a named context's build is checked against its
//     header; inline, N∖R, since an inline entry charges run);
//   - at the embedding, H∖H2;
//   - at the with in run, the with's context's header minus R;
//   - at the call in the test, R∖P.
//
// A program the model admits is evaluated: the test passes, since
// Store.Get() is 1 + the sum of N's values read from P's providers, and the
// member law Store.Same runs under a context whose header is empty and is
// skipped, naming the header, under one whose header is not (a law case
// provides nothing from a with site).
//
// So a named context with H = N charges the with what the inline context
// charges run. Mutants: ctxn-with-uncharged, ctxn-embed-uncharged,
// ctxn-header-ignored, ctxn-law-runs-unprovided. A failing seed prints the
// source and its replay command.
func TestContextNeedsRowModel(t *testing.T) {
	seeds := []uint64{*contextNeedsSeed}
	if *contextNeedsSeed == 0 {
		seeds = seeds[:0]
		for s := range uint64(200) {
			seeds = append(seeds, s+1)
		}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		p := genNeedsProgram(rand.New(rand.NewPCG(seed, 0xc7a)))
		src, want := p.source(), p.model()
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nsource:\n%s\nreplay: go test -run TestContextNeedsRowModel ./internal/sketchfront -ctxneeds.seed=%d", seed, fmt.Sprintf(format, args...), src, seed)
		}
		a := Check(src)
		var got []string
		for _, d := range a.Diagnostics {
			// The capability a diagnostic charges follows "requires ".
			cap := ""
			if _, after, ok := strings.Cut(d.Message, "requires "); ok {
				cap, _, _ = strings.Cut(after, " ")
				cap = strings.TrimSuffix(cap, ",")
			}
			got = append(got, fmt.Sprintf("%d %s %s", d.Span.Line, d.Code, cap))
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			var full []string
			for _, d := range a.Diagnostics {
				full = append(full, fmt.Sprintf("%d:%d %s %s", d.Span.Line, d.Span.Column, d.Code, d.Message))
			}
			fail("diagnostics %v, model %v\n%s", got, want, strings.Join(full, "\n"))
		}
		reached[fmt.Sprintf("%s refused=%v", p.kind, len(want) > 0)] = true
		if len(want) > 0 {
			continue
		}
		rep, err := a.Evaluate(eval.DefaultBudget())
		if err != nil {
			fail("%v", err)
		}
		var test, under []string
		for _, r := range rep.Results {
			switch {
			case r.Name == "T":
				test = append(test, string(r.Status))
			case strings.Contains(r.Name, " under "):
				under = append(under, r.Name+": "+string(r.Status)+" "+r.Reason)
			}
		}
		if !slices.Equal(test, []string{"passed"}) {
			fail("test T: %v, want passed; report %+v", test, rep.Results)
		}
		wantUnder := p.lawsUnder()
		if len(under) != len(wantUnder) {
			fail("laws under a context %v, want %v", under, wantUnder)
		}
		for i, w := range wantUnder {
			ok := strings.HasPrefix(under[i], w.prefix)
			for _, word := range w.reason {
				ok = ok && strings.Contains(under[i], word)
			}
			if !ok {
				fail("law under a context %q, want %q… naming %v", under[i], w.prefix, w.reason)
			}
		}
		reached[fmt.Sprintf("%s law skipped=%v", p.kind, len(p.header()) > 0)] = true
	}
	if *contextNeedsSeed == 0 {
		for _, k := range []string{"named", "embedded", "inline"} {
			for _, v := range []string{"refused=true", "refused=false"} {
				if !reached[k+" "+v] {
					t.Errorf("no seed reached %s %s", k, v)
				}
			}
			if k == "inline" {
				continue
			}
			for _, v := range []string{"law skipped=true", "law skipped=false"} {
				if !reached[k+" "+v] {
					t.Errorf("no seed reached %s %s", k, v)
				}
			}
		}
	}
}

type needsProgram struct {
	kind           string // "named", "embedded" or "inline"
	n, h, h2, r, p []int
}

// pick is a random subset of within, in ascending order.
func pick(r *rand.Rand, within []int) []int {
	var out []int
	for _, i := range within {
		if r.IntN(2) == 0 {
			out = append(out, i)
		}
	}
	return out
}

func genNeedsProgram(r *rand.Rand) needsProgram {
	all := []int{0, 1, 2, 3}
	p := needsProgram{kind: []string{"named", "embedded", "inline"}[r.IntN(3)]}
	p.n = pick(r, all)
	// Mostly the header the entries need, so the admitted programs are
	// common; else any set, which may miss some or list more.
	if r.IntN(3) > 0 {
		p.h = slices.Clone(p.n)
	} else {
		p.h = pick(r, all)
	}
	if r.IntN(3) > 0 {
		p.h2 = slices.Clone(p.h)
	} else {
		p.h2 = pick(r, all)
	}
	// run's row and the test's providers: within what the with charges,
	// so neither lists a capability nothing uses, and mostly all of it.
	charged := p.header()
	if p.kind == "inline" {
		charged = p.n
	}
	if r.IntN(3) > 0 {
		p.r = slices.Clone(charged)
	} else {
		p.r = pick(r, charged)
	}
	if r.IntN(3) > 0 {
		p.p = slices.Clone(p.r)
	} else {
		p.p = pick(r, p.r)
	}
	return p
}

// header is the row of the context run's with names.
func (p needsProgram) header() []int {
	if p.kind == "embedded" {
		return p.h2
	}
	return p.h
}

func capRow(caps []int) string {
	var names []string
	for _, i := range caps {
		names = append(names, fmt.Sprintf("C%d", i))
	}
	return strings.Join(names, " | ")
}

func needsClause(caps []int) string {
	if len(caps) == 0 {
		return ""
	}
	return " needs " + capRow(caps)
}

// Lines of the source, for the model.
const (
	needsEntryLine = 20 // NewMade's call in the context's entry
	needsEmbedLine = 22 // Outer's embedding of Live
	needsWithLine  = 25 // the with in run (the entry too, inline)
	needsCallLine  = 30 // run's call in the test
)

func (p needsProgram) source() string {
	var b strings.Builder
	for i := range 4 {
		fmt.Fprintf(&b, "type C%d { V%d() int }\n", i, i)
	}
	b.WriteString("type P {} implements C0, C1, C2, C3\n") // line 5
	for i := range 4 {
		fmt.Fprintf(&b, "func V%d(_ P) int { %d }\n", i, 1<<i)
	}
	b.WriteString("\ntype Store {\n\tGet() int,\n\n\tlaw Same() { Get() == Get() },\n}\n\n") // lines 10..16
	sum := "1"
	for _, i := range p.n {
		sum += fmt.Sprintf(" + C%d.V%d()", i, i)
	}
	fmt.Fprintf(&b, "type Made { x int } implements Store\n")                         // 17
	fmt.Fprintf(&b, "func Get(m Made) int { m.x }\n")                                 // 18
	fmt.Fprintf(&b, "func NewMade() Made%s { Made{x: %s} }\n", needsClause(p.n), sum) // 19
	if p.kind == "inline" {
		b.WriteString("\n\n\n\n")
	} else {
		fmt.Fprintf(&b, "let Live = context%s { Store: NewMade() }\n\n", needsClause(p.h)) // 20
		if p.kind == "embedded" {
			fmt.Fprintf(&b, "let Outer = context%s { Live }\n\n", needsClause(p.h2)) // 22
		} else {
			b.WriteString("\n\n")
		}
	}
	fmt.Fprintf(&b, "func run() int%s {\n", needsClause(p.r)) // 24
	switch p.kind {
	case "named":
		b.WriteString("\twith Live { Store.Get() }\n") // 25
	case "embedded":
		b.WriteString("\twith Outer { Store.Get() }\n")
	case "inline":
		b.WriteString("\twith context { Store: NewMade() } { Store.Get() }\n")
	}
	b.WriteString("}\n\n") // 26, 27
	want := 1
	for _, i := range p.n {
		want += 1 << i
	}
	b.WriteString("test T {\n") // 28
	if len(p.p) == 0 {
		b.WriteString("\tif true {\n")
	} else {
		var keys []string
		for _, i := range p.p {
			keys = append(keys, fmt.Sprintf("C%d", i))
		}
		fmt.Fprintf(&b, "\twith context { %s: P } {\n", strings.Join(keys, ", ")) // 29
	}
	fmt.Fprintf(&b, "\t\tassert run() == %d\n\t}\n}\n", want) // 30
	return b.String()
}

func minus(a, b []int) []int {
	var out []int
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func (p needsProgram) model() []string {
	var out []string
	add := func(line int, caps []int) {
		for _, i := range caps {
			out = append(out, fmt.Sprintf("%d S102 C%d", line, i))
		}
	}
	switch {
	case p.kind == "inline":
		add(needsWithLine, minus(p.n, p.r))
	default:
		add(needsEntryLine, minus(p.n, p.h))
		if p.kind == "embedded" {
			add(needsEmbedLine, minus(p.h, p.h2))
		}
		add(needsWithLine, minus(p.header(), p.r))
	}
	add(needsCallLine, minus(p.r, p.p))
	slices.Sort(out)
	return out
}

// lawsUnder is the law runs under a named context, in the report's order
// (contexts by name): each one's "name: status" and the words its skip's
// reason names (the context and the capabilities of its header).
type lawUnder struct {
	prefix string
	reason []string
}

func (p needsProgram) lawsUnder() []lawUnder {
	run := func(name string, header []int) lawUnder {
		if len(header) == 0 {
			return lawUnder{prefix: "law Store.Same under " + name + ": passed"}
		}
		w := lawUnder{prefix: "law Store.Same under " + name + ": skipped", reason: []string{"context " + name + " needs"}}
		for _, i := range header {
			w.reason = append(w.reason, fmt.Sprintf("C%d", i))
		}
		return w
	}
	switch p.kind {
	case "named":
		return []lawUnder{run("Live", p.h)}
	case "embedded":
		return []lawUnder{run("Live", p.h), run("Outer", p.h2)}
	}
	return nil
}
