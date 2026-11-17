package lint_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// The needless-indirection family of the strict preset (LH2b, catalog §4):
// for each rule a generator and a reference that decides from what the
// generator wrote, never from the program text.

// A wrapper kind of the forwarding-func generator: its text, given its
// name, and whether the rule fires on it.
type wrapperKind struct {
	fires bool
	text  string // a format with one %s, the wrapper's name
}

var wrapperKinds = []wrapperKind{
	{true, "func %s(a int, b int) int { add(a, b) }"},
	{true, "func %s(v box, a int) int { v.scale(a) }"},
	{true, "func %s(a int, b int) int fails Bad { try check(a, b) }"},
	{true, "func %s(a int, b int) int {\n\treturn add(a, b)\n}"},
	{false, "func %s(a int, b int) int { add(b, a) }"},
	{false, "func %s(a int, b int) int { add(a, 1) }"},
	{false, "func %s(a int, b int) int {\n\tlet _ = a\n\tadd(a, b)\n}"},
	{false, "func %s[T](a T) []T { wrap[T](a) }"},
	{false, "func %s() int { zero() }"},
	{false, "func %s(a int) []int { wrap(a) }"},
	{true, "func %s[T](a T) []T { wrap(a) }"},
	{false, "func %s(a int, b int) int { add(a, b) }"},
}

// TestForwardingFuncAgreesWithReference writes wrappers of random kinds,
// each called from main, and checks that forwarding-func fires on exactly
// those that pass every parameter on in order: a dot call's receiver is
// the first argument, a try or a return changes nothing, and swapped or
// literal arguments, a second statement, written type arguments, no
// parameters, one instance of a generic callee and a use as a value are
// no finding. Mutants: lh2b-forward-order-ignored,
// lh2b-forward-receiver-dropped, lh2b-forward-typed-call,
// lh2b-forward-instance-fires, lh2b-forward-value-fires.
func TestForwardingFuncAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xe1))
		p := newPlanted()
		p.write("type Bad { N int }\n\ntype box { N int }\n\nfunc add(a int, b int) int { a + b }\n\nfunc scale(v box, a int) int { v.N * a }\n\nfunc check(a int, b int) int fails Bad {\n\tif a > b {\n\t\treturn Bad{N: a}\n\t}\n\tb\n}\n\nfunc wrap[T](x T) []T { []T{x} }\n\nfunc zero() int { 0 }\n\nfunc apply(f func(int, int) int) int { f(1, 2) }", false, 0)
		var calls []string
		for i := range 1 + r.IntN(5) {
			k := r.IntN(len(wrapperKinds))
			name := fmt.Sprintf("w%d", i)
			p.write("\n"+fmt.Sprintf(wrapperKinds[k].text, name), wrapperKinds[k].fires, 1)
			switch k {
			case 1:
				calls = append(calls, name+"(box{N: 1}, 2)")
			case 2:
				calls = append(calls, name+"(1, 2) catch {\n\tcase Bad:\n\t\t0\n\t}")
			case 7, 9, 10:
				calls = append(calls, name+"(1)[0]")
			case 8:
				calls = append(calls, name+"()")
			case 11:
				calls = append(calls, "apply("+name+")")
			default:
				calls = append(calls, name+"(1, 2)")
			}
		}
		p.write("\nfunc main() {", false, 0)
		for _, c := range calls {
			p.write("\tlet _ = "+c, false, 0)
		}
		p.write("}", false, 0)
		p.check(t, "forwarding-func", "TestForwardingFuncAgreesWithReference", seed)
	}
}

// TestLoneProviderAgreesWithReference declares operation types with zero,
// one or two providers and a member law, a top-level law over them, or
// none, and checks that lone-provider fires on exactly the ones with one
// provider and no law. Mutants: lh2b-lone-law-ignored,
// lh2b-lone-count-ignored.
func TestLoneProviderAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xe2))
		p := newPlanted()
		var uses []string
		for i := range 1 + r.IntN(4) {
			providers, law := r.IntN(3), r.IntN(3) // law: 0 none, 1 member, 2 top-level
			member := ""
			if law == 1 {
				member = fmt.Sprintf("\n\tlaw Nonneg%d() { get%d() >= 0 },", i, i)
			}
			p.write(fmt.Sprintf("\ntype op%d {\n\tget%d() int,%s\n}", i, i, member), providers == 1 && law == 0, 1)
			if law == 2 {
				p.write(fmt.Sprintf("\nlaw Positive%d(o op%d) {\n\to.get%d() >= 0\n}", i, i, i), false, 0)
			}
			for j := range providers {
				p.write(fmt.Sprintf("\ntype p%d_%d { N int } implements op%d\n\nfunc get%d(p p%d_%d) int { p.N }", i, j, i, i, i, j), false, 0)
				uses = append(uses, fmt.Sprintf("use%d(p%d_%d{N: 1})", i, i, j))
			}
			p.write(fmt.Sprintf("\nfunc use%d(o op%d) int { o.get%d() }", i, i, i), false, 0)
		}
		p.write("\nfunc main() {", false, 0)
		for _, u := range uses {
			p.write("\tlet _ = "+u, false, 0)
		}
		p.write("}", false, 0)
		p.check(t, "lone-provider", "TestLoneProviderAgreesWithReference", seed)
	}
}

// A use of the mode-parameter generator: the body's statements that read
// the bool parameter on, and whether they keep it a mode.
type modeUse struct {
	mode bool
	text string
}

var modeUses = []modeUse{
	{true, "\tif on {\n\t\treturn 1\n\t}"},
	{true, "\tif !on {\n\t\treturn 2\n\t}"},
	{false, "\tfor i in []int{1, 2} {\n\t\tif on {\n\t\t\treturn i\n\t\t}\n\t}"},
	{false, "\tif on && n > 0 {\n\t\treturn 3\n\t}"},
	{false, "\tlet _ = flip(on)"},
}

// TestModeParameterAgreesWithReference writes functions whose bool
// parameter is read by a random set of uses, and that main calls with a
// constant, with a computed bool, or passes as a value, and checks that
// mode-parameter fires on exactly the parameters read at least once and
// only as the condition, or the negated condition, of an if written
// directly in the body, of a function only called, always with a
// constant. Mutants: lh2b-mode-nested-if, lh2b-mode-unread-fires,
// lh2b-mode-negation-unread, lh2b-mode-computed-caller,
// lh2b-mode-value-use.
func TestModeParameterAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xe3))
		p := newPlanted()
		p.write("func flip(b bool) bool { !b }\n\nfunc apply(f func(bool, int) int) int { f(true, 0) }", false, 0)
		var calls []string
		for i := range 1 + r.IntN(4) {
			var body []modeUse
			for range r.IntN(3) {
				body = append(body, modeUses[r.IntN(len(modeUses))])
			}
			// how main reaches m: 0 constant calls, 1 a computed bool, 2 as a value
			by := r.IntN(3)
			fires := len(body) > 0 && by == 0
			for _, u := range body {
				fires = fires && u.mode
			}
			p.write(fmt.Sprintf("\nfunc m%d(on bool, n int) int {", i), fires, 1)
			for _, u := range body {
				p.write(u.text, false, 0)
			}
			p.write("\tn\n}", false, 0)
			switch by {
			case 0:
				calls = append(calls, fmt.Sprintf("m%d(true, 1)", i), fmt.Sprintf("m%d(false, 2)", i))
			case 1:
				calls = append(calls, fmt.Sprintf("m%d(true, 1)", i), fmt.Sprintf("m%d(flip(true), 2)", i))
			case 2:
				calls = append(calls, fmt.Sprintf("m%d(true, 1)", i), fmt.Sprintf("apply(m%d)", i))
			}
		}
		p.write("\nfunc main() {", false, 0)
		for _, c := range calls {
			p.write("\tlet _ = "+c, false, 0)
		}
		p.write("}", false, 0)
		p.check(t, "mode-parameter", "TestModeParameterAgreesWithReference", seed)
	}
}

// TestStringlyStateAgreesWithReference compares a record's string fields
// and a local string with literals drawn from a small set, by ==, != and
// match, sets each field to "open" outside tests, only in a test, or
// never, and checks that stringly-state fires once per field set outside
// tests and compared with three or more different literals, "open" one of
// them, at its first
// comparison, and never on the local. Mutants:
// lh2b-stringly-repeats-counted, lh2b-stringly-match-unread,
// lh2b-stringly-test-sets, lh2b-stringly-set-unrelated.
func TestStringlyStateAgreesWithReference(t *testing.T) {
	values := []string{"open", "shut", "held", "gone"}
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xe4))
		p := newPlanted()
		p.write("type rec { S string, T string }", false, 0)
		// how each field is set from a literal: 0 outside tests, 1 in a test only, 2 never
		sets := map[string]int{"r.S": r.IntN(3), "r.T": r.IntN(3)}
		for _, field := range []string{"S", "T"} {
			name := "r." + field
			other := map[string]string{"S": "T", "T": "S"}[field]
			switch sets[name] {
			case 0:
				p.write(fmt.Sprintf("\nfunc new%s(x string) rec { rec{%s: %q, %s: x} }", field, field, "open", other), false, 0)
			case 1:
				p.write(fmt.Sprintf("\ntest set%s {\n\tlet _ = rec{%s: %q, %s: \"\" + \"\"}\n}", field, field, "open", other), false, 0)
			}
		}
		seen := map[string]map[string]bool{"r.S": {}, "r.T": {}, "s": {}}
		first := map[string]int{}
		var order []string
		for i := range 3 + r.IntN(10) {
			subject := []string{"r.S", "r.T", "s"}[r.IntN(3)]
			v := values[r.IntN(len(values))]
			line := p.line + 4
			if _, ok := first[subject]; !ok {
				first[subject] = line
				order = append(order, subject)
			}
			seen[subject][v] = true
			switch r.IntN(3) {
			case 0:
				p.write(fmt.Sprintf("\nfunc c%d(r rec) bool {\n\tlet s = r.S + r.T\n\tlet _ = s\n\t%s == %q\n}", i, subject, v), false, 0)
			case 1:
				p.write(fmt.Sprintf("\nfunc c%d(r rec) bool {\n\tlet s = r.S + r.T\n\tlet _ = s\n\t%s != %q\n}", i, subject, v), false, 0)
			default:
				p.write(fmt.Sprintf("\nfunc c%d(r rec) bool {\n\tlet s = r.S + r.T\n\tlet _ = s\n\tmatch %s {\n\tcase %q:\n\t\ttrue\n\tcase _:\n\t\tfalse\n\t}\n}", i, subject, v), false, 0)
			}
		}
		for _, subject := range order {
			if subject != "s" && sets[subject] == 0 && seen[subject]["open"] && len(seen[subject]) >= 3 {
				p.want = append(p.want, first[subject])
			}
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "stringly-state", "TestStringlyStateAgreesWithReference", seed)
	}
}
