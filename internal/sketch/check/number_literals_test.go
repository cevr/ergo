package check

import (
	"go/constant"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// numberLiteral writes v in base (2, 8, 10 or 16) as Go writes it: a
// prefix of either case for the bases other than 10, digits of either
// case, and digit separators at random between two digits and right after
// the prefix (grammar.md, Literals 1).
func numberLiteral(r *rand.Rand, v uint64, base int) string {
	digits := strconv.FormatUint(v, base)
	if base == 16 && r.IntN(2) == 0 {
		digits = strings.ToUpper(digits)
	}
	var b strings.Builder
	prefixed := false
	switch base {
	case 2:
		b.WriteString([]string{"0b", "0B"}[r.IntN(2)])
		prefixed = true
	case 8:
		b.WriteString([]string{"0o", "0O"}[r.IntN(2)])
		prefixed = true
	case 16:
		b.WriteString([]string{"0x", "0X"}[r.IntN(2)])
		prefixed = true
	}
	if prefixed && r.IntN(8) == 0 {
		b.WriteByte('_')
	}
	for i := range len(digits) {
		if i > 0 && r.IntN(4) == 0 {
			b.WriteByte('_')
		}
		b.WriteByte(digits[i])
	}
	return b.String()
}

// TestNumberLiteralValues (L1 acceptance): over generated values and
// bases, the constant the checker records for a literal equals Go's
// strconv.ParseInt(text, 0, 64) of the same text. A failing seed prints
// the literal.
func TestNumberLiteralValues(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x4c31))
		var lits []string
		for range 64 {
			var v uint64
			switch r.IntN(4) {
			case 0:
				v = uint64(r.IntN(300))
			case 1:
				v = uint64(1)<<r.IntN(63) - uint64(r.IntN(2))
			default:
				v = r.Uint64() >> 1
			}
			lits = append(lits, numberLiteral(r, v, []int{2, 8, 10, 16}[r.IntN(4)]))
		}
		src := "func f() []int64 {\n\treturn []int64{" + strings.Join(lits, ", ") + "}\n}\n"
		pkg, diags := CheckSource(src)
		if len(diags) > 0 {
			t.Fatalf("seed %d: want a clean check, got %s\n%s", seed, describeDiags(diags), src)
		}
		got := map[string]constant.Value{}
		for x, v := range pkg.Info.Values {
			if lit, ok := x.(*syntax.BasicLit); ok {
				got[src[lit.Pos():lit.End()]] = v
			}
		}
		for _, lit := range lits {
			want, err := strconv.ParseInt(lit, 0, 64)
			if err != nil {
				t.Fatalf("seed %d: the generator wrote %s, which Go refuses: %v", seed, lit, err)
			}
			v, ok := got[lit]
			if !ok {
				t.Fatalf("seed %d: no value for the literal %s", seed, lit)
			}
			if n, exact := constant.Int64Val(v); !exact || n != want {
				t.Fatalf("seed %d: the literal %s has the value %s, Go reads %d", seed, lit, v, want)
			}
		}
	}
}

// TestNumberLiteralTyping: a literal with a base prefix or separators is
// an integer literal like any other (196u): it takes the type its context
// expects, must fit it (S108), and a decimal literal with a leading zero
// stays S108 with or without separators.
func TestNumberLiteralTyping(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"func f() uint8 {\n\treturn 0xFF\n}\n", ""},
		{"func f() uint8 {\n\treturn 0b1111_1111\n}\n", ""},
		{"func f() float64 {\n\treturn 0x10 + 2.5\n}\n", ""},
		{"func f() int {\n\treturn 1_000_000\n}\n", ""},
		{"func f() float64 {\n\treturn 1_000.000_5\n}\n", ""},
		{"func f() duration {\n\treturn 1_500ms\n}\n", ""},
		{"func f() int64 {\n\treturn 0x7FFF_FFFF_FFFF_FFFF\n}\n", ""},
		{"func f() uint64 {\n\treturn 0xFFFF_FFFF_FFFF_FFFF\n}\n", ""},
		{"func f() uint8 {\n\treturn 0x100\n}\n", "S108"},
		{"func f() int8 {\n\treturn 0o200\n}\n", "S108"},
		{"func f() int {\n\treturn 0_7\n}\n", "leading zero"},
		{"func f() int {\n\treturn 07\n}\n", "write 0o7"},
		{"func f() duration {\n\treturn 0_5ms\n}\n", "leading zero"},
		{"func f() int {\n\treturn 0x1p4\n}\n", "S001"},
		{"func f() int {\n\treturn 0b102\n}\n", "S001"},
		{"func f() int {\n\treturn 1__0\n}\n", "S001"},
		{"func f() int {\n\treturn 0x10ms\n}\n", "S001"},
	} {
		_, diags := CheckSource(tc.src)
		got := describeDiags(diags)
		if tc.want == "" && len(diags) > 0 || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%q: got %s, want %q", tc.src, got, tc.want)
		}
	}
}
