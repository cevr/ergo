package v1

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"ergo.local/prototype/internal/lawledger"
)

// padGo is Go's pad: the fmt width ("%*s", "%-*s") with any rune for the
// space, string(r) giving U+FFFD for a rune that is not valid. std's
// PadLeft and PadRight are Ergo bodies, held to it by lower's
// TestStdMatchesGo (NP4f-3).
func padGo(s string, width int, c rune, left bool) string {
	k := width - utf8.RuneCountInString(s)
	if k <= 0 {
		return s
	}
	if left {
		return strings.Repeat(string(c), k) + s
	}
	return s + strings.Repeat(string(c), k)
}

// padLaws restate the laws of strings' pad.ergo over padGo, on texts with
// every rune length and invalid bytes (runeText), widths from -64 to 63
// and runes of every class, valid or not (anyRune).
var padLaws = map[string]func(r *rand.Rand) (bool, any){
	"strings PadWidth": func(r *rand.Rand) (bool, any) {
		s, w, c := runeText(r), int(r.Int64N(1000))-500, anyRune(r)
		width := w % 64
		n := utf8.RuneCountInString(s)
		want := max(width, n)
		return utf8.RuneCountInString(padGo(s, width, c, true)) == want && utf8.RuneCountInString(padGo(s, width, c, false)) == want, []any{s, width, c}
	},
	"strings PadKeepsText": func(r *rand.Rand) (bool, any) {
		s, w, c := runeText(r), int(r.Int64N(1000))-500, anyRune(r)
		width := w % 64
		return strings.HasSuffix(padGo(s, width, c, true), s) && strings.HasPrefix(padGo(s, width, c, false), s), []any{s, width, c}
	},
	"strings CompareIsAntisymmetric": func(r *rand.Rand) (bool, any) {
		a, b := runeText(r), runeText(r)
		if r.IntN(4) == 0 {
			b = a
		}
		return strings.Compare(a, b) == -strings.Compare(b, a) && (strings.Compare(a, b) == 0) == (a == b), []any{a, b}
	},
}

var padLawStatements = map[string]string{
	"strings PadWidth":               `PadWidth(s string, w int, r rune) { let width = w % 64 let n = utf8.RuneCount(s) let want = if width > n { width } else { n } utf8.RuneCount(PadLeft(s, width, r)) == want && utf8.RuneCount(PadRight(s, width, r)) == want }`,
	"strings PadKeepsText":           `PadKeepsText(s string, w int, r rune) { let width = w % 64 HasSuffix(PadLeft(s, width, r), s) && HasPrefix(PadRight(s, width, r), s) }`,
	"strings CompareIsAntisymmetric": `CompareIsAntisymmetric(a string, b string) { Compare(a, b) == -Compare(b, a) && (Compare(a, b) == 0) == (a == b) }`,
}

// TestStdPadLaws runs the laws of strings' pad.ergo (stdlib.md, Std laws)
// on 10,000 seeded cases each.
func TestStdPadLaws(t *testing.T) {
	laws := map[string]lawledger.Law{}
	for name, holds := range padLaws {
		laws[name] = lawledger.Law{Statement: padLawStatements[name], Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(4, uint64(len(name))))
			for i := range 10_000 {
				if ok, c := holds(r); !ok {
					t.Errorf("%s fails on case %d: %#v", name, i, c)
					break
				}
			}
		}}
	}
	lawledger.Run(t, laws)
}
