package lower

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"math/big"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestFloatLiteralTinyLowers is the smallest repro of the float literal
// bug (wf10 PA2 gaps.md, tomlparse item 6): `2.5e-300` lowered to the
// exact fraction num.0/den, whose denominator has about a thousand bits,
// and Go refused it as "constant overflow".
func TestFloatLiteralTinyLowers(t *testing.T) {
	o := mustLower(t, "float_tiny", "func main() {\n\tlet x float64 = 2.5e-300\n\tlet _ = x\n}\n")
	typecheckGo(t, []*lowered{o})
}

// floatEdges are literals at the edges of float64 and float32: the
// smallest subnormal, the largest subnormal, the smallest normal, the
// largest finite value, halfway cases and their neighbours.
var floatEdges = []string{
	"4.9406564584124654e-324", "5e-324", "2.2250738585072009e-308", "2.2250738585072014e-308",
	"1.7976931348623157e308", "1.7976931348623158e308", "9007199254740993.0", "0.1", "2.5e-300",
	"1.401298464324817e-45", "1.1754942e-38", "1.17549435e-38", "3.4028234663852886e38", "16777217.0",
	"0.30000000000000004", "123456789012345678901234567890.123456789e-200", "1e-320", "1e23",
	float32DoubleRounding(),
}

// float32DoubleRounding is h + 10^-200, where h is a float32 halfway
// point above 1 whose shortest float64 decimal is below h. As a float32
// the literal rounds up, past h. Rounded to float64 first it is h, whose
// shortest decimal then rounds down as a float32. Its 200 digits put its
// numerator past Go's 512 bits, so it takes the decimal spelling.
func float32DoubleRounding() string {
	for i := int64(0); ; i++ {
		h := new(big.Rat).SetFrac64(1<<24+2*i+1, 1<<24) // 1 + (2i+1)*2^-24
		hf, _ := h.Float64()
		short, _ := new(big.Rat).SetString(strconv.FormatFloat(hf, 'g', -1, 64))
		if short.Cmp(h) < 0 {
			return h.FloatString(60) + strings.Repeat("0", 139) + "1"
		}
	}
}

// floatLiteral is the literal of a seed: seeds 1 to len(floatEdges) are
// the edges in order; a later seed draws random digits (up to 40, with a
// fraction) and an exponent from -340 to 308.
func floatLiteral(seed uint64, r *rand.Rand) string {
	if seed >= 1 && seed <= uint64(len(floatEdges)) {
		return floatEdges[seed-1]
	}
	var b strings.Builder
	b.WriteByte(byte('1' + r.IntN(9)))
	for range r.IntN(40) {
		b.WriteByte(byte('0' + r.IntN(10)))
	}
	b.WriteByte('.')
	b.WriteByte(byte('0' + r.IntN(10)))
	for range r.IntN(20) {
		b.WriteByte(byte('0' + r.IntN(10)))
	}
	return b.String() + "e" + strconv.Itoa(r.IntN(649)-340)
}

// TestFloatLiteralsLowerExactly is the property of float literals at the
// edges: for each literal and each of float64 and float32, the Go text
// the lowering writes type-checks in go/types (which holds Go's limit on
// the size of a constant, as gc does) and is the literal's value
// correctly rounded to the type, as strconv.ParseFloat rounds it. The
// literals that overflow the type or round to zero are skipped (the
// checker's business). A failing seed prints the Ergo source, both values
// and the replay command. One program of all the literals is lowered and
// type-checked too, so the property reaches the spelling through the
// lowering of a let.
func TestFloatLiteralsLowerExactly(t *testing.T) {
	seeds := 200
	first := uint64(1)
	if s := os.Getenv("FLOATLIT_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("FLOATLIT_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	var src strings.Builder
	src.WriteString("func main() {\n")
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0xf107))
		lit := floatLiteral(seed, r)
		v := constant.MakeFromLiteral(lit, token.FLOAT, 0)
		if v.Kind() == constant.Unknown {
			t.Fatalf("seed %d: %s is not a Go float literal", seed, lit)
		}
		for _, k := range []struct {
			kind check.BasicKind
			name string
			bits int
		}{{check.Float64, "float64", 64}, {check.Float32, "float32", 32}} {
			want, err := strconv.ParseFloat(lit, k.bits)
			if err != nil || want == 0 {
				continue // out of the type's range: the checker refuses or rounds it to 0
			}
			ergo := fmt.Sprintf("let x %s = %s", k.name, lit)
			spelled := floatSpelling(v, k.kind)
			tv, err := types.Eval(token.NewFileSet(), nil, token.NoPos, k.name+"("+spelled+")")
			replay := fmt.Sprintf("replay: FLOATLIT_SEED=%d go test -count=1 -run '^TestFloatLiteralsLowerExactly$' ./internal/sketch/lower", seed)
			if err != nil {
				t.Fatalf("seed %d: %s lowers to %s(%s), which Go refuses: %v\n%s", seed, ergo, k.name, firstLines(spelled, 3), err, replay)
			}
			got, _ := constant.Float64Val(tv.Value)
			if math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("seed %d: %s lowers to %s(%s), which is %v; strconv.ParseFloat reads %v\n%s", seed, ergo, k.name, firstLines(spelled, 3), got, want, replay)
			}
			fmt.Fprintf(&src, "\tlet x%d_%d %s = %s\n\tlet _ = x%d_%d\n", seed, k.bits, k.name, lit, seed, k.bits)
		}
	}
	src.WriteString("}\n")
	typecheckGo(t, []*lowered{mustLower(t, "float_literals", src.String())})
}
