package v1

import (
	"errors"
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
		if UTF8ValidString(s) != utf8.ValidString(s) {
			t.Fatalf("case %d: ValidString(%q)", i, s)
		}
		repl := []string{"\uFFFD", "", "?", "\xff"}[r.IntN(4)]
		if got, want := StringsToValidUTF8(s, repl), strings.ToValidUTF8(s, repl); got != want {
			t.Fatalf("case %d: ToValidUTF8(%q, %q) = %q; Go %q", i, s, repl, got, want)
		}
		c := anyRune(r)
		if UTF8RuneText(c) != string(c) || UTF8RuneLen(c) != utf8.RuneLen(c) || UTF8ValidRune(c) != utf8.ValidRune(c) {
			t.Fatalf("case %d: rune %d: RuneText %q, RuneLen %d, ValidRune %v", i, c, UTF8RuneText(c), UTF8RuneLen(c), UTF8ValidRune(c))
		}
		prefix := []byte(s)
		if got, want := UTF8AppendRune(prefix[:len(prefix):len(prefix)], c), utf8.AppendRune([]byte(s), c); string(got) != string(want) {
			t.Fatalf("case %d: AppendRune(%q, %d) = %q; Go %q", i, s, c, got, want)
		}
		n := int64(r.Uint64())
		base := 2 + r.IntN(35)
		if got := StrconvFormatInt(n, base); got != strconv.FormatInt(n, base) {
			t.Fatalf("case %d: FormatInt(%d, %d) = %q", i, n, base, got)
		}
		f, g := anyFloat(r), anyFloat(r)
		same := func(name string, got, want float64) {
			t.Helper()
			if math.Float64bits(got) != math.Float64bits(want) && !(math.IsNaN(got) && math.IsNaN(want)) {
				t.Fatalf("case %d: %s(%v) = %v; Go %v", i, name, f, got, want)
			}
		}
		same("Abs", MathAbs(f), math.Abs(f))
		same("Ceil", MathCeil(f), math.Ceil(f))
		same("Floor", MathFloor(f), math.Floor(f))
		same("Trunc", MathTrunc(f), math.Trunc(f))
		same("Round", MathRound(f), math.Round(f))
		same("Sqrt", MathSqrt(f), math.Sqrt(f))
		same("Pow", MathPow(f, g), math.Pow(f, g))
		same("Mod", MathMod(f, g), math.Mod(f, g))
		sign := r.IntN(3) - 1
		if MathIsNaN(f) != math.IsNaN(f) || MathIsInf(f, sign) != math.IsInf(f, sign) || MathInf(sign) != math.Inf(sign) || !math.IsNaN(MathNaN()) {
			t.Fatalf("case %d: IsNaN, IsInf or Inf of %v, %d", i, f, sign)
		}
		x := r.Uint64() >> r.IntN(65)
		k := r.IntN(200) - 100
		if BitsOnesCount(uint(x)) != bits.OnesCount(uint(x)) || BitsOnesCount64(x) != bits.OnesCount64(x) ||
			BitsLeadingZeros(uint(x)) != bits.LeadingZeros(uint(x)) || BitsLeadingZeros64(x) != bits.LeadingZeros64(x) ||
			BitsTrailingZeros(uint(x)) != bits.TrailingZeros(uint(x)) || BitsTrailingZeros64(x) != bits.TrailingZeros64(x) ||
			BitsLen(uint(x)) != bits.Len(uint(x)) || BitsLen64(x) != bits.Len64(x) ||
			BitsRotateLeft32(uint32(x), k) != bits.RotateLeft32(uint32(x), k) || BitsRotateLeft64(x, k) != bits.RotateLeft64(x, k) {
			t.Fatalf("case %d: a bits body differs from Go's on %#x, %d", i, x, k)
		}
	}
}

// runeSink and bitsSink keep the benchmarks' results live.
var (
	runeSink int
	bitsSink uint64
)

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

// BenchmarkBitsRotate is 1,000 RotateLeft32 steps of the body against
// Go's bits.RotateLeft32, which Go compiles to one instruction.
func BenchmarkBitsRotate(b *testing.B) {
	b.Run("body", func(b *testing.B) {
		for b.Loop() {
			x := uint32(0x9e3779b9)
			for k := range 1000 {
				x = BitsRotateLeft32(x, k) ^ uint32(k)
			}
			bitsSink = uint64(x)
		}
	})
	b.Run("go", func(b *testing.B) {
		for b.Loop() {
			x := uint32(0x9e3779b9)
			for k := range 1000 {
				x = bits.RotateLeft32(x, k) ^ uint32(k)
			}
			bitsSink = uint64(x)
		}
	})
}

// BenchmarkMathFloor is 1,000 Floor calls of the body against Go's
// math.Floor.
func BenchmarkMathFloor(b *testing.B) {
	b.Run("body", func(b *testing.B) {
		for b.Loop() {
			s := 0.0
			for k := range 1000 {
				s += MathFloor(float64(k) * 0.37)
			}
			bitsSink = math.Float64bits(s)
		}
	})
	b.Run("go", func(b *testing.B) {
		for b.Loop() {
			s := 0.0
			for k := range 1000 {
				s += math.Floor(float64(k) * 0.37)
			}
			bitsSink = math.Float64bits(s)
		}
	})
}

// runesLaws are the laws of std-text-runes that a Go body decides, each
// as a check over a seeded case; runesLawStatements are their ledger
// statements.
var runesLaws = map[string]func(r *rand.Rand) (bool, any){
	"utf8 DecodeInvertsRuneText": func(r *rand.Rand) (bool, any) {
		c := anyRune(r)
		if !UTF8ValidRune(c) {
			return true, c
		}
		d := UTF8DecodeRuneInString(UTF8RuneText(c))
		return d.Rune == c && d.Size == UTF8RuneLen(c) && d.Size == len(UTF8RuneText(c)), c
	},
	"utf8 InvalidRuneTextIsReplacement": func(r *rand.Rand) (bool, any) {
		c := anyRune(r)
		return UTF8ValidRune(c) || UTF8RuneText(c) == "\uFFFD", c
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
		v := StringsToValidUTF8(s, "\uFFFD")
		return UTF8ValidString(v) && (!UTF8ValidString(s) || v == s) && (UTF8ValidString(s) || StringsContains(v, "\uFFFD")), s
	},
	"strconv ParseIntInvertsFormatInt": func(r *rand.Rand) (bool, any) {
		n, b := int64(r.Uint64()), int(r.Int64())
		base := 2 + (b%35+35)%35
		back, err := StrconvParseInt(StrconvFormatInt(n, base), base, 64)
		return err == nil && back == n, []any{n, base}
	},
	"math FloorCeilBracket": func(r *rand.Rand) (bool, any) {
		f := anyFloat(r)
		if MathIsNaN(f) || MathIsInf(f, 0) {
			return true, f
		}
		return MathFloor(f) <= f && f <= MathCeil(f) && MathCeil(f)-MathFloor(f) <= 1.0, f
	},
	"math TruncTowardZero": func(r *rand.Rand) (bool, any) {
		f := anyFloat(r)
		if MathIsNaN(f) {
			return true, f
		}
		return MathAbs(MathTrunc(f)) <= MathAbs(f) && MathTrunc(MathTrunc(f)) == MathTrunc(f), f
	},
	"math AbsIsNonNegative": func(r *rand.Rand) (bool, any) {
		f := anyFloat(r)
		return MathIsNaN(f) || MathAbs(f) >= 0.0, f
	},
	// ToInt64's laws draw edgeFloat (convert_test.go), which reaches every
	// bound of the conversion and the floats next to it.
	"math ToInt64FailsOnlyOutside": func(r *rand.Rand) (bool, any) {
		f := edgeFloat(r)
		n, err := MathToInt64(f)
		if err != nil {
			var o MathOutOfRange
			return errors.As(err, &o) && (MathIsNaN(f) || f >= 9223372036854775808.0 || f < -9223372036854775808.0), f
		}
		return float64(n) == MathTrunc(f), f
	},
	"math ToInt64AgreesWithConversion": func(r *rand.Rand) (bool, any) {
		f := edgeFloat(r)
		n, err := MathToInt64(f)
		if err != nil {
			return MathIsNaN(f) && FloatToInt64(f) == 0 || f > 0.0 && FloatToInt64(f) == math.MaxInt64 || f < 0.0 && FloatToInt64(f) == math.MinInt64, f
		}
		return n == FloatToInt64(f), f
	},
	"bits RotateLeft64Inverts": func(r *rand.Rand) (bool, any) {
		x, k := r.Uint64(), int(r.Int64())
		return BitsRotateLeft64(BitsRotateLeft64(x, k), -k) == x, []any{x, k}
	},
	"bits RotateLeft32Inverts": func(r *rand.Rand) (bool, any) {
		x, k := r.Uint32(), int(r.Int64())
		return BitsRotateLeft32(BitsRotateLeft32(x, k), -k) == x, []any{x, k}
	},
	"bits RotateKeepsOnes": func(r *rand.Rand) (bool, any) {
		x, k := r.Uint64()>>r.IntN(65), int(r.Int64())
		return BitsOnesCount64(BitsRotateLeft64(x, k)) == BitsOnesCount64(x), []any{x, k}
	},
	"bits LenAndLeadingZeros": func(r *rand.Rand) (bool, any) {
		x := r.Uint64() >> r.IntN(65)
		return BitsLen64(x)+BitsLeadingZeros64(x) == 64, x
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
	"math ToInt64FailsOnlyOutside":      `ToInt64FailsOnlyOutside(f float64) { let n = ToInt64(f) catch { case OutOfRange: return IsNaN(f) || f >= 9223372036854775808.0 || f < -9223372036854775808.0 } float64(n) == Trunc(f) }`,
	"math ToInt64AgreesWithConversion":  `ToInt64AgreesWithConversion(f float64) { let n = ToInt64(f) catch { case OutOfRange: return IsNaN(f) && int64(f) == 0 || f > 0.0 && int64(f) == MaxInt64() || f < 0.0 && int64(f) == MinInt64() } n == int64(f) }`,
	"bits RotateLeft64Inverts":          `RotateLeft64Inverts(x uint64, k int) { RotateLeft64(RotateLeft64(x, k), -k) == x }`,
	"bits RotateLeft32Inverts":          `RotateLeft32Inverts(x uint32, k int) { RotateLeft32(RotateLeft32(x, k), -k) == x }`,
	"bits RotateKeepsOnes":              `RotateKeepsOnes(x uint64, k int) { OnesCount64(RotateLeft64(x, k)) == OnesCount64(x) }`,
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
