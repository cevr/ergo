package v1

import (
	"strconv"
	"strings"
)

// The bodies of std strings and strconv (docs/spec/stdlib.md, strings and
// strconv). Each is a top-level function of basic and slice types with no
// hidden argument, so BodyOf binds it as written. The names carry their
// std package, because the lowering keys runtime bindings by bare name.

// StringsSplit is strings.Split.
func StringsSplit(s, sep string) []string { return strings.Split(s, sep) }

// StringsJoin is strings.Join.
func StringsJoin(xs []string, sep string) string { return strings.Join(xs, sep) }

// StringsHasPrefix is strings.HasPrefix.
func StringsHasPrefix(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

// StringsHasSuffix is strings.HasSuffix.
func StringsHasSuffix(s, suffix string) bool { return strings.HasSuffix(s, suffix) }

// StringsTrimSpace is strings.TrimSpace.
func StringsTrimSpace(s string) string { return strings.TrimSpace(s) }

// StringsTrimPrefix is strings.TrimPrefix.
func StringsTrimPrefix(s, prefix string) string { return strings.TrimPrefix(s, prefix) }

// StringsTrimSuffix is strings.TrimSuffix.
func StringsTrimSuffix(s, suffix string) string { return strings.TrimSuffix(s, suffix) }

// StringsFields is strings.Fields.
func StringsFields(s string) []string { return strings.Fields(s) }

// StringsRepeat is strings.Repeat; a negative n panics, a defect.
func StringsRepeat(s string, n int) string { return strings.Repeat(s, n) }

// StringsToLower is strings.ToLower.
func StringsToLower(s string) string { return strings.ToLower(s) }

// StringsToUpper is strings.ToUpper.
func StringsToUpper(s string) string { return strings.ToUpper(s) }

// StringsIndex is strings.Index.
func StringsIndex(s, sub string) int { return strings.Index(s, sub) }

// StringsContains is strings.Contains.
func StringsContains(s, sub string) bool { return strings.Contains(s, sub) }

// StringsSlice is s[i:j]; a bound out of range panics, a defect.
func StringsSlice(s string, i, j int) string { return s[i:j] }

// Malformed is strconv.Malformed, the typed failure of Atoi. It is a
// record of a v1 package, so the lowering binds strconv.Malformed to this
// Go type (docs/spec/stdlib.md, Types 4).
type Malformed struct{ Text string }

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's.
func (m Malformed) Error() string { return "strconv.Malformed{Text:" + m.Text + "}" }

// StrconvItoa is strconv.Itoa.
func StrconvItoa(n int) string { return strconv.Itoa(n) }

// StrconvAtoi is strconv.Atoi; any error is Malformed{Text: s}.
func StrconvAtoi(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, Malformed{Text: s}
	}
	return n, nil
}

// StrconvQuote is strconv.Quote.
func StrconvQuote(s string) string { return strconv.Quote(s) }
