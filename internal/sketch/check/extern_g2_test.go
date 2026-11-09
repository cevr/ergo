package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// G2 gx-allowlist (lane G, batch 12; f-11): unicode and unicode/utf16
// joined the pure list. IG4 replaced the host text with the Level 2
// relation (TestLevel2AdapterRelation): a target off the list is admitted
// with needs go.Foreign.

// TestExternUnicodeAdmitted: the f-11 repro's shapes over the two new
// packages check clean with no needs row.
func TestExternUnicodeAdmitted(t *testing.T) {
	src := `extern go unicode.ToLower func Lower(r rune) rune
extern go unicode.IsUpper func IsUpper(r rune) bool
extern go unicode/utf16.IsSurrogate func IsSurrogate(r rune) bool
extern go math/bits.OnesCount func OnesCount(x uint) int
func Use(r rune) bool { IsUpper(Lower(r)) || IsSurrogate(r) || OnesCount(uint(r)) > 3 }
`
	if _, diags := check.CheckSource(src); len(diags) != 0 {
		t.Fatalf("want clean, got\n\t%s", describe(diags))
	}
}
