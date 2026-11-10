package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// localPrelude declares what the local-context rules name: three
// capabilities, providers for them, a function that requires Store, a
// context-value type (runtime.md, G1) and two package contexts.
const localPrelude = `type Greeter {
	Greet(name string) string,
}

type Store {
	Get() string,
}

type Timer {
	Now() int,
}

type Plain {}
type Loud {}
type Holder { v string }
type Fixed {}
type Tag { s string }
type Box { w int }

func Greet(_ Plain, name string) string {
	name
}

func Greet(_ Loud, name string) string {
	name
}

func Get(h Holder) string {
	h.v
}

func Now(_ Fixed) int {
	1
}

func read() string needs Store {
	Store.Get()
}

func contextKey(t Tag) string {
	"tag"
}

func contextText(t Tag) string {
	t.s
}

func use(n int) int {
	n
}

let Live = context { Greeter: Plain }

let Stored = context { Store: Holder{v: "pkg"} }

`

// localRule is one program of TestLocalContextRules: its body follows
// localPrelude, and its `// want CODE` lines are its exact diagnostics.
type localRule struct {
	name, body string
}

// localRules are the rules of Design A2 (check-v1 E5 spec): each S108 form
// once in each body kind, shadowing, embedding, the charge site inside a
// function literal, every ordered pair of operand kinds and the refusals
// that stay.
func localRules() []localRule {
	rules := []localRule{
		// The planner's probes, with their verdicts after E5.
		{"probe-local", "func main() needs Console {\n\tlet quiet = context { Greeter: Plain }\n\tConsole.Println(with quiet { Greeter.Greet(\"ada\") })\n}\n"},
		{"probe-several", "func main() needs Console {\n\twith Stored, context { Greeter: Plain, Store: Holder{v: read()} } {\n\t\tConsole.Println(\"\\(Greeter.Greet(\"ada\")) \\(Store.Get())\")\n\t}\n}\n"},
		{"probe-escape", "func main() needs Console {\n\tlet w = context { Greeter: Plain }\n\tlet v = w // want S108\n\tlet mut m = context { Greeter: Plain } // want S108\n\tConsole.Println(with context { w } { Greeter.Greet(\"ada\") })\n\tConsole.Println(with m { Greeter.Greet(\"bo\") })\n\t_ = v\n}\n"},
		{"probe-inline", "func main() needs Console {\n\tConsole.Println(with context { Greeter: Plain } { Greeter.Greet(\"ada\") })\n\tConsole.Println(with (Live) { Greeter.Greet(\"bo\") })\n}\n"},

		// The other placements of a context.
		{"let-typed", "func f() string {\n\tlet w Greeter = context { Greeter: Plain } // want S108\n\tw.Greet(\"a\")\n}\n"},
		{"assign", "func f() {\n\tlet w = context { Greeter: Plain }\n\tw = 1 // want S108\n\twith w { Greeter.Greet(\"a\") }\n}\n"},
		{"literal-argument", "func f() int {\n\tuse(context { Greeter: Plain }) // want S108\n}\n"},
		{"literal-tuple", "func f() int {\n\tlet (a, b) = (context { Greeter: Plain }, 1) // want S108\n\t_ = a\n\tb\n}\n"},
		{"literal-provider", "func f() string {\n\tlet w = context { Greeter: context { Greeter: Plain } } // want S108\n\twith w { Greeter.Greet(\"a\") }\n}\n"},
		{"literal-blank", "func f() {\n\tlet _ = context { Greeter: Plain } // want S108\n}\n"},

		// Shadowing (D-D2): a local named like a package context wins in its
		// scope; one named like an enclosing local is S108.
		{"shadow-package", "func f() string {\n\tlet Live = context { Store: Holder{v: \"x\"} }\n\twith Live { Store.Get() }\n}\n"},
		{"shadow-local", "func f(b bool) string {\n\tlet w = context { Greeter: Plain }\n\tif b {\n\t\tlet w = context { Greeter: Loud } // want S108\n\t\t_ = with w { Greeter.Greet(\"a\") }\n\t}\n\twith w { Greeter.Greet(\"b\") }\n}\n"},
		{"shadow-same-block", "func f() string {\n\tlet w = context { Greeter: Plain }\n\tlet w = context { Greeter: Loud } // want S108\n\twith w { Greeter.Greet(\"b\") }\n}\n"},
		{"sibling-blocks", "func f(b bool) string {\n\tif b {\n\t\tlet w = context { Greeter: Plain }\n\t\twith w { Greeter.Greet(\"a\") }\n\t} else {\n\t\tlet w = context { Store: Holder{v: \"x\"} }\n\t\twith context { w } { Store.Get() }\n\t}\n}\n"},

		// Embedding.
		{"embed-in-inline", "func f() string {\n\tlet w = context { Greeter: Plain }\n\twith context { Store: Holder{v: \"x\"}, w } { Greeter.Greet(Store.Get()) }\n}\n"},
		{"embed-in-local", "func f() string {\n\tlet w = context { Greeter: Plain }\n\tlet v = context { Store: Holder{v: \"x\"}, w }\n\twith v { Greeter.Greet(Store.Get()) }\n}\n"},
		{"embed-chain", "func f() string {\n\tlet a = context { Greeter: Plain }\n\tlet b = context { a }\n\tlet c = context { replace Greeter: Loud, b, Live }\n\twith c { Greeter.Greet(\"x\") }\n}\n"},
		{"embed-duplicate", "func f() string {\n\tlet w = context { Greeter: Plain }\n\twith context { Greeter: Loud, w } { Greeter.Greet(\"x\") } // want S111\n}\n"},
		{"embed-replace-unbound", "func f() string {\n\tlet w = context { Greeter: Plain }\n\twith context { replace Store: Holder{v: \"x\"}, w } { Store.Get() } // want S108\n}\n"},
		{"embed-two-locals-collide", "func f() string {\n\tlet a = context { Greeter: Plain }\n\tlet b = context { Greeter: Loud }\n\twith context { a, b } { Greeter.Greet(\"x\") } // want S111\n}\n"},
		{"embed-local-needs", "func f() string {\n\tlet w = context { Store: Holder{v: read()} }\n\twith context { Greeter: Plain, w } { Store.Get() } // want S102\n}\n\nfunc g() string needs Store {\n\tlet w = context { Store: Holder{v: read()} }\n\tlet v = context { Greeter: Plain, w }\n\twith v { Store.Get() }\n}\n"},
		{"embed-own-literal", "func f() string {\n\tlet w = context { w, Greeter: Plain } // want S104\n\twith w { Greeter.Greet(\"x\") }\n}\n"},
		{"package-cannot-embed-local", "func f() string {\n\tlet w = context { Greeter: Plain }\n\twith w { Greeter.Greet(\"x\") }\n}\n\nlet P = context { w } // want S104\n"},

		// The charge site (C34): needs at each with, failures to its function.
		{"charge-written", "func f() string needs Store {\n\tlet w = context { Greeter: Loud, Store: Holder{v: read()} }\n\twith w { Greeter.Greet(\"x\") }\n}\n"},
		{"charge-inferred", "func f() string {\n\tlet w = context { Store: Holder{v: read()} }\n\twith w { Store.Get() } // want S102\n}\n"},
		{"charge-enclosing-with", "func f() string {\n\tlet w = context { Greeter: Loud, Store: Holder{v: read()} }\n\twith Stored {\n\t\twith w { Greeter.Greet(\"x\") }\n\t}\n}\n"},
		{"charge-let-inside-with", "func f() string {\n\twith Stored {\n\t\tlet w = context { Greeter: Loud, Store: Holder{v: read()} }\n\t\tlet g = func() string {\n\t\t\twith w { Greeter.Greet(\"x\") } // want S102\n\t\t}\n\t\tg()\n\t}\n}\n"},
		{"charge-each-with", "func f() string needs Store {\n\tlet w = context { Store: Holder{v: read()} }\n\tlet a = with w { Store.Get() }\n\tlet b = with w { Store.Get() }\n\ta + b\n}\n\nfunc g() string {\n\tlet w = context { Store: Holder{v: read()} }\n\twith Stored {\n\t\tlet a = with w { Store.Get() }\n\t\t_ = a\n\t}\n\twith w { Store.Get() } // want S102\n}\n"},
		{"charge-literal-row", "func f() int {\n\tlet w = context { Store: Holder{v: read()} }\n\tlet g = func() string needs Store {\n\t\twith w { Store.Get() }\n\t}\n\t_ = g\n\t1\n}\n\nfunc h() escaping func() string {\n\tlet w = context { Store: Holder{v: read()} }\n\tfunc() string {\n\t\twith w { Store.Get() } // want S102\n\t}\n}\n"},

		// The header and a constructor entry stay refused (#196 stage b, #37).
		{"local-header", "func f() string {\n\tlet w = context provides Greeter { Greeter: Plain } // want S109\n\twith w { Greeter.Greet(\"x\") }\n}\n"},
		{"local-constructor", "func mk() Plain {\n\tPlain\n}\n\nfunc f() string {\n\tlet w = context { Greeter: mk } // want S109\n\twith w { Greeter.Greet(\"x\") }\n}\n"},
	}
	// Each S108 use of a local context, once in each body kind: a function,
	// a function literal, a test body and a law body (Design A2).
	uses := []struct{ name, stmt string }{
		{"argument", "_ = use(w) // want S108"},
		{"let", "let u = w // want S108\n\t_ = u"},
		{"field", "_ = Box{w: w} // want S108"},
		{"element", "_ = []int{w} // want S108"},
		{"operand", "_ = w + 1 // want S108"},
		{"selector", "_ = w.x // want S108"},
		{"call", "_ = w() // want S108"},
		{"interpolation", "_ = \"\\(w)\" // want S108"},
		{"capture", "let g = func() int { w } // want S108\n\t_ = g"},
		{"paren", "_ = use((w)) // want S108"},
		{"index", "let xs = []int{1}\n\t_ = xs[w] // want S108"},
	}
	for _, u := range uses {
		body := "\tlet w = context { Greeter: Plain }\n\t" + u.stmt + "\n\t_ = with w { Greeter.Greet(\"x\") }\n"
		rules = append(rules,
			localRule{"func-" + u.name, "func f() {\n" + body + "}\n"},
			localRule{"lit-" + u.name, "func f() {\n\tlet run = func() {\n" + indent(body) + "\t}\n\trun()\n}\n"},
			localRule{"test-" + u.name, "test T {\n" + body + "}\n"},
			localRule{"law-" + u.name, "type Law {\n\tOp(v int) int,\n\n\tlaw Holds(v int) {\n" + indent(body) + "\t\tOp(v) == v\n\t},\n}\n"},
		)
	}
	rules = append(rules, localRule{"func-return", "func f() int {\n\tlet w = context { Greeter: Plain }\n\treturn w // want S108\n}\n"})
	rules = append(rules, localRule{"func-tail", "func f() int {\n\tlet w = context { Greeter: Plain }\n\tw // want S108\n}\n"})
	// with A, B for each ordered pair of operand kinds, both binding Greeter
	// or one binding Store: clean, never the old several-operand S109.
	kinds := map[string]struct{ setup, op string }{
		"named":  {"", "Live"},
		"local":  {"\tlet l = context { Store: Holder{v: \"l\"} }\n", "l"},
		"inline": {"", "context { Greeter: Loud }"},
		"value":  {"", "(Tag{s: \"t\"})"},
	}
	for _, a := range []string{"named", "local", "inline", "value"} {
		for _, b := range []string{"named", "local", "inline", "value"} {
			setup := kinds[a].setup
			if b != a {
				setup += kinds[b].setup
			}
			rules = append(rules, localRule{"pair-" + a + "-" + b, "func f() int {\n" + setup + "\twith " + kinds[a].op + ", " + kinds[b].op + " { 1 }\n}\n"})
		}
	}
	return rules
}

// indent adds one tab to each line of a body.
func indent(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "\t" + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestLocalContextRules (check-v1 E5, A2): each program gets exactly its
// `// want` diagnostics, with no cascade; a clean program's Info is
// complete. The E5 acceptance rules (testdata/v1) are judged the same way.
func TestLocalContextRules(t *testing.T) {
	for _, r := range localRules() {
		t.Run(r.name, func(t *testing.T) {
			judgeLocal(t, localPrelude+r.body)
		})
	}
	for _, name := range []string{"E6-extern-contexts.pos", "E6-extern-contexts.neg", "Esub5-context-charge.pos", "Esub5-context-charge.neg"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "v1", name+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			judgeLocal(t, string(data))
		})
	}
}

// judgeLocal checks src: exactly its want lines; Info complete when clean;
// every S108 of a context's use carries one of the Design A2 messages.
func judgeLocal(t *testing.T, src string) {
	t.Helper()
	_, diags := check.CheckSource(src)
	want, have := expected(src), got(diags)
	if strings.Join(want, " ") != strings.Join(have, " ") {
		t.Fatalf("want %v, got %v\n\t%s\n%s", want, have, describe(diags), numbered(src))
	}
	for _, d := range diags {
		if strings.Contains(d.Message, "not checked yet") && strings.Contains(d.Message, "196x") {
			t.Errorf("a 196x refusal is left: %s", d)
		}
	}
	if len(diags) == 0 {
		if gaps := check.InfoGaps(src); len(gaps) > 0 {
			t.Errorf("Info gaps:\n\t%s", strings.Join(gaps, "\n\t"))
		}
	}
}

// chargeCase is one drawn program of TestLocalContextChargeSite: a local
// context whose provider needs the capabilities needs and fails with the
// errors fails; around the let, a with that binds aroundLet (which no with
// of the context may use); and one function literal per with of it.
type chargeCase struct {
	needs, fails, aroundLet []int
	withs                   []chargeWith
}

// chargeWith is one with of the local context, in a function literal whose
// written row is row, inside an enclosing inline with that binds enclosing
// (none when empty). The function that holds the let calls the literal as
// call says.
type chargeWith struct {
	row, enclosing []int
	call           chargeCall
}

// chargeCall is how the function that holds the let uses a literal: not at
// all, a call whose failures a catch takes, a call under try (its failures
// escape), or a plain call (only when the context cannot fail).
type chargeCall int

const (
	callNone chargeCall = iota
	callCaught
	callTry
	callPlain
)

var chargeErrors = []string{"E0", "E1"}

func chargePrelude() string {
	var b strings.Builder
	b.WriteString(severalPrelude())
	b.WriteString("type Out { O() int }\ntype Po { v int }\nfunc O(p Po) int { p.v }\n")
	for _, e := range chargeErrors {
		fmt.Fprintf(&b, "type %s {}\nfunc fail%s(b bool) int fails %s {\n\tif b {\n\t\treturn %s\n\t}\n\t0\n}\n", e, e, e, e)
	}
	return b.String()
}

// capRow writes the capabilities xs as a row, or "" for none.
func capRow(xs []int) string {
	var names []string
	for _, c := range xs {
		names = append(names, "C"+severalCaps[c])
	}
	return strings.Join(names, " | ")
}

// capEntries writes inline-context entries that bind xs.
func capEntries(xs []int) string {
	var parts []string
	for _, c := range xs {
		parts = append(parts, fmt.Sprintf("C%s: P%s{v: 0}", severalCaps[c], severalCaps[c]))
	}
	return strings.Join(parts, ", ")
}

// source writes the case's program and returns the line of the let and of
// each with of the local context.
func (cc chargeCase) source() (src string, letLine int, withLines []int) {
	var b strings.Builder
	b.WriteString(chargePrelude())
	line := func() int { return strings.Count(b.String(), "\n") + 1 }
	var terms []string
	for _, c := range cc.needs {
		terms = append(terms, "need"+severalCaps[c]+"()")
	}
	for _, e := range cc.fails {
		terms = append(terms, "(try fail"+chargeErrors[e]+"(true))")
	}
	if len(terms) == 0 {
		terms = []string{"0"}
	}
	b.WriteString("func f() int {\n")
	pad := "\t"
	if len(cc.aroundLet) > 0 {
		fmt.Fprintf(&b, "\twith context { %s } {\n", capEntries(cc.aroundLet))
		pad = "\t\t"
	}
	letLine = line()
	fmt.Fprintf(&b, "%slet w = context { Out: Po{v: %s} }\n", pad, strings.Join(terms, " + "))
	for i, w := range cc.withs {
		row := ""
		if len(w.row) > 0 {
			row = " needs " + capRow(w.row)
		}
		fmt.Fprintf(&b, "%slet g%d = func() int%s {\n", pad, i, row)
		in := pad + "\t"
		if len(w.enclosing) > 0 {
			fmt.Fprintf(&b, "%swith context { %s } {\n", in, capEntries(w.enclosing))
			in += "\t"
		}
		withLines = append(withLines, line())
		fmt.Fprintf(&b, "%swith w { Out.O() }\n", in)
		if len(w.enclosing) > 0 {
			fmt.Fprintf(&b, "%s}\n", pad+"\t")
		}
		fmt.Fprintf(&b, "%s}\n", pad)
		switch w.call {
		case callNone:
			fmt.Fprintf(&b, "%s_ = g%d\n", pad, i)
		case callCaught:
			fmt.Fprintf(&b, "%s_ = g%d() catch {\n%scase _:\n%s\t0\n%s}\n", pad, i, pad, pad, pad)
		case callTry:
			fmt.Fprintf(&b, "%s_ = try g%d()\n", pad, i)
		case callPlain:
			fmt.Fprintf(&b, "%s_ = g%d()\n", pad, i)
		}
	}
	fmt.Fprintf(&b, "%s0\n", pad)
	if len(cc.aroundLet) > 0 {
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String(), letLine, withLines
}

// chargeRef is what the reference computes for a case: the number of S102
// at each with, the failure row of each literal, and the rows of the
// function that holds the let.
type chargeRef struct {
	s102           []int
	litFails       []string
	fFails, fNeeds string
}

// reference computes, from the drawn sets alone: one S102 at a with per
// need of the context that the literal's written row plus its enclosing
// with do not hold (the with around the let does not count); each
// literal's failure row is the context's failures, which escape at the
// with that builds it; the holding function fails with them exactly when it
// calls a literal under try, and it needs what its called literals' rows
// hold that the with around the let does not bind. Nothing is charged at
// the let.
func (cc chargeCase) reference() chargeRef {
	var ref chargeRef
	var es []string
	for _, e := range cc.fails {
		es = append(es, "main."+chargeErrors[e])
	}
	around := map[int]bool{}
	for _, c := range cc.aroundLet {
		around[c] = true
	}
	fNeeds := map[int]bool{}
	for _, w := range cc.withs {
		held := map[int]bool{}
		for _, c := range append(append([]int(nil), w.row...), w.enclosing...) {
			held[c] = true
		}
		missing := 0
		for _, c := range cc.needs {
			if !held[c] {
				missing++
			}
		}
		ref.s102 = append(ref.s102, missing)
		ref.litFails = append(ref.litFails, strings.Join(es, " | "))
		if w.call == callTry {
			ref.fFails = strings.Join(es, " | ")
		}
		if w.call != callNone {
			for _, c := range w.row {
				if !around[c] {
					fNeeds[c] = true
				}
			}
		}
	}
	var ns []string
	for c := range severalCaps {
		if fNeeds[c] {
			ns = append(ns, "main.C"+severalCaps[c])
		}
	}
	ref.fNeeds = strings.Join(ns, " | ")
	return ref
}

// rowSet sorts a row's text, so two spellings of one set compare equal.
func rowSet(s string) string {
	xs := strings.Split(s, " | ")
	sort.Strings(xs)
	return strings.Join(xs, " | ")
}

func chargeCases(seed int64, n int) []chargeCase {
	r := rand.New(rand.NewSource(seed))
	errs := func() []int {
		var s []int
		for i := range chargeErrors {
			if r.Intn(2) == 0 {
				s = append(s, i)
			}
		}
		return s
	}
	var cases []chargeCase
	for len(cases) < n {
		cc := chargeCase{needs: drawSubset(r, false), fails: errs()}
		if r.Intn(3) == 0 {
			cc.aroundLet = drawSubset(r, true)
		}
		k := 1 + r.Intn(3)
		for i := 0; i < k; i++ {
			w := chargeWith{row: drawSubset(r, false)}
			if r.Intn(2) == 0 {
				w.enclosing = drawSubset(r, true)
			}
			w.call = chargeCall(r.Intn(3))
			if len(cc.fails) == 0 && w.call != callNone {
				// A catch of a call that cannot fail is S108, and try of
				// one is no escape: call it plainly.
				w.call = callPlain
			}
			cc.withs = append(cc.withs, w)
		}
		cases = append(cases, cc)
	}
	return cases
}

// TestLocalContextChargeSite (check-v1 E5, A5; C34): a local context's
// needs and failures are charged at each with that builds it, never at the
// let. Over drawn programs (a reference computes each verdict from the
// drawn sets alone): the S102 sites are exactly the withs whose function
// row plus enclosing with do not hold the context's needs (a with around
// the let does not count); each literal that builds the context fails with
// its failures; and the function that holds the let needs and fails with
// only what its calls of the literals bring.
func TestLocalContextChargeSite(t *testing.T) {
	failures := 0
	for n, cc := range chargeCases(5, 600) {
		src, letLine, withLines := cc.source()
		pkg, diags := check.CheckSource(src)
		fail := func(format string, args ...any) {
			failures++
			if failures <= 5 {
				t.Errorf("case %d: "+format+"\n%s", append([]any{n}, append(args, numbered(src))...)...)
			}
		}
		ref := cc.reference()
		var want []string
		for i, w := range withLines {
			for j := 0; j < ref.s102[i]; j++ {
				want = append(want, fmt.Sprintf("S102@%d", w))
			}
		}
		sort.Strings(want)
		if have := got(diags); strings.Join(have, " ") != strings.Join(want, " ") {
			fail("want %v, got %v (the let is line %d)\n\t%s", want, have, letLine, describe(diags))
			continue
		}
		var lits []*syntax.FuncLit
		for x := range pkg.Info.Literals {
			if l, ok := x.(*syntax.FuncLit); ok {
				lits = append(lits, l)
			}
		}
		sort.Slice(lits, func(i, j int) bool { return lits[i].Pos() < lits[j].Pos() })
		if len(lits) != len(cc.withs) {
			fail("%d literals in Info, %d withs", len(lits), len(cc.withs))
			continue
		}
		for i, l := range lits {
			if have := rowSet(pkg.Info.Literals[l].Errors.String()); have != rowSet(ref.litFails[i]) {
				fail("literal %d fails with %q, the reference says %q", i, have, ref.litFails[i])
			}
		}
		for _, f := range pkg.Funcs {
			if f.Name != "f" {
				continue
			}
			if have := rowSet(f.Sig.Errors.String()); have != rowSet(ref.fFails) {
				fail("f fails with %q, the reference says %q", have, ref.fFails)
			}
			if have := rowSet(f.Sig.Uses.String()); have != rowSet(ref.fNeeds) {
				fail("f needs %q, the reference says %q", have, ref.fNeeds)
			}
		}
		if len(diags) == 0 {
			if gaps := check.InfoGaps(src); len(gaps) > 0 {
				fail("Info gaps:\n\t%s", strings.Join(gaps, "\n\t"))
			}
		}
	}
	if failures > 5 {
		t.Errorf("%d failures in all", failures)
	}
}

// numbered prints src with line numbers, for a failure.
func numbered(src string) string {
	var b strings.Builder
	for i, l := range strings.Split(src, "\n") {
		fmt.Fprintf(&b, "%3d %s\n", i+1, l)
	}
	return b.String()
}

// TestLocalContextLawNine (rows.md, Composition law 9 and its exception;
// E5 round 1 M2): the provider make() needs Console. In a function that
// writes its capability row, `with w` and the inline literal at the same
// with give one row and one set of codes. In a function whose row is
// inferred, the literal's need widens the row and the local's is S102 at
// its operand.
func TestLocalContextLawNine(t *testing.T) {
	const prelude = `type Greeter {
	Greet(name string) string,
}

type Plain {}

func Greet(_ Plain, name string) string {
	name
}

func make() Plain needs Console {
	Console.Println("made")
	Plain
}

`
	bodies := [2]string{
		"\twith context { Greeter: make() } { Greeter.Greet(\"ada\") }\n",
		"\tlet w = context { Greeter: make() }\n\twith w { Greeter.Greet(\"ada\") }\n",
	}
	// check gives f's capability row and its diagnostics' codes.
	check1 := func(sig, body string) (string, []string, string) {
		src := prelude + "func f() string" + sig + " {\n" + body + "}\n"
		pkg, diags := check.CheckSource(src)
		var codes []string
		for _, d := range diags {
			codes = append(codes, d.Code)
		}
		for _, f := range pkg.Funcs {
			if f.Name == "f" && f.Sig != nil {
				return rowSet(f.Sig.Uses.String()), codes, src
			}
		}
		t.Fatalf("no f in\n%s", numbered(src))
		return "", nil, ""
	}
	for _, sig := range []string{" needs Console", " needs Console | Greeter", " needs Greeter"} {
		inRow, inCodes, _ := check1(sig, bodies[0])
		locRow, locCodes, src := check1(sig, bodies[1])
		if inRow != locRow || strings.Join(inCodes, " ") != strings.Join(locCodes, " ") {
			t.Errorf("written row%s: the inline literal gives %q %v, the local %q %v\n%s", sig, inRow, inCodes, locRow, locCodes, numbered(src))
		}
	}
	inRow, inCodes, src := check1("", bodies[0])
	if inRow != "Console" || len(inCodes) != 0 {
		t.Errorf("inferred row: the inline literal gives %q %v, want Console and no diagnostic\n%s", inRow, inCodes, numbered(src))
	}
	_, diags := check.CheckSource(prelude + "func f() string {\n" + bodies[1] + "}\n")
	if have := strings.Join(got(diags), " "); have != "S102@18" || diags[0].Column != 7 {
		t.Errorf("inferred row: the local gives %v, want S102@18 at the operand w\n\t%s", have, describe(diags))
	}
}
