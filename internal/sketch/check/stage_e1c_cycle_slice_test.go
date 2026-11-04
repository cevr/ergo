package check_test

import (
	"fmt"
	"testing"
)

const (
	e1cFullSample  = 20_000
	e1cSliceSample = 500
)

// Section 3.2, counted without constructing any alphabet or choice set:
// Q = int + own parameters + non-generic declarations is the argument domain;
// A = int + own parameters + sum(Q^arity) is the atom domain. Each atom has
// three field forms, records have one field or an unordered pair with
// repetition, and the bases are A tuples plus A-P defined types.
func e1cExpectedChoices(arities []int, params int) int {
	arguments := 1 + params
	for _, arity := range arities {
		if arity == 0 {
			arguments++
		}
	}
	atoms := 1 + params
	for _, arity := range arities {
		power := 1
		for range arity {
			power *= arguments
		}
		atoms += power
	}
	fields := 3 * atoms
	return fields*(fields+3)/2 + 2*atoms - params
}

func e1cExpectedDomain(full bool) (total int) {
	for a := 0; a <= 2; a++ {
		total += e1cExpectedChoices([]int{a}, a)
		for b := 0; b <= 2; b++ {
			arities := []int{a, b}
			left := e1cExpectedChoices(arities, a)
			right := e1cExpectedChoices(arities, b)
			if full {
				total += left * right
			} else {
				total += left + right
			}
		}
	}
	if full {
		return total + 2*e1cFullSample
	}
	return total + 2*e1cSliceSample
}

// Expected source spellings, independently of the model's alphabet builder.
// Keeping complete instantiated atoms here also checks every argument tuple,
// including mixed arguments, at each observed field/base position.
func e1cRequiredAtoms(arities []int, params int) map[string]bool {
	parameters := []string{"T", "U"}[:params]
	arguments := append([]string{"int"}, parameters...)
	for d, arity := range arities {
		if arity == 0 {
			arguments = append(arguments, fmt.Sprintf("D%d", d))
		}
	}
	want := map[string]bool{"int": true}
	for _, p := range parameters {
		want[p] = true
	}
	for d, arity := range arities {
		name := fmt.Sprintf("D%d", d)
		switch arity {
		case 0:
			want[name] = true
		case 1:
			for _, a := range arguments {
				want[name+"["+a+"]"] = true
			}
		case 2:
			for _, a := range arguments {
				for _, b := range arguments {
					want[name+"["+a+", "+b+"]"] = true
				}
			}
		}
	}
	return want
}

type e1cSliceCoverage struct {
	positions map[string]bool
	kinds     [4]int
}

func (c *e1cSliceCoverage) observe(ds []e1cDecl, kind int) {
	c.kinds[kind]++
	arities := []int{ds[0].params, ds[1].params}
	for d, decl := range ds {
		for position, field := range decl.fields {
			c.positions[e1cCoverageKey(arities, d, decl.form, len(decl.fields), position, field.text())] = true
		}
	}
}

// e1cCoverageKey is one atom position of the slice coverage. A record's
// key holds its arity, so one-field and two-field records are separate
// positions (r1/0, r2/0, r2/1; stage E1c round 3 m2, check-v1 E6b).
func e1cCoverageKey(arities []int, d int, form byte, fields, position int, atom string) string {
	shape := fmt.Sprintf("%c%d", form, position)
	if form == 'r' {
		shape = fmt.Sprintf("r%d/%d", fields, position)
	}
	return fmt.Sprintf("%v/D%d/%s/%s", arities, d, shape, atom)
}

func e1cAssertSliceCoverage(t *testing.T, parts []e1cSliceCoverage) {
	t.Helper()
	seen := map[string]bool{}
	var kinds [4]int
	for _, part := range parts {
		for position := range part.positions {
			seen[position] = true
		}
		for kind, count := range part.kinds {
			kinds[kind] += count
		}
	}
	require := func(arities []int, d int, form byte, fields, position int, atom string) {
		t.Helper()
		key := e1cCoverageKey(arities, d, form, fields, position, atom)
		if !seen[key] {
			t.Fatalf("slice lost atom position %s", key)
		}
	}
	for a := 0; a <= 2; a++ {
		for b := 0; b <= 2; b++ {
			arities := []int{a, b}
			for d, params := range arities {
				for atom := range e1cRequiredAtoms(arities, params) {
					for _, field := range []string{atom, "Option[" + atom + "]", "[]" + atom} {
						// A one-field record, and each position of a
						// two-field one (the pair {f, f} holds f at both).
						require(arities, d, 'r', 1, 0, field)
						require(arities, d, 'r', 2, 0, field)
						require(arities, d, 'r', 2, 1, field)
					}
					require(arities, d, 'q', 1, 0, "("+atom+", int)")
					if atom != "T" && atom != "U" {
						require(arities, d, 'd', 1, 0, atom)
					}
				}
			}
		}
	}
	for kind, name := range []string{"finite", "intrinsic", "orphan", "mixed"} {
		if kinds[kind] == 0 {
			t.Fatalf("slice lost R2' case kind %s", name)
		}
	}
	t.Logf("slice coverage: atom-positions=%d R2' kinds [finite intrinsic orphan mixed]=%v", len(seen), kinds)
}
