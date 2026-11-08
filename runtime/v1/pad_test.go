package v1

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"ergo.local/prototype/internal/lawledger"
)

// padLaws restate the laws of strings' pad.ergo over the Go bodies, on
// texts with every rune length and invalid bytes (runeText), widths from
// -64 to 63 and runes of every class, valid or not (anyRune).
var padLaws = map[string]func(r *rand.Rand) (bool, any){
	"strings PadWidth": func(r *rand.Rand) (bool, any) {
		s, w, c := runeText(r), int(r.Int64N(1000))-500, anyRune(r)
		width := w % 64
		n := utf8.RuneCountInString(s)
		want := max(width, n)
		return utf8.RuneCountInString(StringsPadLeft(s, width, c)) == want && utf8.RuneCountInString(StringsPadRight(s, width, c)) == want, []any{s, width, c}
	},
	"strings PadKeepsText": func(r *rand.Rand) (bool, any) {
		s, w, c := runeText(r), int(r.Int64N(1000))-500, anyRune(r)
		width := w % 64
		return strings.HasSuffix(StringsPadLeft(s, width, c), s) && strings.HasPrefix(StringsPadRight(s, width, c), s), []any{s, width, c}
	},
	"strings CompareIsAntisymmetric": func(r *rand.Rand) (bool, any) {
		a, b := runeText(r), runeText(r)
		if r.IntN(4) == 0 {
			b = a
		}
		return StringsCompare(a, b) == -StringsCompare(b, a) && (StringsCompare(a, b) == 0) == (a == b), []any{a, b}
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

// TestStdPadMatchesGo (U2 std-strings-pad; stdlib.md, strings and strconv
// 9): PadLeft and PadRight with a space are fmt's width, "%*s" and "%-*s",
// and Compare is strings.Compare, on 10,000 seeded cases over texts with
// multi-byte runes and invalid bytes and widths from -8 to 23. It catches
// what the laws do not: a pad on the wrong side, and a Compare that keeps
// the sign of one pair but not Go's order.
func TestStdPadMatchesGo(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 2))
	for i := range 10_000 {
		s, w := runeText(r), r.IntN(32)-8
		// fmt reads a negative width as the flag "-"; a pad of a negative
		// width pads nothing.
		left, right := fmt.Sprintf("%*s", w, s), fmt.Sprintf("%-*s", w, s)
		if w < 0 {
			left, right = s, s
		}
		if got := StringsPadLeft(s, w, ' '); got != left {
			t.Fatalf("case %d: PadLeft(%q, %d, ' ') = %q; want %q", i, s, w, got, left)
		}
		if got := StringsPadRight(s, w, ' '); got != right {
			t.Fatalf("case %d: PadRight(%q, %d, ' ') = %q; want %q", i, s, w, got, right)
		}
		u := runeText(r)
		if got, want := StringsCompare(s, u), strings.Compare(s, u); got != want {
			t.Fatalf("case %d: Compare(%q, %q) = %d; Go %d", i, s, u, got, want)
		}
	}
}
