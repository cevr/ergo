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

// Decoded is utf8.Decoded, the result of DecodeRuneInString: the first
// rune of a text and its size in bytes. It is a record of a std package,
// so the lowering binds utf8.Decoded to this Go type (docs/spec/
// stdlib.md, Types 4).
type Decoded struct {
	Rune rune
	Size int
}

// UTF8DecodeRuneInString is utf8.DecodeRuneInString, its two results as
// one Decoded: (RuneError, 1) for an invalid byte and (RuneError, 0) for
// an empty text, as in Go.
//
//ergo:pure
func UTF8DecodeRuneInString(s string) Decoded {
	r, n := utf8.DecodeRuneInString(s)
	return Decoded{Rune: r, Size: n}
}

// UTF8RuneText is Go's string(r): the UTF-8 text of r, or "\uFFFD" for a
// rune that is not valid.
//
//ergo:pure
func UTF8RuneText(r rune) string { return string(r) }

// UTF8AppendRune is utf8.AppendRune.
//
//ergo:pure
func UTF8AppendRune(p []byte, r rune) []byte { return utf8.AppendRune(p, r) }

// UTF8ValidString is utf8.ValidString.
//
//ergo:pure
func UTF8ValidString(s string) bool { return utf8.ValidString(s) }

// UTF8ValidRune is utf8.ValidRune.
//
//ergo:pure
func UTF8ValidRune(r rune) bool { return utf8.ValidRune(r) }

// UTF8RuneLen is utf8.RuneLen: -1 for a rune that is not valid.
//
//ergo:pure
func UTF8RuneLen(r rune) int { return utf8.RuneLen(r) }

// StringsToValidUTF8 is strings.ToValidUTF8.
//
//ergo:pure
func StringsToValidUTF8(s string, replacement string) string {
	return strings.ToValidUTF8(s, replacement)
}

// StrconvFormatInt is strconv.FormatInt; a base outside 2..36 panics as
// Go's does, a defect.
//
//ergo:pure
func StrconvFormatInt(n int64, base int) string { return strconv.FormatInt(n, base) }

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

// The base-10 reads of Atoi and ParseInt are written out here, as
// strconv.ParseInt reads base 10 (an optional sign, then digits; the
// first digit that overflows ends the read, so a bad byte after it is not
// seen),
// so a failure costs one allocation, the typed failure, and not
// strconv's *NumError and its copy of s as well. TestStrconvBase10MatchesGo
// holds them equal to strconv on generated texts and every bit size. A pure
// body names no other function of this package, so the two loops are
// separate copies.

// StrconvAtoi is strconv.Atoi: OutOfRange for a number out of int's
// range, Malformed for any other text it refuses.
//
//ergo:pure
func StrconvAtoi(s string) (int, error) {
	i, neg := 0, false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg, i = s[0] == '-', 1
	}
	if i == len(s) {
		return 0, Malformed{Text: s}
	}
	const cutoff = ^uint64(0)/10 + 1
	const maxVal = 1<<strconv.IntSize - 1
	var n uint64
	for ; i < len(s); i++ {
		d := s[i] - '0'
		if d > 9 {
			return 0, Malformed{Text: s}
		}
		if n >= cutoff {
			return 0, OutOfRange{Text: s}
		}
		n *= 10
		n1 := n + uint64(d)
		if n1 < n || n1 > maxVal {
			return 0, OutOfRange{Text: s}
		}
		n = n1
	}
	const limit = uint64(1) << (strconv.IntSize - 1)
	if !neg && n >= limit || neg && n > limit {
		return 0, OutOfRange{Text: s}
	}
	if neg {
		return int(-n), nil
	}
	return int(n), nil
}

// StrconvParseInt is strconv.ParseInt.
//
//ergo:pure
func StrconvParseInt(s string, base int, bits int) (int64, error) {
	if base == 10 && 0 <= bits && bits <= 64 {
		if bits == 0 {
			bits = strconv.IntSize
		}
		i, neg := 0, false
		if s != "" && (s[0] == '+' || s[0] == '-') {
			neg, i = s[0] == '-', 1
		}
		if i == len(s) {
			return 0, Malformed{Text: s}
		}
		const cutoff = ^uint64(0)/10 + 1
		maxVal := uint64(1)<<uint(bits-1)<<1 - 1
		var n uint64
		for ; i < len(s); i++ {
			d := s[i] - '0'
			if d > 9 {
				return 0, Malformed{Text: s}
			}
			// An overflow ends the read at maxVal, as strconv.ParseUint
			// does; the sign check below then decides, as ParseInt's does
			// (so "-9" at 1 bit is -1, as in Go).
			if n >= cutoff {
				n = maxVal
				break
			}
			n *= 10
			n1 := n + uint64(d)
			if n1 < n || n1 > maxVal {
				n = maxVal
				break
			}
			n = n1
		}
		limit := uint64(1) << uint(bits-1)
		if !neg && n >= limit || neg && n > limit {
			return 0, OutOfRange{Text: s}
		}
		if neg {
			return -int64(n), nil
		}
		return int64(n), nil
	}
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
