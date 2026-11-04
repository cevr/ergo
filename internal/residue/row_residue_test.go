package residue_test

import (
	"regexp"
	"testing"
)

// rowResidueWord matches `needs` or `provides` followed by a comma list of
// names, parenthesised or not: the comma spelling of a capability row
// (196n). It is Go's regexp, so it reads the same on every host; git grep
// only selects the lines that hold a literal (residue).
var rowResidueWord = regexp.MustCompile(`\b(needs|provides)[[:space:]]+\(?[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*[[:space:]]*,[[:space:]]*[A-Za-z_]`)

// rowResidue is the check of TestRowResidue.
var rowResidue = residue{
	allowlist: "testdata/row-residue.txt",
	literals:  []string{"needs", "provides"},
	word:      rowResidueWord,
	fix:       "write the row with |: needs A | B, or add the prose to the allowlist with its reason",
}

// TestRowResidue is 196n acceptance check 2: a capability row is written
// with |, so a tracked line outside history that writes `needs A, B` or
// `provides A, B` is prose that only reads like a row, an old-syntax input,
// or the record of the change, and testdata/row-residue.txt names it
// with its reason. A match no row covers fails, and so does a row that
// covers no match.
func TestRowResidue(t *testing.T) { rowResidue.run(t) }
