package lint_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
)

// The SKILL-RULES rules for Ergo (SR-ERGO-n): each a generator of
// programs and a reference decided from the generator's choices.

// A form of the derived-field generator: how a construction writes F from
// the parameters a and b (A's and B's values) and c (no field's), how an
// update over r writes it, and whether it is a derived form: constants,
// operators and pure calls over the siblings only.
type derivedForm struct {
	build, update string
	derived       bool
	console       bool   // the form calls a function that needs Console
	b             string // B's value in the constructor, b when empty
}

var derivedForms = []derivedForm{
	{"a + b", "r.A + 1 + r.B", true, false, ""},
	{"twice(a) - b", "twice(r.A + 1) - r.B", true, false, ""},
	{"noisy(a) + b", "noisy(r.A + 1) + r.B", false, true, ""},
	{"a + c", "r.A + 1 + c", false, false, ""},
	{"7", "7", false, false, ""},
	{"a", "r.A + 1", false, false, ""},
	{"twice(1) + 1", "twice(1) + 1", false, false, "twice(1)"},
}

// TestDerivedFieldAgreesWithReference writes records { A, B, F } with a
// constructor that sets F by one form, and sometimes a second write:
// another constructor with another form, an update that sets A and F by
// the same form, or one that sets A alone. It checks that derived-field
// fires on F exactly when every write gives F one derived form (one that
// reads a sibling's value, not a copy of it, and not a value that reads
// no variable) and no write moves A without it. Mutants: sr1-purity,
// sr1-same-form, sr1-partial, sr1-no-sibling, sr1-free-name, sr1-copy,
// sr1-input-reads-var.
func TestDerivedFieldAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(30) {
		r := rand.New(rand.NewPCG(seed, 0x5e1))
		p := newPlanted()
		p.write("func twice(n int) int { n * 2 }\n\nfunc noisy(n int) int needs Console {\n\tConsole.Println(\"n\")\n\tn\n}", false, 0)
		for i := range 1 + r.IntN(4) {
			form := derivedForms[r.IntN(len(derivedForms))]
			needs := ""
			if form.console {
				needs = " needs Console"
			}
			bval := form.b
			if bval == "" {
				bval = "b"
			}
			second := r.IntN(5)
			derived := form.derived
			text := fmt.Sprintf("type R%[1]d { A int, B int, F int }\n\nfunc make%[1]d(a int, b int, c int) R%[1]d%[2]s {\n\tR%[1]d{A: a, B: %[4]s, F: %[3]s}\n}", i, needs, form.build, bval)
			switch second {
			case 1: // another form
				text += fmt.Sprintf("\n\nfunc other%[1]d(a int, b int) R%[1]d {\n\tR%[1]d{A: a, B: b, F: a * b}\n}", i)
				derived = false
			case 3: // an update that keeps F in step
				text += fmt.Sprintf("\n\nfunc step%[1]d(r R%[1]d, c int) R%[1]d%[2]s {\n\tR%[1]d{..r, A: r.A + 1, F: %[3]s}\n}", i, needs, form.update)
			case 4: // an update that moves A alone
				text += fmt.Sprintf("\n\nfunc drift%[1]d(r R%[1]d) R%[1]d {\n\tR%[1]d{..r, A: r.A + 1}\n}", i)
				derived = false
			}
			p.write("\n"+text, derived, 1)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "derived-field", "TestDerivedFieldAgreesWithReference", seed)
	}
}

// The modes of the option-fields-follow-variant generator: how a write
// gives the record's mode field, by index into Idle, Busy, Done.
var followModes = []string{"Idle%[1]d", "Busy%[1]d{Job: n}", "Done%[1]d{Code: n}"}

// TestOptionFieldsFollowVariantAgreesWithReference writes records
// { M Mode, J Option[int] } and one to four constructors, each giving M a
// mode and J a value or None; sometimes J from a call (its variant not
// visible), an update that sets J alone, or an assignment to J. It checks that
// option-fields-follow-variant fires on J exactly when every write's
// variants are visible, no update moves J alone, every constructor that
// gives J a value gives M one mode, and some constructor gives M another.
// Mutants: sr2-some-site, sr2-unknown, sr2-no-other.
func TestOptionFieldsFollowVariantAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0x5e2))
		p := newPlanted()
		p.write("func pick(n int) Option[int] {\n\tif n > 0 {\n\t\treturn Some[int]{Value: n}\n\t}\n\tNone\n}", false, 0)
		for i := range 1 + r.IntN(3) {
			var sites []string
			under, mixed, other, hidden := -1, false, false, false
			n := 1 + r.IntN(4)
			modes := make([]int, n)
			present := make([]bool, n)
			for k := range n {
				modes[k] = r.IntN(len(followModes))
				present[k] = r.IntN(2) == 0
				opt := "None"
				if present[k] {
					opt = "Some[int]{Value: n}"
					if under >= 0 && under != modes[k] {
						mixed = true
					}
					under = modes[k]
				}
				sites = append(sites, fmt.Sprintf("func mk%[1]d_%[2]d(n int) R%[1]d {\n\tR%[1]d{M: "+followModes[modes[k]]+", J: %[3]s}\n}", i, k, opt))
			}
			for k := range n {
				if under >= 0 && modes[k] != under {
					other = true
				}
			}
			switch r.IntN(5) {
			case 0:
				sites = append(sites, fmt.Sprintf("func hidden%[1]d(n int) R%[1]d {\n\tR%[1]d{M: Idle%[1]d, J: pick(n)}\n}", i))
				hidden = true
			case 1:
				sites = append(sites, fmt.Sprintf("func clear%[1]d(r R%[1]d) R%[1]d {\n\tR%[1]d{..r, J: None}\n}", i))
				hidden = true
			case 2:
				sites = append(sites, fmt.Sprintf("func reset%[1]d(r R%[1]d) R%[1]d {\n\tlet mut s = r\n\ts.J = None\n\ts\n}", i))
				hidden = true
			}
			fires := under >= 0 && !mixed && other && !hidden
			text := fmt.Sprintf("type Idle%[1]d {}\ntype Busy%[1]d { Job int }\ntype Done%[1]d { Code int }\ntype Mode%[1]d = Idle%[1]d | Busy%[1]d | Done%[1]d\ntype R%[1]d { M Mode%[1]d, J Option[int] }", i)
			for _, s := range sites {
				text += "\n\n" + s
			}
			p.write("\n"+text, fires, 5)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "option-fields-follow-variant", "TestOptionFieldsFollowVariantAgreesWithReference", seed)
	}
}

// The avoided terms of the glossary generator, each under the term to use.
var glossaryAvoids = []struct{ use, avoid string }{
	{"Customer", "Client"},
	{"Customer", "buyer"},
	{"Invoice", "payment request"},
	{"Invoice", "Bill"},
}

// A function name of the glossary generator and the avoided terms (by
// index into glossaryAvoids) it holds as a run of its words.
var glossaryNames = []struct {
	name  string
	holds []int
}{
	{"clientList", []int{0}},
	{"BuyerOf", []int{1}},
	{"paymentRequestID", []int{2}},
	{"lastBillOfClient", []int{0, 3}},
	{"paymentID", nil},      // one word of a two-word term
	{"requestPayment", nil}, // its words out of order
	{"billing", nil},        // another word
	{"customerName", nil},
}

// TestBannedGlossaryAgreesWithReference writes a GLOSSARY.md that lists a
// random subset of avoided terms (one of two words) under their terms,
// configures banned-words with it as glossary, relative to the document's
// directory, and checks that the rule fires on exactly the functions whose
// name holds a listed term as a run of its words, naming the term to use;
// and that a glossary that cannot be read refuses the configuration.
// Mutants: sr3-run, sr3-unread, sr3-use.
func TestBannedGlossaryAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0x6105))
		listed := make([]bool, len(glossaryAvoids))
		doc := "# Shop\n\n## Language\n"
		for _, use := range []string{"Customer", "Invoice"} {
			var avoid []string
			for i, a := range glossaryAvoids {
				if a.use == use && r.IntN(2) == 0 {
					listed[i] = true
					avoid = append(avoid, a.avoid)
				}
			}
			doc += "\n**" + use + "**:\nA term of the shop.\n"
			if avoid != nil {
				doc += "_Avoid_: " + strings.Join(avoid, ", ") + "\n"
			}
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "GLOSSARY.md"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		p := newPlanted()
		uses := map[int]string{}
		for i := range 1 + r.IntN(5) {
			n := glossaryNames[r.IntN(len(glossaryNames))]
			fires := false
			for _, h := range n.holds {
				if listed[h] {
					if !fires {
						uses[p.line+1] = glossaryAvoids[h].use
					}
					fires = true
				}
			}
			p.write(fmt.Sprintf("\nfunc %s%d() int {\n\t%d\n}", n.name, i, i), fires, 1)
		}
		p.write("\nfunc main() {\n}", false, 0)
		settings := map[string]lint.Setting{}
		for _, rule := range lint.Builtin().Rules() {
			settings[rule.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
		}
		settings["banned-words"] = lint.Setting{Severity: lint.Warning, HasSeverity: true, Options: []byte("{\"glossary\": \"GLOSSARY.md\"}")}
		conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings, Dir: dir}, nil)
		if err != nil {
			t.Fatal(err)
		}
		src := p.b.String()
		var got []int
		for _, f := range conf.Run(lintInput(src)).Findings {
			got = append(got, f.Line)
			if !strings.HasSuffix(f.Instead, "glossary's term "+uses[f.Line]) {
				t.Fatalf("seed %d: line %d names no term %q to use: %s", seed, f.Line, uses[f.Line], f.Instead)
			}
		}
		want := slices.Clone(p.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: banned-words fired on lines %v, the reference says %v\n%s\n%s\nreplay: go test ./internal/sketch/lint -run TestBannedGlossaryAgreesWithReference -lint.seed=%d", seed, got, want, doc, numbered(src), seed)
		}
		if _, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings, Dir: filepath.Join(dir, "absent")}, nil); err == nil {
			t.Fatalf("seed %d: a glossary that cannot be read configured", seed)
		}
	}
}

// The laws of the take-one-by-law generator over sortOf: the law's body
// and which ends of sortOf(xs) it states (first by least, last by most).
var takeLaws = []struct {
	body        string
	first, last bool
}{
	{"\tlet s = sortOf(xs)\n\tif len(s) == 0 {\n\t\treturn true\n\t}\n\tleast(xs) == Some[int]{Value: s[0]} && most(xs) == Some[int]{Value: s[len(s)-1]}", true, true},
	{"\tlet s = sortOf(xs)\n\tif len(xs) > 3 {\n\t\treturn true\n\t}\n\tleast(xs) == Some[int]{Value: s[0]}", false, false}, // a guard that is not the empty case
	{"\tleast(xs) == Some[int]{Value: sortOf(xs)[0]}", true, false},
	{"\tlet s = sortOf(xs)\n\tleast(xs) == Some[int]{Value: s[1]}", false, false}, // not an end
}

// A function of the take-one-by-law generator: its body over xs, the
// element type, the end it takes (0 none, 1 first, 2 last) and the line
// of the take within the function.
var takeSites = []struct {
	body, elem string
	end, at    int
}{
	{"\tsortOf(xs)[0]", "int", 1, 1},
	{"\tlet s = sortOf(xs)\n\ts[len(s)-1]", "int", 2, 2},
	{"\tlet s = sortOf(xs)\n\tif len(s) == 0 {\n\t\treturn 0\n\t}\n\ts[0]", "int", 1, 5},
	{"\tlet s = sortOf(xs)\n\ts[0] + s[1]", "int", 0, 0},       // the let read for more than its ends
	{"\tsortOf(xs)[1]", "int", 0, 0},                           // not an end
	{"\tsortOf(xs)[0]", "float64", 0, 0},                       // a type the law was not tested at
	{"\tlet s = sortOf(xs)\n\tlet t = s\n\tt[0]", "int", 0, 0}, // the let passed on
}

// TestTakeOneByLawAgreesWithReference declares sortOf, least and most, a
// law of one of four bodies over them, and functions that take an end of
// sortOf's result directly or through a let, and checks that
// take-one-by-law fires on exactly the takes of an end the law states, at
// the law's argument type, through a let read only for its ends and
// length. Mutants: sr5-guard, sr5-type, sr5-other-read, sr5-end.
func TestTakeOneByLawAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0x5a7))
		p := newPlanted()
		p.write("func sortOf[T ordered](xs []T) []T {\n\txs\n}\n\nfunc least(xs []int) Option[int] {\n\tNone\n}\n\nfunc most(xs []int) Option[int] {\n\tNone\n}", false, 0)
		law := takeLaws[r.IntN(len(takeLaws))]
		if r.IntN(4) == 0 {
			law.first, law.last = false, false
		} else {
			p.write("\nlaw Ends(xs []int) {\n"+law.body+"\n}", false, 0)
		}
		for i := range 1 + r.IntN(4) {
			s := takeSites[r.IntN(len(takeSites))]
			fires := s.end == 1 && law.first || s.end == 2 && law.last
			p.write(fmt.Sprintf("\nfunc take%d(xs []%s) %s {\n%s\n}", i, s.elem, s.elem, s.body), fires, 1+s.at)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "take-one-by-law", "TestTakeOneByLawAgreesWithReference", seed)
	}
}

// A let value of the independent-waits generator (the second let may
// read x, the first's): its text, the capabilities it exercises
// (A, B, or none), whether every call in it is known, whether it reads
// x, and whether it may leave the function early.
var waitLets = []struct {
	text  string
	caps  string
	known bool
	reads bool
	leave bool
}{
	{"A.Get()", "A", true, false, false},
	{"fromA()", "A", true, false, false},
	{"B.Get()", "B", true, false, false},
	{"fromB(0)", "B", true, false, false},
	{"fromB(x)", "B", true, true, false},
	{"B.Get() + x", "B", true, true, false},
	{"both()", "AB", true, false, false},
	{"3 + 4", "", true, false, false},
	{"g()", "", false, false, false}, // a function value: its footprint is unknown
	{"if A.Get() > 0 {\n\t\treturn 0\n\t} else {\n\t\t1\n\t}", "A", true, false, true},
}

// TestIndependentWaitsAgreesWithReference writes functions whose body is
// two lets in a row, each from waitLets, over operation types A and B,
// and checks that independent-waits fires on the second exactly when both
// exercise known capabilities, none shared, and the second reads nothing
// the first binds, and the first cannot return early. Mutants: sr6-reads,
// sr6-disjoint, sr6-known, sr6-empty, sr6-leave.
func TestIndependentWaitsAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0x5e6))
		p := newPlanted()
		p.write("type A {\n\tGet() int,\n}\n\ntype B {\n\tGet() int,\n}\n\nfunc fromA() int needs A {\n\tA.Get()\n}\n\nfunc fromB(n int) int needs B {\n\tB.Get() + n\n}\n\nfunc both() int needs A | B {\n\tA.Get() + B.Get()\n}", false, 0)
		for i := range 1 + r.IntN(4) {
			a := waitLets[r.IntN(len(waitLets))]
			for a.reads {
				a = waitLets[r.IntN(len(waitLets))]
			}
			b := waitLets[r.IntN(len(waitLets))]
			shared := false
			for _, c := range a.caps {
				shared = shared || strings.ContainsRune(b.caps, c)
			}
			fires := a.known && b.known && a.caps != "" && b.caps != "" && !shared && !b.reads && !a.leave
			p.write(fmt.Sprintf("\nfunc run%d(g func() int needs A) int needs A | B {\n\tlet x = %s\n\tlet y = %s\n\tx + y\n}", i, a.text, b.text), fires, 3+strings.Count(a.text, "\n"))
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "independent-waits", "TestIndependentWaitsAgreesWithReference", seed)
	}
}

// A statement of the weak-test generator: its text (one line, at the
// indent of a test body), whether it is an assert whose sides are
// constants of one value, and whether it is an assert, which a repeat of
// it with only asserts between restates (each is pure).
var weakStmts = []struct {
	text             string
	restated, assert bool
}{
	{"assert Limit == 5", true, true},
	{"assert 3 == 3", true, true},
	{"assert \"a\" == \"a\"", true, true},
	{"assert Limit + 1 == 6", false, true}, // not a bare name: the checker folds no let
	{"assert double(Limit) == 10", false, true},
	{"assert double(2) == 4", false, true},
	{"assert Limit == 6", false, true}, // constants of two values: a failing test, not a restated one
	{"let _ = double(3)", false, false},
	{"let _ = get(1) catch {\n\tcase Bad:\n\t\t0\n\t}", false, false},
}

// TestWeakTestsAgreeWithReference writes tests of one to four statements
// from weakStmts over a constant Limit, a pure double and a failing get,
// and checks constant-restated (every assert of two constants of one
// value) and repeated-case (an assert equal to one before it with only
// asserts between). Mutants: sr7-value, sr7-between, sr7-name.
func TestWeakTestsAgreeWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0x7e57))
		ps := map[string]*plantedRule{"constant-restated": {p: newPlanted()}, "repeated-case": {p: newPlanted()}}
		head := "let Limit = 5\n\ntype Bad {}\n\nfunc double(n int) int {\n\tn + n\n}\n\nfunc get(n int) int fails Bad {\n\tif n < 0 {\n\t\treturn Bad\n\t}\n\tn\n}"
		for _, pr := range ps {
			pr.p.write(head, false, 0)
		}
		for i := range 1 + r.IntN(4) {
			lines := []string{fmt.Sprintf("\ntest Weak%d {", i)}
			restated, repeated := []int{}, []int{}
			var run []string // the asserts since the last other statement
			for range 1 + r.IntN(4) {
				s := weakStmts[r.IntN(len(weakStmts))]
				if r.IntN(3) == 0 && len(run) > 0 {
					s = weakStmts[0]
					for _, w := range weakStmts {
						if w.text == run[r.IntN(len(run))] {
							s = w
						}
					}
				}
				at := len(lines)
				if s.restated {
					restated = append(restated, at)
				}
				if s.assert && slices.Contains(run, s.text) {
					repeated = append(repeated, at)
				}
				if s.assert {
					run = append(run, s.text)
				} else {
					run = nil
				}
				lines = append(lines, "\t"+s.text)
				lines = append(lines, make([]string, strings.Count(s.text, "\n"))...)
			}
			text := ""
			for _, l := range lines {
				if l != "" {
					text += l + "\n"
				}
			}
			text += "}"
			ps["constant-restated"].write(text, restated)
			ps["repeated-case"].write(text, repeated)
		}
		for name, pr := range ps {
			pr.p.write("\nfunc main() {\n}", false, 0)
			pr.p.check(t, name, "TestWeakTestsAgreeWithReference", seed)
		}
	}
}

// plantedRule is a planted program for one rule of a generator that
// plants several on the same text.
type plantedRule struct{ p *planted }

// write adds text, recording each of its lines at (0 its first; text's
// own first line is a newline, so at counts from the line after it).
func (pr *plantedRule) write(text string, at []int) {
	first := pr.p.line
	pr.p.write(text, false, 0)
	for _, a := range at {
		pr.p.want = append(pr.p.want, first+1+a)
	}
}
