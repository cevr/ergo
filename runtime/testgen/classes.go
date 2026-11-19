package testgen

import (
	"math"
	"strconv"
	"strings"
)

// The named classes of a licence run (runtime.md, Tests 7; LAWFFI r2 §3.4
// part 1): the values a run takes once each, before it draws, so that a
// law false only at an edge the drawn cases never reach is refuted. A
// drawn string has at most 25 units and a drawn float is finite, while a
// Go function changes its code path at 16, 32, 64 and 4,096 bytes, at a
// NaN, at an infinity and at a byte that is not UTF-8.
//
// Prior art: QuickCheck's and Hypothesis's generators bias towards
// boundary values (Hypothesis's st.floats yields NaN, the infinities and
// the signed zeros first), and Go's fuzzer seeds its corpus with the
// interesting values of each type; neither names which classes a run
// reached. A licence run does, since its verdict rests on it.

// A Class is one named class and the value that stands for it.
type Class[T any] struct {
	Name  string
	Value T
}

// StringClasses are a string's classes: six of its length (0, 1, 2 to 15,
// 16 to 63, 64 to 4,095, 4,096 and more), then the eight unit classes of
// Str (a quote, a backslash, a control byte, DEL, a byte that is not
// UTF-8, an astral rune, another BMP rune, printable ASCII) and a string
// that ends inside a rune.
func StringClasses() []Class[string] {
	return []Class[string]{
		{"len=0", ""},
		{"len=1", "a"},
		{"len=2..15", "abcdefgh"},
		{"len=16..63", strings.Repeat("ab", 16)},
		{"len=64..4095", strings.Repeat("abcd", 50)},
		{"len>=4096", strings.Repeat("abcd", 1024)},
		{"quote", `a"b`},
		{"backslash", `a\b`},
		{"control", "a\x01b"},
		{"DEL", "a\x7fb"},
		{"not UTF-8", "a\xffb"},
		{"astral", "a\U0001D11Eb"},
		{"BMP", "a\u00e9\u4e16b"},
		{"printable ASCII", "a~ Z"},
		{"ends inside a rune", "a\xe4\xb8"},
	}
}

// IntClasses are an integer's classes at a width of bits (0: the target
// int, 64 bits) and signedness: 0, 1, -1 (for a signed type) and the
// bounds of each Go width that the type holds. Each value is the
// integer's bits at its width.
func IntClasses(width uint, signed bool) []Class[uint64] {
	if width == 0 {
		width = 64
	}
	mask := ^uint64(0) >> (64 - width)
	out := []Class[uint64]{{"=0", 0}, {"=1", 1}}
	if signed {
		out = append(out, Class[uint64]{"=-1", mask})
	}
	for _, w := range []uint{8, 16, 32, 64} {
		if w > width {
			break
		}
		if signed {
			hi := uint64(1)<<(w-1) - 1
			out = append(out, Class[uint64]{"=" + strconv.FormatUint(hi, 10), hi}, Class[uint64]{"=-" + strconv.FormatUint(hi+1, 10), -(hi + 1) & mask})
		} else {
			hi := ^uint64(0) >> (64 - w)
			out = append(out, Class[uint64]{"=" + strconv.FormatUint(hi, 10), hi})
		}
	}
	return out
}

// FloatClasses are a float's classes at bits (32 or 64): both zeros, both
// infinities, a NaN, a subnormal, and the largest and the smallest finite
// value.
func FloatClasses(bits int) []Class[float64] {
	maxF, sub := math.MaxFloat64, math.SmallestNonzeroFloat64
	if bits == 32 {
		maxF, sub = math.MaxFloat32, math.SmallestNonzeroFloat32
	}
	return []Class[float64]{
		{"=+0", 0}, {"=-0", math.Copysign(0, -1)},
		{"=+Inf", math.Inf(1)}, {"=-Inf", math.Inf(-1)},
		{"=NaN", math.NaN()},
		{"=subnormal", sub},
		{"=max", maxF}, {"=-max", -maxF},
	}
}

// SliceLengths are a slice's length classes, as a string's: a length that
// stands for each.
func SliceLengths() []Class[int] {
	return []Class[int]{{"len=0", 0}, {"len=1", 1}, {"len=2..15", 8}, {"len=16..63", 32}, {"len=64..4095", 200}, {"len>=4096", 4096}}
}
