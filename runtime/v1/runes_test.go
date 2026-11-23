package v1

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

// runeText is a short text of valid runes of every length (1 to 4
// bytes), the replacement rune, and invalid bytes: a lone continuation
// byte, a cut 2-, 3- and 4-byte sequence, an overlong form, a surrogate's
// bytes and 0xff.
func runeText(r *rand.Rand) string {
	parts := []string{"a", "Z", "\x00", "\x7f", "é", "ß", "世", "€", "\U0001F600", "\U0010FFFF", "\uFFFD",
		"\x80", "\xc3", "\xe4\xb8", "\xf0\x9f\x98", "\xc0\xaf", "\xed\xa0\x80", "\xff", "\xf4\x90\x80\x80"}
	var b strings.Builder
	for range r.IntN(8) {
		b.WriteString(parts[r.IntN(len(parts))])
	}
	return b.String()
}

// anyRune is a rune of every class: ASCII, 2-, 3- and 4-byte, a
// surrogate, the largest, past the largest, and negative.
func anyRune(r *rand.Rand) rune {
	switch r.IntN(8) {
	case 0:
		return rune(r.IntN(0x80))
	case 1:
		return rune(0x80 + r.IntN(0x800-0x80))
	case 2:
		return rune(0x800 + r.IntN(0x10000-0x800))
	case 3:
		return rune(0x10000 + r.IntN(0x110000-0x10000))
	case 4:
		return rune(0xD800 + r.IntN(0x800))
	case 5:
		return []rune{0x10FFFF, 0x110000, utf8.RuneError, 0}[r.IntN(4)]
	case 6:
		return rune(r.Int32())
	}
	return -rune(1 + r.IntN(1000))
}

// anyFloat is a float64 of every class: small and large, integral,
// halves, negative zero, the infinities and NaN.
func anyFloat(r *rand.Rand) float64 {
	switch r.IntN(7) {
	case 0:
		return float64(r.IntN(2001)-1000) / 4
	case 1:
		return (r.Float64() - 0.5) * 1e18
	case 2:
		return math.Float64frombits(r.Uint64())
	case 3:
		return []float64{math.Inf(1), math.Inf(-1), math.NaN(), math.Copysign(0, -1), 0, 0.5, -0.5, 2.5}[r.IntN(8)]
	}
	return (r.Float64() - 0.5) * 100
}

// TestStdRunesMatchGo (std-text-runes): each new body of utf8, strings,
// strconv, math and bits gives Go's own function's answer on 10,000
// seeded cases over texts with invalid UTF-8, runes of every class,
// floats of every class and unsigned ints of every bit pattern.
func TestStdRunesMatchGo(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	for i := range 10000 {
		s := runeText(r)
		gr, gn := utf8.DecodeRuneInString(s)
		if d := UTF8DecodeRuneInString(s); d != (Decoded{Rune: gr, Size: gn}) {
			t.Fatalf("case %d: DecodeRuneInString(%q) = %#v; Go %q %d", i, s, d, gr, gn)
		}
		repl := []string{"\uFFFD", "", "?", "\xff"}[r.IntN(4)]
		c := anyRune(r)
		if UTF8RuneText(c) != string(c) {
			t.Fatalf("case %d: rune %d: RuneText %q", i, c, UTF8RuneText(c))
		}
		prefix := []byte(s)
		if got, want := UTF8AppendRune(prefix[:len(prefix):len(prefix)], c), utf8.AppendRune([]byte(s), c); string(got) != string(want) {
			t.Fatalf("case %d: AppendRune(%q, %d) = %q; Go %q", i, s, c, got, want)
		}
		f, g := anyFloat(r), anyFloat(r)
		same := func(name string, got, want float64) {
			t.Helper()
			if math.Float64bits(got) != math.Float64bits(want) && !(math.IsNaN(got) && math.IsNaN(want)) {
				t.Fatalf("case %d: %s(%v) = %v; Go %v", i, name, f, got, want)
			}
		}
		same("Max", CmpMax(f, g), max(f, g))
		same("Min", CmpMin(f, g), min(f, g))
		cut := runeText(r)
		var gb strings.Builder
		var eb []byte
		for _, part := range []string{s, cut, repl} {
			if n := r.IntN(8); n%2 == 0 {
				gb.Grow(n)
				eb = StringsBuilderGrow(eb, n)
				if cap(eb)-len(eb) < n {
					t.Fatalf("case %d: Grow(%d) left room %d", i, n, cap(eb)-len(eb))
				}
			}
			gb.WriteString(part)
			eb = StringsBuilderAppend(eb, part)
			c := byte(r.IntN(256))
			gb.WriteByte(c)
			eb = StringsBuilderAppendByte(eb, c)
		}
		if StringsBuilderText(eb) != gb.String() || len(eb) != gb.Len() {
			t.Fatalf("case %d: Builder text %q (len %d); Go %q (len %d)", i, StringsBuilderText(eb), len(eb), gb.String(), gb.Len())
		}
		// The owned form shares the consumed buffer: the same text, and
		// an append past its length (all a Builder writes) leaves it.
		owned := StringsBuilderTextOwned(eb)
		eb = StringsBuilderAppend(eb, repl)
		if owned != gb.String() {
			t.Fatalf("case %d: owned Builder text %q; Go %q", i, owned, gb.String())
		}
	}
}

// runeSink keeps the benchmark's result live.
var runeSink int

// BenchmarkUTF8Decode walks a mixed text of 1,000 runes rune by rune with
// the DecodeRuneInString body (a Decoded record) and, as the hand-Go
// control, with Go's two results.
func BenchmarkUTF8Decode(b *testing.B) {
	text := strings.Repeat("aé世\U0001F600", 250)
	b.Run("body", func(b *testing.B) {
		for b.Loop() {
			n := 0
			for i := 0; i < len(text); {
				d := UTF8DecodeRuneInString(text[i:])
				n += int(d.Rune)
				i += d.Size
			}
			runeSink = n
		}
	})
	b.Run("go", func(b *testing.B) {
		for b.Loop() {
			n := 0
			for i := 0; i < len(text); {
				r, size := utf8.DecodeRuneInString(text[i:])
				n += int(r)
				i += size
			}
			runeSink = n
		}
	})
}
