package check_test

import (
	"regexp"
	"testing"
)

// rowResiduePattern matches `needs` or `provides` followed by a comma list
// of names, parenthesised or not: the comma spelling of a capability row
// (196n). The ERE form is git grep's; rowResidueWord is the same pattern for
// a match's span.
const rowResiduePattern = `\b(needs|provides)[[:space:]]+\(?[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*[[:space:]]*,[[:space:]]*[A-Za-z_]`

var rowResidueWord = regexp.MustCompile(`\b(needs|provides)[[:space:]]+\(?[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*[[:space:]]*,[[:space:]]*[A-Za-z_]`)

// TestRowResidue is 196n acceptance check 2: a capability row is written
// with |, so a tracked line outside history that writes `needs A, B` or
// `provides A, B` is prose that only reads like a row, an old-syntax input,
// or the record of the change, and testdata/v1/row-residue.txt names it
// with its reason. A match no row covers fails, and so does a row that
// covers no match.
func TestRowResidue(t *testing.T) {
	residue{
		allowlist: "testdata/v1/row-residue.txt",
		grep:      []string{"-E", rowResiduePattern},
		word:      rowResidueWord,
		fix:       "write the row with |: needs A | B, or add the prose to the allowlist with its reason",
	}.run(t)
}
