package v1

import (
	"math"
	"math/bits"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"ergo.local/prototype/internal/lawledger"
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

// runesLaws are the laws of std-text-runes that a Go body decides (a
// runtime/v1 body, or Go's own function where std binds it through import
// go), each as a check over a seeded case; runesLawStatements are their ledger
// statements.
var runesLaws = map[string]func(r *rand.Rand) (bool, any){
	"utf8 DecodeInvertsRuneText": func(r *rand.Rand) (bool, any) {
		c := anyRune(r)
		if !utf8.ValidRune(c) {
			return true, c
		}
		d := UTF8DecodeRuneInString(UTF8RuneText(c))
		return d.Rune == c && d.Size == utf8.RuneLen(c) && d.Size == len(UTF8RuneText(c)), c
	},
	"utf8 InvalidRuneTextIsReplacement": func(r *rand.Rand) (bool, any) {
		c := anyRune(r)
		return utf8.ValidRune(c) || UTF8RuneText(c) == "\uFFFD", c
	},
	"utf8 DecodeMovesOn": func(r *rand.Rand) (bool, any) {
		s := runeText(r)
		d := UTF8DecodeRuneInString(s)
		if len(s) == 0 {
			return d.Size == 0, s
		}
		return d.Size >= 1 && d.Size <= 4 && d.Size <= len(s), s
	},
	"strings ToValidUTF8IsValid": func(r *rand.Rand) (bool, any) {
		s := runeText(r)
		v := strings.ToValidUTF8(s, "\uFFFD")
		return utf8.ValidString(v) && (!utf8.ValidString(s) || v == s) && (utf8.ValidString(s) || strings.Contains(v, "\uFFFD")), s
	},
	"strconv ParseIntInvertsFormatInt": func(r *rand.Rand) (bool, any) {
		n, b := int64(r.Uint64()), int(r.Int64())
		base := 2 + (b%35+35)%35
		back, err := StrconvParseInt(strconv.FormatInt(n, base), base, 64)
		return err == nil && back == n, []any{n, base}
	},
	"math FloorCeilBracket": func(r *rand.Rand) (bool, any) {
		f := anyFloat(r)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return true, f
		}
		return math.Floor(f) <= f && f <= math.Ceil(f) && math.Ceil(f)-math.Floor(f) <= 1.0, f
	},
	"math TruncTowardZero": func(r *rand.Rand) (bool, any) {
		f := anyFloat(r)
		if math.IsNaN(f) {
			return true, f
		}
		return math.Abs(math.Trunc(f)) <= math.Abs(f) && math.Trunc(math.Trunc(f)) == math.Trunc(f), f
	},
	// The two FMA laws round each product with float64(…), as Ergo's
	// lowering does (types.md, Operators 5), so no host fuses the unfused
	// side of the law.
	"math FMAWithinTheUnfusedError": func(r *rand.Rand) (bool, any) {
		a, b, c := int(r.Int64()), int(r.Int64()), int(r.Int64())
		x := float64(a%1000003) / 3.0
		y := float64(b%1000033) / 7.0
		p := float64(x * y)
		z := float64(c%1009)/11.0 - p
		unfused := p + z
		fused := math.FMA(x, y, z)
		eps := 1.1102230246251565e-16
		return math.Abs(fused-unfused) <= float64(float64(2.0*eps)*(math.Abs(p)+math.Abs(unfused)+math.Abs(fused))), []any{a, b, c}
	},
	"math FMARoundsOnce": func(r *rand.Rand) (bool, any) {
		k := int(r.Int64())
		d := float64(float64((k%1000+1000)%1000+1) * 9.313225746154785e-10)
		return math.FMA(1.0+d, 1.0-d, -1.0) == -float64(d*d), k
	},
	"math AbsIsNonNegative": func(r *rand.Rand) (bool, any) {
		f := anyFloat(r)
		return math.IsNaN(f) || math.Abs(f) >= 0.0, f
	},
	"bits RotateLeft64Inverts": func(r *rand.Rand) (bool, any) {
		x, k := r.Uint64(), int(r.Int64())
		return bits.RotateLeft64(bits.RotateLeft64(x, k), -k) == x, []any{x, k}
	},
	"bits RotateLeft32Inverts": func(r *rand.Rand) (bool, any) {
		x, k := r.Uint32(), int(r.Int64())
		return bits.RotateLeft32(bits.RotateLeft32(x, k), -k) == x, []any{x, k}
	},
	"bits RotateKeepsOnes": func(r *rand.Rand) (bool, any) {
		x, k := r.Uint64()>>r.IntN(65), int(r.Int64())
		return bits.OnesCount64(bits.RotateLeft64(x, k)) == bits.OnesCount64(x), []any{x, k}
	},
	"bits SubUndoesAdd64": func(r *rand.Rand) (bool, any) {
		xh, xl, y := r.Uint64(), r.Uint64(), r.Uint64()
		lo, c := BitsAdd64(xl, y, 0)
		hi, _ := BitsAdd64(xh, 0, c)
		l2, b := BitsSub64(lo, y, 0)
		h2, _ := BitsSub64(hi, 0, b)
		return l2 == xl && h2 == xh, []any{xh, xl, y}
	},
	"bits DivUndoesMul64": func(r *rand.Rand) (bool, any) {
		x, y, rr := r.Uint64(), r.Uint64()>>r.IntN(64), r.Uint64()
		if y == 0 {
			return true, []any{x, y, rr}
		}
		hi, lo := BitsMul64(x, y)
		rem := rr % y
		lo2, c := BitsAdd64(lo, rem, 0)
		q, back := BitsDiv64(hi+c, lo2, y)
		return q == x && back == rem, []any{x, y, rr}
	},
	"bits LenAndLeadingZeros": func(r *rand.Rand) (bool, any) {
		x := r.Uint64() >> r.IntN(65)
		return bits.Len64(x)+bits.LeadingZeros64(x) == 64, x
	},
}

var runesLawStatements = map[string]string{
	"utf8 DecodeInvertsRuneText":        `DecodeInvertsRuneText(r rune) { if !ValidRune(r) { return true } let d = DecodeRuneInString(RuneText(r)) d.Rune == r && d.Size == RuneLen(r) && d.Size == len(RuneText(r)) }`,
	"utf8 InvalidRuneTextIsReplacement": `InvalidRuneTextIsReplacement(r rune) { ValidRune(r) || RuneText(r) == "\uFFFD" }`,
	"utf8 DecodeMovesOn":                `DecodeMovesOn(s string) { let d = DecodeRuneInString(s) if len(s) == 0 { return d.Size == 0 } d.Size >= 1 && d.Size <= 4 && d.Size <= len(s) }`,
	"strings ToValidUTF8IsValid":        `ToValidUTF8IsValid(s string) { let v = ToValidUTF8(s, "\uFFFD") utf8.ValidString(v) && (!utf8.ValidString(s) || v == s) && (utf8.ValidString(s) || Contains(v, "\uFFFD")) }`,
	"strconv ParseIntInvertsFormatInt":  `ParseIntInvertsFormatInt(n int64, b int) { let base = 2 + (b%35+35)%35 try ParseInt(FormatInt(n, base), base, 64) == n }`,
	"math FloorCeilBracket":             `FloorCeilBracket(f float64) { if IsNaN(f) || IsInf(f, 0) { return true } Floor(f) <= f && f <= Ceil(f) && Ceil(f)-Floor(f) <= 1.0 }`,
	"math TruncTowardZero":              `TruncTowardZero(f float64) { if IsNaN(f) { return true } Abs(Trunc(f)) <= Abs(f) && Trunc(Trunc(f)) == Trunc(f) }`,
	"math AbsIsNonNegative":             `AbsIsNonNegative(f float64) { IsNaN(f) || Abs(f) >= 0.0 }`,
	"math FMAWithinTheUnfusedError":     `FMAWithinTheUnfusedError(a int, b int, c int) { let x = float64(a%1000003) / 3.0 let y = float64(b%1000033) / 7.0 let p = x * y let z = float64(c%1009)/11.0 - p let unfused = p + z let fused = FMA(x, y, z) let eps = 1.1102230246251565e-16 Abs(fused-unfused) <= 2.0*eps*(Abs(p)+Abs(unfused)+Abs(fused)) }`,
	"math FMARoundsOnce":                `FMARoundsOnce(k int) { let d = float64((k%1000+1000)%1000+1) * 9.313225746154785e-10 FMA(1.0+d, 1.0-d, -1.0) == -(d * d) }`,
	"bits RotateLeft64Inverts":          `RotateLeft64Inverts(x uint64, k int) { RotateLeft64(RotateLeft64(x, k), -k) == x }`,
	"bits RotateLeft32Inverts":          `RotateLeft32Inverts(x uint32, k int) { RotateLeft32(RotateLeft32(x, k), -k) == x }`,
	"bits RotateKeepsOnes":              `RotateKeepsOnes(x uint64, k int) { OnesCount64(RotateLeft64(x, k)) } == { OnesCount64(x) }`,
	"bits SubUndoesAdd64":               `SubUndoesAdd64(xh uint64, xl uint64, y uint64) { let (lo, c) = Add64(xl, y, 0) let (hi, _) = Add64(xh, 0, c) let (l2, b) = Sub64(lo, y, 0) let (h2, _) = Sub64(hi, 0, b) l2 == xl && h2 == xh }`,
	"bits DivUndoesMul64":               `DivUndoesMul64(x uint64, y uint64, r uint64) { if y == 0 { return true } let (hi, lo) = Mul64(x, y) let rem = r % y let (lo2, c) = Add64(lo, rem, 0) let (q, back) = Div64(hi+c, lo2, y) q == x && back == rem }`,
	"bits LenAndLeadingZeros":           `LenAndLeadingZeros(x uint64) { Len64(x)+LeadingZeros64(x) == 64 }`,
}

// TestStdRunesLaws runs the laws of std-text-runes over Go bodies, each on
// 10,000 seeded cases; it is their ledger runner.
func TestStdRunesLaws(t *testing.T) {
	laws := map[string]lawledger.Law{}
	for name, holds := range runesLaws {
		laws[name] = lawledger.Law{Statement: runesLawStatements[name], Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(2, uint64(len(name))))
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
