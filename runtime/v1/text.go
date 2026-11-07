package v1

import (
	"strconv"
	"strings"
	"unicode/utf8"
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

// StringsReplaceAll is strings.ReplaceAll.
//
//ergo:pure
func StringsReplaceAll(s, old, repl string) string { return strings.ReplaceAll(s, old, repl) }

// StringsLastIndex is strings.LastIndex.
//
//ergo:pure
func StringsLastIndex(s, sub string) int { return strings.LastIndex(s, sub) }

// StringsCount is strings.Count.
//
//ergo:pure
func StringsCount(s, sub string) int { return strings.Count(s, sub) }

// StringsSplitN is strings.SplitN. Go returns nil for n == 0; Ergo has no
// nil slice, so it is an empty slice.
//
//ergo:pure
func StringsSplitN(s, sep string, n int) []string {
	if parts := strings.SplitN(s, sep, n); parts != nil {
		return parts
	}
	return []string{}
}

// StringsEqualFold is strings.EqualFold.
//
//ergo:pure
func StringsEqualFold(s, t string) bool { return strings.EqualFold(s, t) }

// StringsContainsRune is strings.ContainsRune.
//
//ergo:pure
func StringsContainsRune(s string, r rune) bool { return strings.ContainsRune(s, r) }

// UTF8RuneCount is utf8.RuneCountInString.
//
//ergo:pure
func UTF8RuneCount(s string) int { return utf8.RuneCountInString(s) }

// Malformed is strconv.Malformed, the failure of a text that is not in the
// form a strconv function reads (Go's ErrSyntax). It is a record of a v1
// package, so the lowering binds strconv.Malformed to this Go type
// (docs/spec/stdlib.md, Types 4).
type Malformed struct{ Text string }

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's.
func (m Malformed) Error() string { return "strconv.Malformed{Text:" + m.Text + "}" }

// OutOfRange is strconv.OutOfRange, the failure of a number that does not
// fit its type (Go's ErrRange).
type OutOfRange struct{ Text string }

// Error is the failure's text, as Malformed's is.
func (m OutOfRange) Error() string { return "strconv.OutOfRange{Text:" + m.Text + "}" }

// Each parse body below maps Go's *strconv.NumError inline (a pure body
// names no other function of this package): ErrRange is OutOfRange,
// ErrSyntax is Malformed, and any other error (an invalid base or bit
// size, a precondition of the call) panics as a defect.

// StrconvItoa is strconv.Itoa.
//
//ergo:pure
func StrconvItoa(n int) string { return strconv.Itoa(n) }

// StrconvAtoi is strconv.Atoi: OutOfRange for a number out of int's
// range, Malformed for any other text it refuses.
//
//ergo:pure
func StrconvAtoi(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return 0, OutOfRange{Text: s}
		}
		return 0, Malformed{Text: s}
	}
	return n, nil
}

// StrconvParseInt is strconv.ParseInt.
//
//ergo:pure
func StrconvParseInt(s string, base int, bits int) (int64, error) {
	n, err := strconv.ParseInt(s, base, bits)
	if err != nil {
		ne, ok := err.(*strconv.NumError)
		switch {
		case ok && ne.Err == strconv.ErrRange:
			return 0, OutOfRange{Text: s}
		case ok && ne.Err == strconv.ErrSyntax:
			return 0, Malformed{Text: s}
		}
		panic("strconv.ParseInt: " + err.Error())
	}
	return n, nil
}

// StrconvParseFloat is strconv.ParseFloat at 64 bits.
//
//ergo:pure
func StrconvParseFloat(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return 0, OutOfRange{Text: s}
		}
		return 0, Malformed{Text: s}
	}
	return f, nil
}

// StrconvParseBool is strconv.ParseBool.
//
//ergo:pure
func StrconvParseBool(s string) (bool, error) {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return false, Malformed{Text: s}
	}
	return b, nil
}

// StrconvFormatBool is strconv.FormatBool.
//
//ergo:pure
func StrconvFormatBool(b bool) string { return strconv.FormatBool(b) }

// StrconvFormatFloat is strconv.FormatFloat at 64 bits.
//
//ergo:pure
func StrconvFormatFloat(f float64, fmt byte, prec int) string {
	return strconv.FormatFloat(f, fmt, prec, 64)
}

// StrconvUnquote is strconv.Unquote.
//
//ergo:pure
func StrconvUnquote(s string) (string, error) {
	u, err := strconv.Unquote(s)
	if err != nil {
		return "", Malformed{Text: s}
	}
	return u, nil
}

// StrconvQuote is strconv.Quote.
//
//ergo:pure
func StrconvQuote(s string) string { return strconv.Quote(s) }
