package check

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The mapped text's seeded property (types.md, Borrows 8; mappedtext.go).
// Each seed writes functions that each open a Mapped m, read its text
// either through a chain of lets (`let t = m.Text()`, then zero to two
// slices of it, each a let) or in place (`m.Text()` at each use), and use
// it at a run of sites: allowed ones (len, an fs.Text argument, an index, a
// slice, a comparison, the range of a for, a capture by a non-escaping
// literal or by tasks.Transform's f, fs.Clone) and refused ones (a
// conversion to string, an interpolation, a `+`, a capture by an escaping
// literal or by task.Go's block). At one point of the run, or none, m is
// handed off: closed, closed on one branch of an if, or passed to a
// function that takes it.
// The lifetime model: a use at a refused site is refused at its line; a
// use after the hand-off is refused at its line as a use of m, "was handed
// off" after a hand-off on every path and "may have been" after one on one
// path. The checker's lines and kinds must be the model's, and no other
// diagnostic may appear.
//
// Replay: go test -run '^TestMappedTextModel$' ./internal/sketch/check -args -mappedtext.seed=N

var mappedTextSeed = flag.Uint64("mappedtext.seed", 0, "run TestMappedTextModel on this seed alone")

const mappedTextPrelude = `import fs
import task
import tasks

func count(t fs.Text) int { len(t) }

func run(f escaping func() int) int { f() }

func each(f func() int) int { f() }

func flag() bool { len("a") > 0 }

func consume(m fs.Mapped) takes m { m.Close() }

func imports() int { task.Go() { 1 }.Wait() + len(tasks.Transform([]int{1}, 1, func(i int) int { i })) }
`

// An mtSite is a statement that uses the text %s once. ok: a text may
// stand there.
type mtSite struct {
	name string
	at   string
	ok   bool
}

var mtSites = []mtSite{
	{name: "len", at: "n += len(%s)", ok: true},
	{name: "arg", at: "n += count(%s)", ok: true},
	{name: "index", at: "n += int(%s[0])", ok: true},
	{name: "slice", at: "n += count(%s[1:])", ok: true},
	{name: "compare", at: "if %s == \"abc\" { n += 1 }", ok: true},
	{name: "range", at: "for _, r in %s { if r == 'a' { n += 1 } }", ok: true},
	{name: "each", at: "n += each(func() int { len(%s) })", ok: true},
	{name: "transform", at: "n += len(tasks.Transform([]int{0, 1}, 2, func(i int) int { len(%s[i:]) }))", ok: true},
	{name: "clone", at: "n += len(fs.Clone(%s))", ok: true},
	{name: "convert", at: "n += len(string(%s))"},
	{name: "interpolate", at: "n += len(\"\\(%s)\")"},
	{name: "concat", at: "n += len(%s + \"a\")"},
	{name: "escaping", at: "n += run(func() int { len(%s) })"},
	{name: "task", at: "n += task.Go() { len(%s) }.Wait()"},
}

// mtCaptures are the sites whose frame may outlive the call or run on
// another task. The in-place form never uses them: there the frame
// captures m itself, a resource, which the resource rules judge (a
// capture moves it into the frame, R12, R31), not this one.
var mtCaptures = map[string]bool{"transform": true, "escaping": true, "task": true}

// mtHandoffs are the statements that hand m off, and whether they do on
// every path.
var mtHandoffs = []struct {
	name   string
	at     string
	always bool
}{
	{name: "close", at: "m.Close()", always: true},
	{name: "take", at: "consume(m)", always: true},
	{name: "branch", at: "if flag() { m.Close() }"},
}

// mtCase writes one function and returns its lines, the model's verdict
// by line (counted from the function's first line), and the names of the
// cases it reaches.
func mtCase(r *rand.Rand, name string) ([]string, map[int]string, []string) {
	lines := []string{fmt.Sprintf("func %s() int {", name), "\tlet m = fs.MapText(\"abc\")", "\tlet mut n = 0"}
	body := func(s string) int {
		lines = append(lines, "\t"+s)
		return len(lines) - 1
	}
	text, form := "m.Text()", "inplace"
	if r.IntN(3) > 0 {
		form = "local"
		body("let t = m.Text()")
		text = "t"
		for k := range r.IntN(3) {
			next := fmt.Sprintf("u%d", k)
			body(fmt.Sprintf("let %s = %s[%d:]", next, text, k%2))
			text = next
		}
	}
	uses := 1 + r.IntN(4)
	handoff := r.IntN(uses + 2) // uses+1: none
	want := map[int]string{}
	var reached []string
	after := ""
	for k := range uses + 1 {
		if k == handoff {
			h := mtHandoffs[r.IntN(len(mtHandoffs))]
			body(h.at)
			after = map[bool]string{true: "after", false: "maybe"}[h.always]
			reached = append(reached, form+" handoff "+h.name)
		}
		if k == uses {
			break
		}
		site := mtSites[r.IntN(len(mtSites))]
		for form == "inplace" && mtCaptures[site.name] {
			site = mtSites[r.IntN(len(mtSites))]
		}
		at := body(fmt.Sprintf(site.at, text))
		var kinds []string
		if !site.ok {
			kinds = append(kinds, "escape")
		}
		if after != "" {
			kinds = append(kinds, after)
		}
		if len(kinds) > 0 {
			want[at] = strings.Join(kinds, "+")
		}
		reached = append(reached, form+" "+site.name+" "+map[bool]string{true: "accepted", false: "refused"}[len(kinds) == 0]+" "+map[bool]string{true: "after", false: "before"}[after != ""])
	}
	body("n")
	lines = append(lines, "}")
	return lines, want, reached
}

// mappedTextProgram is seed's source, the model's verdict by line, and the
// names of the cases it reaches.
func mappedTextProgram(seed uint64) (string, map[int]string, []string) {
	r := rand.New(rand.NewPCG(seed, 0x3a99))
	var b strings.Builder
	b.WriteString(mappedTextPrelude)
	line := strings.Count(mappedTextPrelude, "\n")
	want := map[int]string{}
	var reached []string
	for i := range 2 + r.IntN(4) {
		lines, at, cases := mtCase(r, fmt.Sprintf("f%d", i))
		b.WriteString("\n")
		line++
		for l, kind := range at {
			want[line+1+l] = kind
		}
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		line += len(lines)
		reached = append(reached, cases...)
	}
	b.WriteString("\nfunc main() {}\n")
	return b.String(), want, reached
}

// mtKind is the model's kind of the diagnostic d, or its text when it is
// none of the rule's.
func mtKind(d Diagnostic) string {
	m := d.Message
	switch {
	case d.Code != "S108":
		return d.String()
	case strings.Contains(m, "may have been handed off"):
		return "maybe"
	case strings.Contains(m, "was handed off"):
		return "after"
	case strings.Contains(m, "Borrows 8"):
		return "escape"
	}
	return d.String()
}

// TestMappedTextModel is the property (see above).
func TestMappedTextModel(t *testing.T) {
	seeds := []uint64{}
	for s := range uint64(500) {
		seeds = append(seeds, s+1)
	}
	if *mappedTextSeed != 0 {
		seeds = []uint64{*mappedTextSeed}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		src, want, cases := mappedTextProgram(seed)
		_, diags := CheckSource(src)
		kinds := map[int][]string{}
		for _, d := range diags {
			k := mtKind(d)
			if !slices.Contains(kinds[d.Line], k) {
				kinds[d.Line] = append(kinds[d.Line], k)
			}
		}
		got := map[int]string{}
		for line, ks := range kinds {
			slices.SortFunc(ks, func(a, b string) int {
				order := map[string]int{"escape": 0, "after": 1, "maybe": 1}
				return order[a] - order[b]
			})
			got[line] = strings.Join(ks, "+")
		}
		if !mapsEqual(got, want) {
			t.Errorf("seed %d: the checker says %v, the model %v\nReplay: go test -run '^TestMappedTextModel$' ./internal/sketch/check -args -mappedtext.seed=%d\n%s", seed, got, want, seed, src)
		}
		for _, c := range cases {
			reached[c] = true
		}
	}
	if *mappedTextSeed != 0 {
		return
	}
	// Every site is reached in each form, before and after a hand-off,
	// and every hand-off in each form.
	for _, form := range []string{"local", "inplace"} {
		for _, s := range mtSites {
			if form == "inplace" && mtCaptures[s.name] {
				continue
			}
			for _, when := range []string{"before", "after"} {
				ok := "refused"
				if s.ok && when == "before" {
					ok = "accepted"
				}
				if k := form + " " + s.name + " " + ok + " " + when; !reached[k] {
					t.Errorf("no seed reaches %q", k)
				}
			}
		}
		for _, h := range mtHandoffs {
			if k := form + " handoff " + h.name; !reached[k] {
				t.Errorf("no seed reaches %q", k)
			}
		}
	}
}
