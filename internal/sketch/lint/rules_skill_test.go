package lint_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
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
