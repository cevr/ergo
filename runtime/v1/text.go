package v1

import (
	"strconv"
	"strings"
)

// The bodies of std strings and strconv (docs/spec/stdlib.md, strings and
// strconv). Each is a top-level function of basic and slice types with no
// hidden argument, so BodyOf binds it as written. The names carry their
// std package, because the lowering keys runtime bindings by bare name.
//
// Each body is marked //ergo:pure (docs/spec/stdlib.md, Pure bodies): it
// computes on its arguments with strings and strconv only, so a call of it
// is transparent in a footprint. Keep this file's imports in PureImports;
// BodyOf refuses a marked body otherwise.

// StringsSplit is strings.Split.
//
//ergo:pure
func StringsSplit(s, sep string) []string { return strings.Split(s, sep) }

// StringsJoin is strings.Join.
//
//ergo:pure
func StringsJoin(xs []string, sep string) string { return strings.Join(xs, sep) }

// StringsHasPrefix is strings.HasPrefix.
//
//ergo:pure
func StringsHasPrefix(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

// StringsHasSuffix is strings.HasSuffix.
//
//ergo:pure
func StringsHasSuffix(s, suffix string) bool { return strings.HasSuffix(s, suffix) }

// StringsTrimSpace is strings.TrimSpace.
//
//ergo:pure
func StringsTrimSpace(s string) string { return strings.TrimSpace(s) }

// StringsTrimPrefix is strings.TrimPrefix.
//
//ergo:pure
func StringsTrimPrefix(s, prefix string) string { return strings.TrimPrefix(s, prefix) }

// StringsTrimSuffix is strings.TrimSuffix.
//
//ergo:pure
func StringsTrimSuffix(s, suffix string) string { return strings.TrimSuffix(s, suffix) }

// StringsFields is strings.Fields.
//
//ergo:pure
func StringsFields(s string) []string { return strings.Fields(s) }

// StringsRepeat is strings.Repeat; a negative n panics, a defect.
//
//ergo:pure
func StringsRepeat(s string, n int) string { return strings.Repeat(s, n) }

// StringsToLower is strings.ToLower.
//
//ergo:pure
func StringsToLower(s string) string { return strings.ToLower(s) }

// StringsToUpper is strings.ToUpper.
//
//ergo:pure
func StringsToUpper(s string) string { return strings.ToUpper(s) }

// StringsIndex is strings.Index.
//
//ergo:pure
func StringsIndex(s, sub string) int { return strings.Index(s, sub) }

// StringsContains is strings.Contains.
//
//ergo:pure
func StringsContains(s, sub string) bool { return strings.Contains(s, sub) }

// StringsSlice is s[i:j]; a bound out of range panics, a defect.
//
//ergo:pure
func StringsSlice(s string, i, j int) string { return s[i:j] }

// Malformed is strconv.Malformed, the typed failure of Atoi. It is a
// record of a v1 package, so the lowering binds strconv.Malformed to this
// Go type (docs/spec/stdlib.md, Types 4).
type Malformed struct{ Text string }

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's.
func (m Malformed) Error() string { return "strconv.Malformed{Text:" + m.Text + "}" }

// StrconvItoa is strconv.Itoa.
//
//ergo:pure
func StrconvItoa(n int) string { return strconv.Itoa(n) }

// StrconvAtoi is strconv.Atoi; any error is Malformed{Text: s}.
//
//ergo:pure
func StrconvAtoi(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, Malformed{Text: s}
	}
	return n, nil
}

// StrconvQuote is strconv.Quote.
//
//ergo:pure
func StrconvQuote(s string) string { return strconv.Quote(s) }
