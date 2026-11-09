package check

import (
	"strings"
	"testing"
)

// literalSites has one row per expected-type site of spec D-A2 (literals.go
// lists them), and one per position that gives no expected type, where the
// literal takes its default (196u rules 1 and 2). Each row's literal is the
// one basic literal spelled lit in its program; want is the type Info
// records for it, when it is checked, with no update pass (rule 7).
var literalSites = []struct {
	site, src, lit, want string
}{
	{"let with a written type", `func f() int8 {
	let v int8 = 77
	return v
}`, "77", "int8"},
	{"parameter", `func g(x int8) {}
func f() {
	g(77)
}`, "77", "int8"},
	{"parameter fixed by a typed argument", `func Two[T](a, b T) T {
	return a
}
func f(x int8) int8 {
	return Two(x, 77)
}`, "77", "int8"},
	{"parameter fixed by a typed argument after it", `func Two[T](a, b T) T {
	return a
}
func f(x int8) int8 {
	return Two(77, x)
}`, "77", "int8"},
	{"parameter only literals bind", `func Two[T](a, b T) T {
	return a
}
func f() int {
	return Two(77, 1)
}`, "77", "int"},
	{"parameter of an explicit instance", `func Id[T](x T) T {
	return x
}
func f() float32 {
	return Id[float32](77)
}`, "77", "float32"},
	{"return", `func f() int8 {
	return 77
}`, "77", "int8"},
	{"block value", `func make(f func() int8) int8 {
	return f()
}
func f() int8 {
	return make() {
		77
	}
}`, "77", "int8"},
	{"record field", `type P {
	X int8,
}
func f() P {
	return P{X: 77}
}`, "77", "int8"},
	{"slice element", `func f() []uint16 {
	return []uint16{77}
}`, "77", "uint16"},
	{"Map key", `func f() Map[int8, float32] {
	return Map[int8, float32]{77: 1}
}`, "77", "int8"},
	{"Map value", `func f() Map[int8, float32] {
	return Map[int8, float32]{1: 77}
}`, "77", "float32"},
	{"right operand", `func f(x int8) int8 {
	return x + 77
}`, "77", "int8"},
	{"left operand", `func f(x float32) float32 {
	return 77 * x
}`, "77", "float32"},
	{"comparison operand", `func f(x uint8) bool {
	return x < 77
}`, "77", "uint8"},
	{"range bound", `func f(n int16) int16 {
	let mut s int16 = 0
	for i in 77..n {
		s += i
	}
	return s
}`, "77", "int16"},
	{"assignment target", `func f() int8 {
	let mut v int8 = 1
	v = 77
	return v
}`, "77", "int8"},
	{"assignment operation", `func f() int8 {
	let mut v int8 = 1
	v += 77
	return v
}`, "77", "int8"},
	{"index", `func f(xs []string) string {
	return xs[77]
}`, "77", "int"},
	{"constant case", `func f(n uint8) int {
	match n {
	case 77:
		return 1
	case _:
		return 0
	}
}`, "77", "uint8"},
	{"shift left operand", `func f(n int) int64 {
	return 77 << n
}`, "77", "int64"},
	{"signed literal", `func f() int8 {
	return -77
}`, "-77", "int8"},
	{"signed literal in parentheses", `func f() int8 {
	return -(77)
}`, "-(77)", "int8"},
	// No expected type: the default.
	{"let without a type", `func f() int {
	let v = 77
	return v
}`, "77", "int"},
	{"float default", `func f() float64 {
	let v = 77.5
	return v
}`, "77.5", "float64"},
	{"rune default", `func f() rune {
	let v = 'w'
	return v
}`, "'w'", "int32"},
	{"string default", `func f() string {
	let v = "w"
	return v
}`, `"w"`, "string"},
	{"conversion operand", `func f() int8 {
	return int8(77)
}`, "77", "int"},
	{"interpolated value", `func f() string {
	return "\(77)"
}`, "77", "int"},
	{"shift count", `func f(x int8) int8 {
	return x << 7
}`, "7", "int"},
	{"two literals compared", `func f() bool {
	return 77 < 78
}`, "77", "int"},
	{"two literals", `func f() float64 {
	let v = 77.0 * 2.5
	return v
}`, "77.0", "float64"},
}

// TestLiteralSites (spec D-A2): at each site the literal takes the type
// the site expects, else its default, and the program checks clean.
func TestLiteralSites(t *testing.T) {
	for _, row := range literalSites {
		t.Run(row.site, func(t *testing.T) {
			pkg, diags := CheckSource(row.src + "\n")
			if len(diags) > 0 {
				t.Fatalf("want a clean check, got %s", describeDiags(diags))
			}
			// The literal is the expression spelled row.lit: a basic
			// literal, or a signed literal, which is one literal.
			src := row.src + "\n"
			var got []string
			for x, typ := range pkg.Info.Types {
				if src[x.Pos():x.End()] == row.lit {
					got = append(got, typ.String())
				}
			}
			if len(got) != 1 || got[0] != row.want {
				t.Fatalf("the literal %s: want %s, got %q", row.lit, row.want, got)
			}
		})
	}
}

// literalRules has one row per 196u refusal: the program's one diagnostic
// is S108 at line, with the message an agent copies (spec D-A7).
var literalRules = []struct {
	rule, src string
	line      int
	msg       string
}{
	{"1: a float literal is not an int", `func f() int {
	let c int = 2.0
	return c
}`, 2, "a float literal takes only a float type, not int; write 2"},
	{"1: an integer literal is not a duration", `func f() duration {
	let d duration = 5
	return d
}`, 2, "an integer literal is not a duration: write a duration literal such as 5s"},
	{"1: a string literal is not an int", `func g(x int) {}
func f() {
	g("s")
}`, 3, "a string literal takes only a string type, not int"},
	{"1: a float literal beside an int", `func f(n int) int {
	return n + 1.5
}`, 2, "a float literal takes only a float type, not int"},
	{"3: a name does not convert", `let Limit = 10
func f() int8 {
	let b int8 = Limit
	return b
}`, 3, "Limit is int; write int8(Limit) or declare Limit int8"},
	{"3: a typed result overflows", `func f() int8 {
	let y int8 = 100 + 100
	return y
}`, 2, "the constant 200 overflows int8"},
	{"3: a literal overflows its type", `let Max8 int8 = 300`, 1, "the constant 300 overflows int8"},
	{"3: each typed step fits", `func f() int8 {
	let y int8 = 100 + 100 - 100
	return y
}`, 2, "the constant 200 overflows int8"},
	{"3: a signed literal in parentheses fits after its sign", `func f() int8 {
	let a int8 = -(129)
	return a
}`, 2, "the constant -129 overflows int8"},
	{"1: a signed float literal is not an int", `func f() int64 {
	return -1e3
}`, 2, "a float literal takes only a float type, not int64; write -1000"},
	{"1: a float literal under two signs is not an int", `func f() int {
	return -(-16777217.0)
}`, 2, "a float literal takes only a float type, not int; write 16777217"},
	{"D-A3: a conversion's literal takes int", `func f() uint64 {
	return uint64(18446744073709551615)
}`, 2, "the constant 18446744073709551615 overflows int, the type a conversion's literal takes; write it where uint64 is expected: let x uint64 = 18446744073709551615"},
	{"4: two literals take their defaults", `func f() float64 {
	let d = 1 + 2.5
	return d
}`, 2, "mismatched types int and float64; write 1.0 + 2.5"},
	{"4: typed operands never convert", `func f(a int32, b int64) int64 {
	return a + b
}`, 2, "mismatched types int32 and int64; write int64(a)"},
	{"4: an int and a rune literal", `func f() int {
	let v = 1 + 'a'
	return v
}`, 2, "mismatched types int and int32; both literals take their defaults"},
	{"4: a string and a rune literal", `func f() string {
	let v = "a" + 'b'
	return v
}`, 2, "mismatched types string and int32; both literals take their defaults"},
	{"6: mixed defaults", `func Two[T](a, b T) T {
	return a
}
func f() rune {
	return Two(1, 'a')
}`, 5, "cannot infer T; write the type arguments"},
	{"6: mixed defaults in a record literal", `type Pair[T] {
	A T,
	B T,
}
func f() {
	let p = Pair{1, 'a'}
	let q = p
	q
}`, 6, "cannot infer the type argument T of Pair: its literals take int and int32"},
	{"D-A3: a shift in a float context", `func f(n int) float64 {
	let v float64 = 1 << n
	return v
}`, 2, "write int(1) << n"},
	// A constant is not a function: calling one is S108, never an
	// unchecked call (FuzzCheck found `""()` accepted at the base).
	{"a literal is not called", `func f() string {
	return ""()
}`, 2, "cannot call this expression, a value of type string"},
	{"a parenthesized literal is not called", `func f() int {
	return (1)()
}`, 2, "cannot call this expression, a value of type int"},
	{"a named constant is not called", `let K = 1
func f() int {
	return K()
}`, 3, "cannot call K, a value of type int"},
}

// TestLiteralRules (196u rules 1, 3, 4 and 6; spec D-A3): each refusal is
// one S108 at the mistake, naming the form to write.
func TestLiteralRules(t *testing.T) {
	for _, row := range literalRules {
		t.Run(row.rule, func(t *testing.T) {
			_, diags := CheckSource(row.src + "\n")
			if len(diags) != 1 || diags[0].Code != CodeType || diags[0].Line != row.line || !strings.Contains(diags[0].Message, row.msg) {
				t.Fatalf("want one S108 at line %d containing %q, got %s", row.line, row.msg, describeDiags(diags))
			}
		})
	}
}

func describeDiags(ds []Diagnostic) string {
	s := ""
	for _, d := range ds {
		s += "\n\t" + d.String()
	}
	return s
}
