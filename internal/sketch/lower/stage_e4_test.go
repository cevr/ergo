package lower

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Check-v1 stage E4 (196x, C26): a user extern whose signature and target
// are in the checked domain lowers as a direct call of its Go target, with
// no environment, no wrapper and an import under a name the naming pass
// reserved. Each row is one program, built and run in one sweep.
type e4Run struct {
	name, src, want string
}

var e4Externs = []e4Run{
	// accept/runs/Esub4-extern.ergo, verbatim.
	{"esub4", `// Stage E4 (196x): user extern go bindings are called directly and lower
// to calls of their Go targets.
extern go strings.ToUpper func Upper(s string) string
extern go strings.Repeat func Repeat(s string, n int) string

func main() needs Console {
	Console.Println(Upper("ada"))
	Console.Println(Repeat("ab", 3))
}
`, "ADA\nababab\n"},
	// One Go standard-library function per basic-kind family.
	{"int", `extern go strconv.Itoa func Itoa(n int) string
func main() needs Console { Console.Println(Itoa(-42)) }
`, "-42\n"},
	{"int64", `extern go strconv.FormatInt func FormatInt(n int64, base int) string
func main() needs Console { Console.Println(FormatInt(255, 16)) }
`, "ff\n"},
	{"bool", `extern go strconv.FormatBool func FormatBool(b bool) string
func main() needs Console { Console.Println(FormatBool(true)) }
`, "true\n"},
	{"float64", `extern go math.Sqrt func Sqrt(x float64) float64
func main() needs Console { Console.Println("\(Sqrt(2.25))") }
`, "1.5\n"},
	{"float32", `extern go math.Float32bits func Float32bits(f float32) uint32
func main() needs Console { Console.Println("\(Float32bits(1.0))") }
`, "1065353216\n"},
	// G9: only the pure allowlist is a user extern's target, so the
	// unsigned kinds are those its packages take (uint8 as FormatComplex's
	// format byte, below).
	{"unsigned", `extern go strconv.FormatUint func FormatUint(n uint64, base int) string
extern go math.Float64bits func Float64bits(f float64) uint64
extern go math.Float32frombits func Float32frombits(b uint32) float32
func main() needs Console {
	Console.Println(FormatUint(255, 2) + " \(Float64bits(1.0)) \(Float32frombits(1065353216))")
}
`, "11111111 4607182418800017408 1\n"},
	{"int32", `extern go unicode/utf8.RuneLen func RuneLen(r int32) int
func main() needs Console { Console.Println("\(RuneLen(65)) \(RuneLen(19990))") }
`, "1 3\n"},
	// G2 (lane G): unicode and unicode/utf16 joined the pure allowlist.
	{"unicode", `extern go unicode.ToUpper func Up(r rune) rune
extern go unicode.IsSpace func IsSpace(r rune) bool
extern go unicode/utf16.IsSurrogate func IsSurrogate(r rune) bool
extern go unicode/utf16.DecodeRune func DecodeRune(a rune, b rune) rune
func main() needs Console {
	Console.Println("\(Up(97)) \(IsSpace(32)) \(IsSurrogate(55357)) \(DecodeRune(55357, 56832))")
}
`, "65 true true 128512\n"},
	{"complex128", `extern go strconv.FormatComplex func FormatComplex(c complex128, f uint8, prec int, size int) string
func main() needs Console {
	let c complex128 = complex128(5)
	Console.Println(FormatComplex(c, 103, -1, 128))
}
`, "(5+0i)\n"},
	// A declared name the import would take keeps it; the import gets
	// another (strconv, strings).
	{"names", `extern go strconv.Itoa func Itoa(n int) string
extern go strings.ToUpper func Upper(s string) string
func strconv() string { "mine" }
func strings() string { "now" }
func main() needs Console {
	Console.Println(strconv() + " " + strings() + " " + Itoa(7) + Upper("a"))
}
`, "mine now 7A\n"},
	// The P3 wrapper: a literal that calls the extern is the value.
	{"wrapper", `extern go strings.ToUpper func Upper(s string) string
func twice(f func(string) string, s string) string { f(f(s)) }
func main() needs Console {
	let up = func(s string) string { Upper(s) }
	Console.Println(up("x") + " " + twice(func(s string) string { Upper(s) + "!" }, "y"))
}
`, "X Y!!\n"},
	// The call check repeats the call's text in a block that never runs:
	// the operands run left to right, once each, in every use, and the
	// target runs once (round 2 verify probes).
	{"order_once", `extern go strings.Repeat func Repeat(s string, n int) string
func first() string needs Console { Console.Println("first"); "x" }
func second() int needs Console { Console.Println("second"); 2 }
func sink(s string) needs Console { Console.Println(s) }
func main() needs Console {
	sink(Repeat(first(), second()))
	Repeat(first(), second())
	let _ = Repeat(first(), second())
	let f = func() string needs Console { Repeat(first(), second()) }
	sink(f())
	Console.Println("\(Repeat(first(), second()))")
}
`, "first\nsecond\nxx\nfirst\nsecond\nfirst\nsecond\nfirst\nsecond\nxx\nfirst\nsecond\nxx\n"},
	// A declaration with no result over a Go function that has one: the
	// call runs as a statement and Go drops the result. The no-result
	// shape is reachable in the pure allowlist (G9 round 2 M2);
	// TestErgoRunE4DiscardVariadic proves the call is made.
	{"no_result", `extern go strings.ToUpper func Discard(s string)
extern go strings.Repeat func Check(s string, n int)
func main() needs Console {
	Discard("x")
	Check("x", 2)
	Console.Println("discarded")
}
`, "discarded\n"},
	// A Go target whose parameter is variadic ...string accepts the call
	// with a fixed Ergo arity (strings.NewReplacer, its *Replacer result
	// discarded); Go checks the call against the Ergo signature.
	{"variadic", `extern go strings.NewReplacer func Pairs(a string, b string, c string, d string)
func main() needs Console {
	Pairs("a", "b", "c", "d")
	Console.Println("paired")
}
`, "paired\n"},
}

// TestErgoRunE4Externs builds Go because it checks what the built program
// prints and how it exits; no in-process check observes that, and Ergo has
// no reference interpreter yet (test-speed T8).
func TestErgoRunE4Externs(t *testing.T) {
	needsBuild(t)
	var builds []*lowered
	for _, r := range e4Externs {
		builds = append(builds, mustLower(t, "e4_"+r.name, r.src))
	}
	builds = append(builds, e4Library(t))
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, r := range append(e4Externs, e4Run{name: "library", want: "LIB\n"}) {
		stdout, stderr, code := runExe(t, exes["e4_"+r.name])
		if code != 0 || stdout != r.want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want %q", r.name, code, stdout, firstLines(stderr, 8), r.want)
		}
	}
	// The call is direct: Go proves the call a function call (`if false {
	// defer strings.ToUpper("ada") }`), the target is called with the Ergo
	// arguments only (no environment), its result is bound at the Ergo
	// type, and no Go function is declared for the extern.
	esub4 := builds[0].mainGo()
	for _, want := range []string{"if false {", `*/defer strings.ToUpper("ada")` + "\n", `string = strings.ToUpper("ada")`, `*/defer strings.Repeat("ab", `, `string = strings.Repeat("ab", `, "\tstrings \"strings\"\n"} {
		if !strings.Contains(esub4, want) {
			t.Errorf("esub4: the lowered Go has no %q:\n%s", want, esub4)
		}
	}
	if regexp.MustCompile(`func (Upper|Repeat)\b`).MatchString(esub4) {
		t.Errorf("esub4: the lowered Go declares a function for an extern:\n%s", esub4)
	}
}

// e4Library is a program whose imported library package declares the
// extern main calls: the naming pass reserves the import of a library's
// target too.
func e4Library(t *testing.T) *lowered {
	t.Helper()
	lib := cxPackage(t, "example.test/hostlib", `extern go strings.ToUpper func Up(s string) string
func Shout(s string) string { Up(s) }
`)
	return cxLower(t, "e4_library", `import example.test/hostlib
func main() needs Console { Console.Println(hostlib.Shout("lib") + hostlib.Up("")) }
`, cxImporter{"example.test/hostlib": lib})
}

// A user extern whose Ergo signature does not match its Go target fails
// ergo run with go build's error at the line of the Ergo call (the call's
// //line directive), never a panic. A literal argument lowers typed (196u),
// so a target of another type never accepts it as an untyped constant, and
// a result is bound at its Ergo type, so a target's float64 never reaches
// an interpolation that would print it as the Ergo int (Risk 4).
func TestErgoRunE4Mismatch(t *testing.T) {
	needsBuild(t)
	cases := []struct{ name, src, want string }{
		{"argument", `extern go strings.ToUpper func Upper(n int) int
func main() needs Console {
	let x = 1
	Console.Println("\(Upper(x))") // call
}
`, ""},
		{"literal-argument", `extern go math.Sqrt func Root(x int) float64
func main() needs Console {
	Console.Println("\(Root(4))") // call
}
`, ""},
		{"result", `extern go math.Sqrt func Whole(x float64) int
func main() needs Console {
	let n int = Whole(4.0) // call
	Console.Println("\(n)")
}
`, ""},
		// Interpolation takes any basic type, so only the result binding
		// stops 1.4142135623730951 printing as an Ergo int.
		{"result-interpolated", `extern go math.Sqrt func Whole(x float64) int
func main() needs Console {
	Console.Println("\(Whole(2.0))") // call
}
`, ""},
		// Round 1 M1: a discarded result is bound at its Ergo type too.
		{"statement", `extern go math.Sqrt func Whole(x float64) int
func main() needs Console {
	Whole(2.0) // call
	Console.Println("accepted-mismatch")
}
`, ""},
		// Round 1 M2's type target (time.Duration) is outside the pure
		// allowlist since G9, and no allowlisted package declares a named
		// basic type: it is e4HostTargets' type_target.
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := 0
			for i, l := range strings.Split(c.src, "\n") {
				if strings.HasSuffix(l, "// call") {
					line = i + 1
				}
			}
			out, err := ergoRun(t, c.src)
			t.Logf("ergo run output:\n%s", out)
			if err == nil {
				t.Fatalf("ergo run succeeded")
			}
			want := c.want
			if want == "" {
				want = "cannot use "
			}
			if !regexp.MustCompile(fmt.Sprintf(`(?m)main\.ergo:%d:\d+: %s`, line, want)).MatchString(out) {
				t.Errorf("the go build error does not name main.ergo:%d with %q", line, want)
			}
			if strings.Contains(out, "panic") || strings.Contains(out, "internal compiler error") {
				t.Errorf("a panic or an internal error")
			}
		})
	}
}

// A user extern of Go's unsafe that reaches the lowering is an internal
// error, at the call (check-v1 spec C26 amendment 2026-11-04): the checker
// refuses it (S108, TestE4CheckerRefusesFirst/unsafe-extern), and a
// qualified built-in such as unsafe.Sizeof fails hostStmt's defer check.
func TestE4HostUnsafeIce(t *testing.T) {
	site := &syntax.Ident{Name: "Size"}
	defer func() {
		ref, ok := recover().(refusal)
		if !ok || !strings.Contains(ref.msg, "internal compiler error: a user extern of Go's unsafe") {
			t.Fatalf("hostPkg(unsafe): refusal %+v (ok %v); want the internal error", ref, ok)
		}
	}()
	(&lowerer{}).hostPkg("unsafe", site)
}

// Each lowering check that stage E4 turned into an internal error stands at
// a form the checker refuses first. A row whose program the checker admits
// fails, and shows what the lowering does with it.
func TestE4CheckerRefusesFirst(t *testing.T) {
	const p3 = "an extern function is called directly; wrap it in a literal: func() { f() }"
	cases := []struct {
		name, site, code, text, src string
	}{
		{"unsafe-extern", "calls.go hostPkg unsafe", "S108", "Ergo has no unsafe; unsafe stays at the FFI edge", `extern go unsafe.Sizeof func Size(x int64) uintptr
func main() needs Console {
	Console.Println("\(Size(1))")
}
`},
		{"std-extern-value", "calls.go funcValue (std body)", "S108", p3, `import queue
func main() needs Console {
	let mk = queue.New[int]
	let q = mk(1)
	q.End()
	Console.Println("made")
}
`},
		{"sealed-conversion", "calls.go operations", "S109", "#206", `import queue
type Mine {}
func Push(_ Mine, v int) fails queue.Closed {}
func Pop(_ Mine) int fails queue.Closed { 0 }
func End(_ Mine) {}
func Values(_ Mine) Seq[int] { queue.New[int](0).Values() }
func Poll(_ Mine) Option[int] { None }
func Len(_ Mine) int { 0 }
func use(q queue.Queue[int]) { q.End() }
func main() needs Console {
	use(Mine)
	Console.Println("converted")
}
`},
		// The operation, generic and pointer Close forms release since
		// lower-v1 stage 4 (stage4_close_test.go); a generic Close that
		// needs witnesses is still the checker's (closeWitnessed).
		{"close-witnessed", "release.go generic Close with witnesses", "S109", "#197", `type A { Use() }
type Named { Name() string }
type N {}
func Name(_ N) string { "n" }
type G[T] { Value T }
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) {}
let W = context { A: G[N]{Value: N} }
func main() { with W { A.Use() } }
`},
		{"close-extern", "release.go extern Close", "S109", "#202", `type A { Use() }
type P {}
func Use(_ P) {}
extern go strings.Close func Close(p P)
let W = context { A: P }
func main() { with W { A.Use() } }
`},
		{"context-extern", "context.go extern protocol function", "S109", "#202", `type tag { text string }
extern go strings.Key func contextKey(t tag) string
func contextText(t tag) string { t.text }
func main() needs Console { with (tag{text: "x"}) { Console.Println("in") } }
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := lowerSource("e4_refused", c.src, true)
			if o.err != nil {
				t.Fatal(o.err)
			}
			if len(o.diags) == 0 {
				t.Fatalf("the checker admits the form %s guards; the lowering: refused %+v, panic %v", c.site, o.refused, o.panicked)
			}
			for _, d := range o.diags {
				if d.Code != c.code || !strings.Contains(d.Message, c.text) {
					t.Errorf("diagnostic %s %q; want %s naming %q", d.Code, d.Message, c.code, c.text)
				}
			}
		})
	}
}
