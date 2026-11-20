package lower

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"unicode"
)

// stdGoSQ5Cases are SQ5's strings and utf8 functions (STD-SURVEY's
// StringsIsGo), each against Go's own: TrimFunc and FieldsFunc under
// utf8.IsSpace, utf8.IsDigit and a function literal; utf8.IsDigit, IsLetter and IsSpace over runes of
// every class (numbers that are no digit: '²', '½', 'Ⅰ'), invalid runes
// included. Texts mix ASCII, Latin-1 and wider
// space, Arabic-Indic and full-width digits, U+FFFD and invalid bytes.
// IndexAny and LastIndexAny are law IndexAnyFinds' (TestStdTextRuns2).
// Mutants (std ledger): sq5-trimfunc-left, sq5-fieldsfunc-is-fields,
// sq5-isdigit-is-number, sq5-isletter-is-upper, sq5-isspace-ascii.
func stdGoSQ5Cases(r *rand.Rand, n int) []bodyCase {
	with := func(c bodyCase) bodyCase {
		c.decls = stdGo2Decls
		return c
	}
	text := np4fErgoString
	pieces := append([]string{"1", "09", "\u0663", "\uff15", "\u2003", "\u0085", "\n", "\ufffd", "x1,"}, stdGo2Pieces...)
	genText := func(r *rand.Rand) string {
		var b strings.Builder
		for range r.IntN(9) {
			b.WriteString(pieces[r.IntN(len(pieces))])
		}
		return b.String()
	}
	imp := []string{"strings", "utf8"}
	texts := []string{"", " ", "  a b  ", "\u2003x\u0085", "12ab34", "\u0663\u0664x\uff15", ",a,,b,", "\xff \xff", "\ufffd"}
	textCase := func(name, call string, ref func(string) string) bodyCase {
		return with(bodyRows(name, imp, "s string", call, texts, n, r, genText, text, ref))
	}
	sep := func(c rune) bool { return c == ',' || c > 0x7f }
	runes := []rune{'0', '9', 'a', 'Z', ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xa0, 0x1680, 0x2000, 0x2028, 0x3000, 0x663, 0xff15, 0xb2, 0xbd, 0x2160, 0xe9, 0x4e16, 0xfffd, -1, 0xd800, 0x110000}
	runeCase := func(name string, ref func(rune) bool) bodyCase {
		return with(bodyRows("utf8."+name, imp, "r rune", `"\(utf8.`+name+`(r))"`, runes, n, r, np4fGenRune, np4fRuneArg, func(v rune) string { return strconv.FormatBool(ref(v)) }))
	}
	return []bodyCase{
		textCase("strings.TrimFunc", `bs(strings.TrimFunc(s, utf8.IsSpace)) + "|" + bs(strings.TrimFunc(s, utf8.IsDigit)) + "|" + bs(strings.TrimFunc(s, func(c rune) bool { c == ',' || c > 0x7f }))`, func(s string) string {
			return stdGo2Bytes(strings.TrimFunc(s, unicode.IsSpace)) + "|" + stdGo2Bytes(strings.TrimFunc(s, unicode.IsDigit)) + "|" + stdGo2Bytes(strings.TrimFunc(s, sep))
		}),
		textCase("strings.FieldsFunc", `qs(strings.FieldsFunc(s, utf8.IsSpace)) + "/" + qs(strings.FieldsFunc(s, utf8.IsDigit)) + "/" + qs(strings.FieldsFunc(s, func(c rune) bool { c == ',' || c > 0x7f }))`, func(s string) string {
			return stdGo2List(strings.FieldsFunc(s, unicode.IsSpace)) + "/" + stdGo2List(strings.FieldsFunc(s, unicode.IsDigit)) + "/" + stdGo2List(strings.FieldsFunc(s, sep))
		}),
		runeCase("IsDigit", unicode.IsDigit),
		runeCase("IsLetter", unicode.IsLetter),
		runeCase("IsSpace", unicode.IsSpace),
	}
}
