package regexpplan

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// prefixAt is an independent reference for a parse's failure position: the
// longest k such that some NFA thread is alive after text[:k], by a plain
// set simulation of the canonical code with no tags.
func prefixAt(p *Plan, text string) int {
	var close func(pc int, set map[int]bool)
	close = func(pc int, set map[int]bool) {
		if set[pc] {
			return
		}
		set[pc] = true
		switch i := p.code[pc]; i.op {
		case splitOp:
			close(i.x, set)
			close(i.y, set)
		case jumpOp, tagOp:
			close(i.x, set)
		}
	}
	live := map[int]bool{}
	close(p.start, live)
	at := 0
	for k := 0; k < len(text); k++ {
		next := map[int]bool{}
		for pc := range live {
			if i := p.code[pc]; i.op == byteOp && i.set[text[k]/64]&(1<<(text[k]%64)) != 0 {
				close(i.x, next)
			}
		}
		if len(next) == 0 {
			return at
		}
		live, at = next, k+1
	}
	return at
}

func shifted(m Match, by int) Match {
	if !m.OK {
		return Match{}
	}
	m.Start, m.End = m.Start+by, m.End+by
	events := slices.Clone(m.Events)
	for i := range events {
		events[i].Pos += by
	}
	m.Events = events
	return m
}

func slotTexts(n *Node, r *rand.Rand) []string {
	return []string{"", witness(n, r), witness(n, r) + "x", "x" + witness(n, r), witness(n, r) + witness(n, r), "abacaba"}
}

// TestSlotDriverAgrees: the typed parse (with At) and the linear find give
// the reference's match on generated patterns, and their emitted Go
// typechecks. A fixed plan's deterministic parse emits no allocation.
func TestSlotDriverAgrees(t *testing.T) {
	r := rand.New(rand.NewSource(40417))
	fixed, repeated := 0, 0
	for trial := 0; trial < 300; trial++ {
		id := 0
		n := generated(r, 3, &id)
		parse, err := Compile(n, Parse, DefaultLimits())
		if err != nil {
			continue
		}
		find, err := Compile(n, Find, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if parse.Fixed() {
			fixed++
		} else {
			repeated++
		}
		for _, text := range slotTexts(n, r) {
			want := parse.Reference(text)
			got := parse.ParseResult(text)
			if got.OK != want.OK || got.OK && (got.End != want.End || !slices.Equal(got.Events, want.Events)) {
				t.Fatalf("pattern %s text %q: parse %+v, reference %+v", pattern(n), text, got, want)
			}
			if !got.OK && got.At != prefixAt(parse, text) {
				t.Fatalf("pattern %s text %q: At %d, reference %d", pattern(n), text, got.At, prefixAt(parse, text))
			}
			for from := 0; from <= len(text); from++ {
				m, _ := find.FindFrom(text, from)
				if ref := shifted(find.Reference(text[from:]), from); !sameMatch(m, ref) {
					t.Fatalf("pattern %s text %q from %d: find %+v, reference %+v", pattern(n), text, from, m, ref)
				}
			}
		}
		if trial < 40 {
			engines := []Engine{NFA}
			if parse.ir != nil {
				engines = append(engines, parse.info.Engine)
			}
			for _, engine := range engines {
				src, err := parse.EmitParse("parseIt", engine)
				if err != nil {
					t.Fatal(err)
				}
				if engine != NFA && parse.Fixed() {
					for _, site := range []string{"make(", "append(", "new(", "&"} {
						if strings.Contains(src, site) {
							t.Fatalf("fixed deterministic parse has the allocation site %s:\n%s", site, src)
						}
					}
				}
				typecheck(t, "package pattern\n"+src)
			}
			src, err := find.EmitFind("findIt")
			if err != nil {
				t.Fatal(err)
			}
			typecheck(t, "package pattern\n"+src)
		}
	}
	if fixed == 0 || repeated == 0 {
		t.Fatalf("generator reached fixed %d and repeated %d plans", fixed, repeated)
	}
}

// TestFindIsLinear: on failing long prefixes, the find's counted work per
// byte stays under a constant of the pattern (Lowering 4).
func TestFindIsLinear(t *testing.T) {
	// a+b and (?<X>a|aa)+c: every start before the end is a live prefix.
	plans := []*Node{
		{Kind: Concat, Children: []*Node{{Kind: Repeat, Min: 1, Max: -1, Children: []*Node{Byte('a')}}, Byte('b')}},
		{Kind: Concat, Children: []*Node{{Kind: Repeat, Min: 1, Max: -1, Children: []*Node{{Kind: Capture, Name: "X", Children: []*Node{{Kind: Alternative, Children: []*Node{Byte('a'), {Kind: Concat, Children: []*Node{Byte('a'), Byte('a')}}}}}}}}, Byte('c')}},
	}
	for _, n := range plans {
		p, err := Compile(n, Find, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		bound := 2*len(p.code) + 2
		for size := 8; size <= 128; size *= 2 {
			m, work := p.FindFrom(strings.Repeat("a", size), 0)
			if m.OK {
				t.Fatalf("%s matched a failing text", pattern(n))
			}
			if work > bound*(size+1) {
				t.Fatalf("%s: work %d on %d bytes exceeds %d per byte", pattern(n), work, size, bound)
			}
		}
	}
}

// TestEmittedSlotDriversAgree builds Go because it executes the emitted
// parse and find drivers (their closures, slot folds and history storage),
// which the in-process drivers above do not run. All cases share one binary.
func TestEmittedSlotDriversAgree(t *testing.T) {
	needsBuild(t)
	r := rand.New(rand.NewSource(91877))
	var source, checks strings.Builder
	source.WriteString("package main\nimport \"fmt\"\n")
	add := func(n *Node, trial int) {
		parse, err := Compile(n, Parse, DefaultLimits())
		if err != nil {
			return
		}
		find, _ := Compile(n, Find, DefaultLimits())
		engines := []Engine{NFA}
		if parse.ir != nil {
			engines = append(engines, parse.info.Engine)
		}
		texts := slotTexts(n, r)
		expect := func(ok bool, start, end int, events []Event) string {
			if !ok {
				// A failure is the header alone: At (or 0 for a find).
				failed := []int{0, start, 0}
				if parse.Fixed() {
					failed = make([]int, parse.Slots())
					failed[1] = start
				}
				return fmt.Sprint(failed)
			}
			if parse.Fixed() {
				slots := parse.Fold(events)
				slots[0], slots[1], slots[2] = 1, start, end
				return fmt.Sprint(slots)
			}
			flat := []int{0, start, end}
			if ok {
				flat[0] = 1
			}
			for _, e := range events {
				flat = append(flat, e.Tag, e.Pos)
			}
			return fmt.Sprint(flat)
		}
		for _, engine := range engines {
			name := fmt.Sprintf("parse_%d_%s", trial, engine)
			src, err := parse.EmitParse(name, engine)
			if err != nil {
				t.Fatal(err)
			}
			source.WriteString(src + "\n")
			for _, text := range texts {
				res := parse.ParseResult(text)
				want := expect(res.OK, res.At, res.End, res.Events)
				fmt.Fprintf(&checks, "if got := fmt.Sprint(%s(%q)); got != %q {panic(fmt.Sprintf(\"%%s %%q: %%s want %%s\", %q, %q, got, %q))}\n", name, text, want, pattern(n), text, want)
			}
		}
		name := fmt.Sprintf("find_%d", trial)
		src, err := find.EmitFind(name)
		if err != nil {
			t.Fatal(err)
		}
		source.WriteString(src + "\n")
		for _, text := range texts {
			for _, from := range []int{0, len(text) / 2} {
				m := shifted(find.Reference(text[from:]), from)
				want := expect(m.OK, m.Start, m.End, m.Events)
				fmt.Fprintf(&checks, "if got := fmt.Sprint(%s(%q, %d)); got != %q {panic(fmt.Sprintf(\"find %%s %%q: %%s want %%s\", %q, %q, got, %q))}\n", name, text, from, want, pattern(n), text, want)
			}
		}
	}
	for trial := 0; trial < 30; trial++ {
		id := 0
		add(generated(r, 3, &id), trial)
	}
	source.WriteString("func main(){\n" + checks.String() + "}\n")
	oneBinary(t, source.String())
}
