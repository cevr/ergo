package derive

import (
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/regexpplan"
	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// genPattern writes a random pattern of the byte grammar that Go's regexp
// reads with the same meaning: no nullable repeat body, groups written
// ( … ), captures (?<Gn> … ).
func genPattern(r *rand.Rand, depth int, id *int) string {
	leaves := []string{"a", "b", "c", "[ab]", "[^a]", ".", `\x62`, "[a-b]", `\.`}
	if depth == 0 {
		return leaves[r.Intn(len(leaves))]
	}
	switch r.Intn(6) {
	case 0:
		return genPattern(r, depth-1, id) + genPattern(r, depth-1, id)
	case 1:
		return "(" + genPattern(r, depth-1, id) + "|" + genPattern(r, depth-1, id) + ")"
	case 2, 3:
		*id++
		n := *id
		return fmt.Sprintf("(?<G%d>%s)", n, genPattern(r, depth-1, id))
	default:
		body := leaves[r.Intn(len(leaves))]
		if r.Intn(2) == 0 {
			body += genPattern(r, depth-1, id)
		}
		quants := []string{"*", "+", "?", "{1,2}", "{2}", "{0,3}", "{1,}"}
		return "(" + body + ")" + quants[r.Intn(len(quants))]
	}
}

func genText(r *rand.Rand) string {
	alphabet := "abc.\xff"
	n := r.Intn(7)
	var b strings.Builder
	for range n {
		b.WriteByte(alphabet[r.Intn(len(alphabet))])
	}
	return b.String()
}

// TestPatternGrammar: generated patterns mean what Go's regexp means on
// the same bytes (Pattern grammar 2, 3 and 6): a parse accepts exactly the
// texts Go matches whole, a find returns Go's leftmost-first span, and a
// capture that takes part at most once has Go's span. The refused forms
// are refused at their byte with the form to write (rule 4 and 5).
func TestPatternGrammar(t *testing.T) {
	r := rand.New(rand.NewSource(20261111))
	for trial := 0; trial < 400; trial++ {
		id := 0
		text := genPattern(r, 3, &id)
		pt, perr := parsePattern(text)
		if perr != nil {
			t.Fatalf("pattern %q refused: %v", text, perr)
		}
		parse, err := regexpplan.Compile(pt.root, regexpplan.Parse, regexpplan.DefaultLimits())
		if err != nil {
			continue // a nullable body under a repeat: rule 5, rows below
		}
		find, _ := regexpplan.Compile(pt.root, regexpplan.Find, regexpplan.DefaultLimits())
		whole := regexp.MustCompile(`^(?:` + text + `)$`)
		left := regexp.MustCompile(text)
		for k := 0; k < 12; k++ {
			s := genText(r)
			got := parse.ParseResult(s)
			if got.OK != whole.MatchString(s) {
				t.Fatalf("seed 20261111 trial %d: pattern %q text %q: parse %v, Go %v", trial, text, s, got.OK, !got.OK)
			}
			m, _ := find.FindFrom(s, 0)
			want := left.FindStringSubmatchIndex(s)
			if m.OK != (want != nil) || m.OK && (m.Start != want[0] || m.End != want[1]) {
				t.Fatalf("seed 20261111 trial %d: pattern %q text %q: find %v %d..%d, Go %v", trial, text, s, m.OK, m.Start, m.End, want)
			}
			if !m.OK || !find.Fixed() {
				continue
			}
			slots := find.Fold(m.Events)
			for i, name := range find.Names() {
				g := left.SubexpIndex(name)
				if slots[3+2*i] != want[2*g] || slots[4+2*i] != want[2*g+1] {
					t.Fatalf("trial %d: pattern %q text %q: capture %s %d..%d, Go %d..%d", trial, text, s, name, slots[3+2*i], slots[4+2*i], want[2*g], want[2*g+1])
				}
			}
		}
	}
	for _, row := range []struct {
		text string
		at   int
		msg  string
	}{
		{`a*?`, 2, "lazy repeat"},
		{`a++`, 2, "possessive repeat"},
		{`(a)\1`, 3, "backreference"},
		{`\k<x>`, 0, "backreference"},
		{`(?=a)`, 0, "lookaround"},
		{`(?<!a)b`, 0, "lookaround"},
		{`(?i)a`, 0, "flag group"},
		{`(?:a)`, 0, "write ( … ): a group captures only with a name"},
		{`(?P<x>a)`, 0, "the P form"},
		{`^a`, 0, "drop ^"},
		{`a$`, 1, "drop $"},
		{`\bA`, 0, "drop \\b"},
		{`\p{L}`, 0, "Unicode class"},
		{`[[:alpha:]]`, 1, "POSIX class"},
		{`\q`, 0, "not an escape"},
		{"a\xc3", 1, "write the byte as \\xC3"},
		{`[b-a]`, 2, "low end above its high end"},
		{`[^\x00-\xff]`, 0, "holds no byte"},
		{`a{3,2}`, 1, "bounds reversed"},
		{`a{1001}`, 2, "at most 1000"},
		{`(?<1x>a)`, 3, "not an identifier"},
		{`(?<X>a)(?<X>b)`, 10, "named twice"},
		{`((?<K>a)=(?<V>b);)*`, 18, "name the repeated group"},
		{`((?<K>a)(?<V>b))+`, 16, "name the repeated group"},
		{`((?<K>a)(?<V>b)){0,2}`, 16, "name the repeated group"},
		{`(?<A>a)|b`, 8, "every branch of T is a named capture"},
		{`(a`, 0, "unclosed group"},
		{`a)`, 1, "unmatched )"},
		{`[ab`, 0, "unclosed class"},
		{`{`, 0, "write \\{"},
		{`*a`, 0, "nothing to repeat"},
	} {
		pt, perr := parsePattern(row.text)
		if perr == nil {
			_, perr = shapeOf("T", pt)
		}
		if perr == nil || perr.Offset != row.at || !strings.Contains(perr.Msg, row.msg) {
			t.Errorf("pattern %q: got %v, want byte %d %q", row.text, perr, row.at, row.msg)
		}
	}
	// Capture shapes 4 refuses only a repeat that can run more than once:
	// `?` and a count whose maximum is one keep two captures of one scope,
	// and a named repeated group is admitted.
	for _, text := range []string{`((?<K>a)(?<V>b))?`, `((?<K>a)(?<V>b)){0,1}`, `(?<Pair>(?<K>a)=(?<V>b);)*`} {
		pt, perr := parsePattern(text)
		if perr == nil {
			_, perr = shapeOf("T", pt)
		}
		if perr != nil {
			t.Errorf("pattern %q: refused %v", text, perr)
		}
	}
}

// participationOf counts, in one match's events, the occurrences of each
// capture inside each occurrence of its scope (the parent capture's
// occurrence or the whole match): the reference that the derived counts
// must bound.
func participationOf(names []string, parent map[string]string, events []regexpplan.Event) map[string][]int {
	counts := map[string][]int{}
	open := map[string]int{} // a scope's current occurrence count per child
	scopeKids := map[string][]string{}
	for _, n := range names {
		scopeKids[parent[n]] = append(scopeKids[parent[n]], n)
	}
	flush := func(scope string) {
		for _, k := range scopeKids[scope] {
			counts[k] = append(counts[k], open[k])
			open[k] = 0
		}
	}
	for _, e := range events {
		name := names[abs(e.Tag)-1]
		if e.Tag > 0 {
			open[name]++
			for _, k := range scopeKids[name] {
				open[k] = 0
			}
		} else {
			flush(name)
		}
	}
	flush("")
	return counts
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// TestPatternShapes: the derived count of each capture bounds what every
// generated match shows (Capture shapes 2): One is exactly once in each
// scope occurrence, Opt at most once; and the generator reaches captures
// that are absent and captures that repeat.
func TestPatternShapes(t *testing.T) {
	r := rand.New(rand.NewSource(77001))
	absent, repeatedSeen, unions := 0, 0, 0
	for trial := 0; trial < 400; trial++ {
		id := 0
		text := genPattern(r, 3, &id)
		if trial%5 == 0 {
			text = fmt.Sprintf("(?<A%d>%s)|(?<B%d>%s)", trial, genPattern(r, 2, &id), trial, genPattern(r, 2, &id))
		}
		pt, perr := parsePattern(text)
		if perr != nil {
			t.Fatalf("pattern %q: %v", text, perr)
		}
		shape, serr := shapeOf("T", pt)
		if serr != nil {
			continue // rule 4: a repeated group of two captures
		}
		parse, err := regexpplan.Compile(pt.root, regexpplan.Parse, regexpplan.DefaultLimits())
		if err != nil {
			continue
		}
		counts := map[string]Count{}
		parent := map[string]string{}
		var visit func(owner string, fs []*Field)
		visit = func(owner string, fs []*Field) {
			for _, f := range fs {
				counts[f.Name], parent[f.Name] = f.Count, owner
				if f.Record != nil {
					visit(f.Name, f.Record.Fields)
				}
			}
		}
		if shape.Union {
			unions++
			visit("", shape.Variants)
		} else {
			visit("", shape.Record.Fields)
		}
		if shape.Repeated == parse.Fixed() {
			t.Fatalf("pattern %q: shape repeated %v, plan fixed %v", text, shape.Repeated, parse.Fixed())
		}
		names := parse.Names()
		for k := 0; k < 30; k++ {
			s := genText(r)
			m := parse.Reference(s)
			if !m.OK {
				continue
			}
			for name, seen := range participationOf(names, parent, m.Events) {
				for _, n := range seen {
					c := counts[name]
					if shape.Union && parent[name] == "" {
						c = Opt // a variant takes part in the matches of its branch only
					}
					if c == One && n != 1 || c == Opt && n > 1 {
						t.Fatalf("seed 77001 trial %d: pattern %q text %q: capture %s derived %d, took part %d times", trial, text, s, name, c, n)
					}
					if n == 0 {
						absent++
					}
					if n > 1 {
						repeatedSeen++
					}
				}
			}
		}
	}
	if absent == 0 || repeatedSeen == 0 || unions == 0 {
		t.Fatalf("the generator reached %d absent, %d repeated, %d union shapes", absent, repeatedSeen, unions)
	}
}
