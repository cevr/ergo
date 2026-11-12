package rt

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
)

// textErr is an error with a value receiver; its Error panics when panics
// is set, as an Error method of a program may.
type textErr struct {
	text   string
	panics any
}

func (e textErr) Error() string {
	if e.panics != nil {
		panic(e.panics)
	}
	return e.text
}

// derefErr is an error whose Error reads its receiver: a nil *derefErr
// panics in it, which fmt writes as "<nil>".
type derefErr struct{ text string }

func (e *derefErr) Error() string { return e.text }

// textStringer is a Stringer that is no error.
type textStringer struct{ text string }

func (s textStringer) String() string { return s.text }

// genText is a text with runes of every class and invalid UTF-8.
func genText(r *rand.Rand) string {
	parts := []string{"", "a", "é", "\xff", "\x00", "\n", "%v", "日本", "\u2028", "\U0001F600", "<nil>"}
	var s string
	for range r.IntN(4) {
		s += parts[r.IntN(len(parts))]
	}
	return s
}

// genFloat is a float of every class: zero, negative zero, subnormal,
// integral, huge, infinite and NaN.
func genFloat(r *rand.Rand) float64 {
	switch r.IntN(6) {
	case 0:
		return []float64{0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1), math.NaN(), math.SmallestNonzeroFloat64, math.MaxFloat64}[r.IntN(7)]
	case 1:
		return float64(r.Int64N(1<<53)) * []float64{1, -1}[r.IntN(2)]
	case 2:
		return math.Float64frombits(r.Uint64())
	default:
		return (r.Float64() - 0.5) * math.Pow(10, float64(r.IntN(60)-30))
	}
}

// genValue is a value an Ergo program can raise or fail with: a text, a
// number of each Go kind, a boolean, nil, an error (one whose Error
// panics, a nil pointer one, a Go runtime error) and a Stringer.
func genValue(r *rand.Rand) any {
	switch r.IntN(20) {
	case 0:
		return nil
	case 1:
		return genText(r)
	case 2:
		return r.IntN(2) == 0
	case 3:
		return int(r.Int64())
	case 4:
		return int8(r.Int64())
	case 5:
		return int16(r.Int64())
	case 6:
		return int32(r.Int64())
	case 7:
		return r.Int64() - r.Int64()
	case 8:
		return uint(r.Uint64())
	case 9:
		return uint8(r.Uint64())
	case 10:
		return uint16(r.Uint64())
	case 11:
		return uint32(r.Uint64())
	case 12:
		return []any{r.Uint64(), uintptr(r.Uint64())}[r.IntN(2)]
	case 13:
		return float32(genFloat(r))
	case 14:
		return genFloat(r)
	case 15:
		return errors.New(genText(r))
	case 16:
		var panics any
		if r.IntN(2) == 0 {
			panics = []any{genText(r), r.IntN(100), errors.New(genText(r))}[r.IntN(3)]
		}
		return textErr{text: genText(r), panics: panics}
	case 17:
		if r.IntN(2) == 0 {
			return (*derefErr)(nil)
		}
		return &derefErr{genText(r)}
	case 18:
		return runtimeError(r.IntN(3))
	default:
		return textStringer{genText(r)}
	}
}

// runtimeError is a Go runtime error, recovered from a fault.
func runtimeError(n int) (err any) {
	defer func() { err = recover() }()
	var xs []int
	var m map[string]int
	switch n {
	case 0:
		_ = xs[n+5]
	case 1:
		m["x"] = 1
	default:
		var i any = n
		_ = i.(string)
	}
	return nil
}

// TestTextMatchesFmt (binsize BS2): Text writes each generated value as
// fmt's %v does, byte for byte, with no host text linked. fmt is the
// reference the core replaced. A failing case prints the value and both
// texts; the generator is seeded, so the run replays it.
// Mutants: bs2-anytext-no-error (an error writes nothing but its kind)
// and bs2-anytext-int (an int kind is dropped).
func TestTextMatchesFmt(t *testing.T) {
	if hostText != nil {
		t.Fatal("a host text is linked into rt's tests; Text would be fmt itself")
	}
	r := rand.New(rand.NewPCG(2, 19))
	for i := range 20000 {
		v := genValue(r)
		if got, want := Text(v), fmt.Sprint(v); got != want {
			t.Fatalf("case %d (replay: go test -run TestTextMatchesFmt ./runtime/rt): %T %#v\nText %q\nfmt  %q", i, v, v, got, want)
		}
	}
}

// typeZoo holds a value of each Go type form: named, predeclared,
// generic, pointer, slice, array, map, channel, function, struct and
// interface literals, and a type of another package.
type (
	zooNamed      struct{ n int }
	zooBox[T any] struct{ v T }
	zooPtr        *zooNamed
	zooIface      interface{ M() }
)

func typeZoo() []any {
	ch := make(chan int)
	var recv <-chan int = ch
	return []any{
		zooNamed{}, &zooNamed{}, new(*zooNamed), zooBox[int]{}, zooBox[zooBox[string]]{}, &zooBox[[]int]{},
		zooPtr(nil), 1, "s", 1.5, []int{}, [2]string{}, map[string]zooNamed{}, ch, recv, func(int) error { return nil },
		struct{ A int }{}, &struct{}{}, []any{}, new(zooIface), errors.New("x"), Interrupted, Expired{}, &Cause{},
		textErr{}, (*derefErr)(nil),
	}
}

// TestTypeStringMatchesReflect (binsize BS2): TypeString and TypeName give
// reflect's Type.String and Type.Name for each form of Go type, read from
// the Go runtime's assertion error; a Go release that changes the form of
// that error fails here, not in a trace.
// Mutant: bs2-typename-generic (TypeName cuts a generic type's name at
// the last dot inside its brackets).
func TestTypeStringMatchesReflect(t *testing.T) {
	if got := TypeString(nil); got != "nil" {
		t.Errorf("TypeString(nil) = %q", got)
	}
	for _, v := range typeZoo() {
		rt := reflect.TypeOf(v)
		if got := TypeString(v); got != rt.String() {
			t.Errorf("TypeString(%T) = %q; reflect %q", v, got, rt.String())
		}
		if got := TypeName(TypeString(v)); got != rt.Name() {
			t.Errorf("TypeName(%q) = %q; reflect %q", TypeString(v), got, rt.Name())
		}
	}
}
