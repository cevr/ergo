package lower

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
)

// Check-v1 stage E6a (196s, D-E6, Q5): an interpolated value of a basic
// type or of a type defined over one is its text as Go's %v writes it,
// written with no boxing, in one allocation.

// textKind is one basic kind of the property: its Ergo type name, the Go
// element type of the feed's table of values, the Go expression that makes
// the value from a table element x, and a seeded generator of its values.
// A float is held as its bits, so the feed passes the exact value, NaN's
// and -0's included.
type textKind struct {
	name, elem, conv string
	gen              func(r *rand.Rand) []textValue
}

// textValue is one value: its table element's Go literal and its text as
// Go's fmt.Sprint writes the value, the oracle.
type textValue struct {
	lit, want string
}

// textCount is the number of generated values of each kind, on top of
// the edge values.
const textCount = 200

func intValues(bits int, signed bool) func(r *rand.Rand) []textValue {
	return func(r *rand.Rand) []textValue {
		var out []textValue
		add := func(lit string, v any) { out = append(out, textValue{lit, fmt.Sprint(v)}) }
		if signed {
			lo, hi := int64(-1)<<(bits-1), int64(1)<<(bits-1)-1
			for _, v := range []int64{0, 1, -1, 9, 10, 99, 100, -100, lo, hi, lo + 1, hi - 1} {
				add(strconv.FormatInt(v, 10), v)
			}
			for range textCount {
				v := r.Int64() >> (64 - bits)
				if r.IntN(2) == 0 {
					v = max(lo, min(hi, r.Int64N(1000)-500))
				}
				add(strconv.FormatInt(v, 10), v)
			}
			return out
		}
		hi := uint64(math.MaxUint64) >> (64 - bits)
		for _, v := range []uint64{0, 1, 9, 10, 99, 100, hi, hi - 1} {
			add(strconv.FormatUint(v, 10), v)
		}
		for range textCount {
			v := r.Uint64() >> (64 - bits)
			if r.IntN(2) == 0 {
				v = min(hi, r.Uint64N(1000))
			}
			add(strconv.FormatUint(v, 10), v)
		}
		return out
	}
}

// float64Edges are the float64 edge values: zeros, infinities, NaN,
// subnormals, the extremes, and values on each side of %v's switch to an
// exponent (21 digits) and of strconv's (1e21, 1e-4, 1e-5).
func float64Edges() []float64 {
	return []float64{0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1), math.NaN(),
		math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64, math.Float64frombits(0x000fffffffffffff),
		0x1p-1022, math.MaxFloat64, -math.MaxFloat64, 0.1, 0.2, 0.3, 1.0 / 3, 100, 1e20, 1e21, 1e-4, 1e-5,
		123456789, 1234567.0, 1e6, 999999, 0.000123, -2.5, float64(math.MaxInt64), float64(float32(0.1))}
}

func float32Edges() []float32 {
	return []float32{0, float32(math.Copysign(0, -1)), float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.NaN()),
		math.SmallestNonzeroFloat32, -math.SmallestNonzeroFloat32, math.Float32frombits(0x007fffff), 0x1p-126,
		math.MaxFloat32, -math.MaxFloat32, 0.1, 0.2, 0.3, 1.0 / 3, 16777216, 16777217, 1e6, 1e-5, 3.4028235e38, 1.1}
}

func lit64(f float64) string { return fmt.Sprintf("%#x", math.Float64bits(f)) }
func lit32(f float32) string { return fmt.Sprintf("%#x", math.Float32bits(f)) }

func randFloat64(r *rand.Rand) float64 {
	switch r.IntN(3) {
	case 0:
		return math.Float64frombits(r.Uint64())
	case 1:
		return (r.Float64() - 0.5) * math.Pow(10, float64(r.IntN(50)-25))
	}
	return float64(r.Int64N(2000000)-1000000) / 100
}

func randFloat32(r *rand.Rand) float32 {
	switch r.IntN(3) {
	case 0:
		return math.Float32frombits(r.Uint32())
	case 1:
		return float32((r.Float64() - 0.5) * math.Pow(10, float64(r.IntN(30)-15)))
	}
	return float32(r.Int64N(2000000)-1000000) / 100
}

func textKinds() []textKind {
	return []textKind{
		{"bool", "bool", "x", func(r *rand.Rand) []textValue {
			out := []textValue{{"true", "true"}, {"false", "false"}}
			for range textCount {
				b := r.IntN(2) == 0
				out = append(out, textValue{strconv.FormatBool(b), fmt.Sprint(b)})
			}
			return out
		}},
		{"string", "string", "x", func(r *rand.Rand) []textValue {
			out := []textValue{{`""`, ""}}
			for _, s := range []string{`\(`, `a \(b) c`, "\xff\xfe", "é", "\x00", `"quoted"`, "tab\there", "\u2028", string([]byte{0xe2, 0x82})} {
				out = append(out, textValue{strconv.Quote(s), fmt.Sprint(s)})
			}
			for range textCount {
				b := make([]byte, r.IntN(40))
				for i := range b {
					b[i] = byte(r.IntN(256))
				}
				out = append(out, textValue{strconv.Quote(string(b)), fmt.Sprint(string(b))})
			}
			return out
		}},
		{"int", "int", "x", intValues(64, true)},
		{"int8", "int8", "x", intValues(8, true)},
		{"int16", "int16", "x", intValues(16, true)},
		{"int32", "int32", "x", intValues(32, true)},
		{"int64", "int64", "x", intValues(64, true)},
		{"uint", "uint", "x", intValues(64, false)},
		{"uint8", "uint8", "x", intValues(8, false)},
		{"uint16", "uint16", "x", intValues(16, false)},
		{"uint32", "uint32", "x", intValues(32, false)},
		{"uint64", "uint64", "x", intValues(64, false)},
		{"uintptr", "uintptr", "x", intValues(64, false)},
		{"float32", "uint32", "math.Float32frombits(x)", func(r *rand.Rand) []textValue {
			var out []textValue
			for _, f := range float32Edges() {
				out = append(out, textValue{lit32(f), fmt.Sprint(f)})
			}
			for range textCount {
				f := randFloat32(r)
				out = append(out, textValue{lit32(f), fmt.Sprint(f)})
			}
			return out
		}},
		{"float64", "uint64", "math.Float64frombits(x)", func(r *rand.Rand) []textValue {
			var out []textValue
			for _, f := range float64Edges() {
				out = append(out, textValue{lit64(f), fmt.Sprint(f)})
			}
			for range textCount {
				f := randFloat64(r)
				out = append(out, textValue{lit64(f), fmt.Sprint(f)})
			}
			return out
		}},
		{"complex64", "[2]uint32", "complex(math.Float32frombits(x[0]), math.Float32frombits(x[1]))", func(r *rand.Rand) []textValue {
			var out []textValue
			edges := float32Edges()
			for _, re := range edges {
				for _, im := range []float32{0, float32(math.Copysign(0, -1)), float32(math.NaN()), float32(math.Inf(-1)), 0.1, math.SmallestNonzeroFloat32} {
					c := complex(re, im)
					out = append(out, textValue{"{" + lit32(re) + ", " + lit32(im) + "}", fmt.Sprint(c)})
					c = complex(im, re)
					out = append(out, textValue{"{" + lit32(im) + ", " + lit32(re) + "}", fmt.Sprint(c)})
				}
			}
			for range textCount {
				re, im := randFloat32(r), randFloat32(r)
				out = append(out, textValue{"{" + lit32(re) + ", " + lit32(im) + "}", fmt.Sprint(complex(re, im))})
			}
			return out
		}},
		{"complex128", "[2]uint64", "complex(math.Float64frombits(x[0]), math.Float64frombits(x[1]))", func(r *rand.Rand) []textValue {
			var out []textValue
			edges := float64Edges()
			for _, re := range edges {
				for _, im := range []float64{0, math.Copysign(0, -1), math.NaN(), math.Inf(1), math.Inf(-1), 0.1, math.SmallestNonzeroFloat64} {
					out = append(out, textValue{"{" + lit64(re) + ", " + lit64(im) + "}", fmt.Sprint(complex(re, im))})
					out = append(out, textValue{"{" + lit64(im) + ", " + lit64(re) + "}", fmt.Sprint(complex(im, re))})
				}
			}
			for range textCount {
				re, im := randFloat64(r), randFloat64(r)
				out = append(out, textValue{"{" + lit64(re) + ", " + lit64(im) + "}", fmt.Sprint(complex(re, im))})
			}
			return out
		}},
		{"duration", "int64", "time.Duration(x)", func(r *rand.Rand) []textValue {
			var out []textValue
			for _, d := range []time.Duration{0, 1, -1, time.Microsecond, time.Millisecond, time.Second, -time.Second,
				time.Hour, time.Hour + 1, 90 * time.Minute, 1500 * time.Millisecond, math.MaxInt64, math.MinInt64, math.MinInt64 + 1} {
				out = append(out, textValue{strconv.FormatInt(int64(d), 10), fmt.Sprint(d)})
			}
			for range textCount {
				d := time.Duration(r.Int64())
				switch r.IntN(3) {
				case 0:
					d = time.Duration(r.Int64N(int64(48*time.Hour))) - 24*time.Hour
				case 1:
					d = time.Duration(r.Int64N(2000000)) - 1000000
				}
				out = append(out, textValue{strconv.FormatInt(int64(d), 10), fmt.Sprint(d)})
			}
			return out
		}},
	}
}

// textForms are the Ergo functions of each kind k that the feed calls: the
// part in a string with text around it (the in-place concatenation) and
// alone (strconv's Format functions), each for k and for a type defined
// over k, and for a type defined over that defined type.
var textForms = []struct {
	fn, typ, body, open, close string
}{
	{"p", "%s", `"<\(v)>"`, "<", ">"},
	{"b", "%s", `"\(v)"`, "", ""},
	{"pd", "D%s", `"<\(v)>"`, "<", ">"},
	{"bd", "D%s", `"\(v)"`, "", ""},
	{"pdd", "DD%s", `"[\(v)]"`, "[", "]"},
}

// TestInterpolationText is the text of every basic kind and of a type
// defined over it, over generated values (E6a A3): the lowered program
// writes each value interpolated, and each line must equal Go's
// fmt.Sprint of the same Go value, computed here, byte for byte. A feed
// file in the program's package calls the lowered functions with the
// values; the comparison quotes each text, so a line feed or an invalid
// byte in a string is compared exactly too.
func TestInterpolationText(t *testing.T) {
	kinds := textKinds()
	if len(kinds) != len(check.Typ)-1 {
		t.Fatalf("%d kinds for %d basic types", len(kinds), len(check.Typ)-1)
	}
	var src, feed, want strings.Builder
	r := rand.New(rand.NewPCG(196, 6))
	feed.WriteString("package main\n\nimport (\n\t\"math\"\n\t\"os\"\n\t\"strconv\"\n\t\"time\"\n)\n\nvar _ = math.Pi\nvar _ time.Duration\n\nfunc init() {\n\tw := func(s string) { os.Stdout.WriteString(strconv.Quote(s) + \"\\n\") }\n")
	total := 0
	for i, k := range kinds {
		if k.name != check.Typ[i+1].Name {
			t.Fatalf("kind %d is %s, want %s", i, k.name, check.Typ[i+1].Name)
		}
		fmt.Fprintf(&src, "type D%s %s\ntype DD%s D%s\n\n", k.name, k.name, k.name, k.name)
		for _, f := range textForms {
			fmt.Fprintf(&src, "func %s%s(v %s) string {\n\t%s\n}\n\n", f.fn, k.name, fmt.Sprintf(f.typ, k.name), f.body)
		}
		// One table of values per kind and a loop, so the feed compiles in
		// seconds: a Go function of thousands of calls does not.
		fmt.Fprintf(&feed, "\tfor _, x := range []%s{\n", k.elem)
		vals := k.gen(r)
		for _, v := range vals {
			fmt.Fprintf(&feed, "\t\t%s,\n", v.lit)
			for _, f := range textForms {
				want.WriteString(strconv.Quote(f.open+v.want+f.close) + "\n")
				total++
			}
		}
		fmt.Fprintf(&feed, "\t} {\n\t\tv := %s\n", k.conv)
		for _, f := range textForms {
			arg := "v"
			if f.typ != "%s" {
				arg = fmt.Sprintf(f.typ, k.name) + "(v)"
			}
			fmt.Fprintf(&feed, "\t\tw(%s%s(%s))\n", f.fn, k.name, arg)
		}
		feed.WriteString("\t}\n")
	}
	feed.WriteString("}\n")
	if total < len(kinds)*textCount*len(textForms) {
		t.Fatalf("only %d values", total)
	}
	out := runFed(t, "e6a_text", src.String(), feed.String())
	if out == want.String() {
		return
	}
	got, exp := strings.Split(out, "\n"), strings.Split(want.String(), "\n")
	bad := 0
	for i := range exp {
		if i >= len(got) || got[i] != exp[i] {
			g := "(missing)"
			if i < len(got) {
				g = got[i]
			}
			t.Errorf("line %d: got %s, want %s", i+1, g, exp[i])
			if bad++; bad > 20 {
				break
			}
		}
	}
	if len(got) != len(exp) {
		t.Errorf("%d lines, want %d", len(got), len(exp))
	}
}

// TestInterpolationAllocs is the owner's speed rule (#242/#243): an
// interpolation of basic values allocates no more than the Go a person
// writes with strconv appends into a sized buffer. Three parts of three
// kinds with text between them are one allocation, the concatenation's;
// a lone small integer is none, as strconv.Itoa's is.
func TestInterpolationAllocs(t *testing.T) {
	src := `func label(n int, f float64, s string) string {
	"n=\(n) f=\(f) s=\(s)"
}

type Celsius float64
type Count int64

func reading(c Celsius, n Count, ok bool) string {
	"t=\(c) n=\(n) ok=\(ok)"
}

func alone(n int) string {
	"\(n)"
}
`
	feed := `package main

import (
	"os"
	"strconv"
	"testing"
)

var sink string

func handWritten(n int, f float64, s string) string {
	var buf [64]byte
	b := append(buf[:0], "n="...)
	b = strconv.AppendInt(b, int64(n), 10)
	b = append(b, " f="...)
	b = strconv.AppendFloat(b, f, 'g', -1, 64)
	b = append(b, " s="...)
	b = append(b, s...)
	return string(b)
}

func init() {
	s := "a string part"
	allocs := func(f func()) string { return strconv.FormatFloat(testing.AllocsPerRun(200, f), 'f', -1, 64) }
	os.Stdout.WriteString("label " + allocs(func() { sink = label(123456789, -2.2250738585072014e-308, s) }) + "\n")
	os.Stdout.WriteString("hand " + allocs(func() { sink = handWritten(123456789, -2.2250738585072014e-308, s) }) + "\n")
	os.Stdout.WriteString("reading " + allocs(func() { sink = reading(-273.15, -9223372036854775808, true) }) + "\n")
	os.Stdout.WriteString("alone7 " + allocs(func() { sink = alone(7) }) + "\n")
	os.Stdout.WriteString("alone " + allocs(func() { sink = alone(1234567) }) + "\n")
	os.Stdout.WriteString(label(123456789, -2.2250738585072014e-308, s) + "\n")
}
`
	got := runFed(t, "e6a_allocs", src, feed)
	want := "label 1\nhand 1\nreading 1\nalone7 0\nalone 1\nn=123456789 f=-2.2250738585072014e-308 s=a string part\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestInterpolationLowersWithoutBoxing: the lowered Go of a string with
// parts of every basic kind calls strconv and Duration's String directly,
// and never the formatting package, the runtime's boxed text, an empty
// interface or reflection; a number in a string with text is appended into
// a constant-size buffer the concatenation reads in place (E6a A4 at the
// output, beside the static grep of interp.go).
func TestInterpolationLowersWithoutBoxing(t *testing.T) {
	var src strings.Builder
	var params, parts []string
	for _, b := range check.Typ[1:] {
		params = append(params, "v"+b.Name+" "+b.Name)
		parts = append(parts, "\\(v"+b.Name+")")
	}
	fmt.Fprintf(&src, "func all(%s) string {\n\t\"%s\"\n}\n\nfunc main() {}\n", strings.Join(params, ", "), strings.Join(parts, " "))
	o := mustLower(t, "e6a_noboxing", src.String())
	body := o.mainGo()
	i := strings.Index(body, "func all(")
	if i < 0 {
		t.Fatalf("no func all in\n%s", body)
	}
	body = body[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	for _, bad := range []string{"fmt.", "interface{}", "any(", "reflect", ".Text"} {
		if strings.Contains(body, bad) {
			t.Errorf("the lowered interpolation holds %q:\n%s", bad, body)
		}
	}
	for _, want := range []string{"strconv.AppendInt(make([]byte, 0, 20), int64(vint8), 10)", "strconv.AppendUint(make([]byte, 0, 20), uint64(vuint16), 10)",
		"strconv.AppendFloat(make([]byte, 0, 24), float64(vfloat32), 'g', -1, 32)", "strconv.AppendFloat(make([]byte, 0, 24), vfloat64, 'g', -1, 64)",
		"strconv.FormatComplex(complex128(vcomplex64), 'g', -1, 64)", "strconv.FormatComplex(vcomplex128, 'g', -1, 128)",
		"time.Duration(vduration).String()", "strconv.FormatBool(vbool)", "\" \" + vstring + \" \""} {
		if !strings.Contains(body, want) {
			t.Errorf("the lowered interpolation lacks %s:\n%s", want, body)
		}
	}
	buildAll(t, []*lowered{o}, false)
}

// e6aRuns are ergo run programs of the admitted forms (the check-v1
// ruling: every admitted form lands checked and lowered, with a run test).
var e6aRuns = []struct{ name, src, want string }{
	{"defined", `type Celsius float64
type Name string
type Warm Celsius
type Ticks uint8

func main() needs Console {
	let t Celsius = 21.5
	let n Name = "ada"
	let w Warm = -0.25
	let k Ticks = 255
	Console.Println("\(n) \(t) \(w) \(k)")
}
`, "ada 21.5 -0.25 255\n"},
	{"complex-and-duration", `type Span duration

func main() needs Console {
	let z complex128
	let h complex64 = complex64(z)
	let s Span = 90m
	let d = 1500ms
	Console.Println("z=\(z) h=\(h) s=\(s) d=\(d) neg=\(-d)")
}
`, "z=(0+0i) h=(0+0i) s=1h30m0s d=1.5s neg=-1.5s\n"},
	{"floats", `func main() needs Console {
	let zero = 0.0
	let f float32 = 0.1
	Console.Println("\(zero / zero) \(1.0 / zero) \(-1.0 / zero) \(-zero) \(f) \(float64(f)) \(1e21) \(123456789.0)")
}
`, "NaN +Inf -Inf -0 0.1 0.10000000149011612 1e+21 1.23456789e+08\n"},
	{"nested-and-field", `type Card { Number int, Name string }
type Box[T] { V T }

func main() needs Console {
	let c = Card{Number: 7, Name: "seven"}
	let b = Box[int]{V: 42}
	Console.Println("card \("\(c.Number)/\(c.Name)") box \(b.V)")
}
`, "card 7/seven box 42\n"},
	{"spilled", `type Fault {}

func g(n int) int fails Fault {
	if n < 0 {
		return Fault
	}
	n * 2
}

func show(n int) string fails Fault {
	"n=\(n) g=\(try g(n)) end"
}

func main() needs Console {
	let s = show(21) catch {
	case Fault:
		"fault"
	}
	let t = show(-1) catch {
	case Fault:
		"fault"
	}
	Console.Println("\(s) \(t)")
}
`, "n=21 g=42 end fault\n"},
	{"only-parts", `func main() needs Console {
	let a = 3
	let s = "x"
	Console.Println("\(a)\(s)\(a)")
	Console.Println("\(true)")
	Console.Println("")
}
`, "3x3\ntrue\n\n"},
}

func TestErgoRunE6aInterpolation(t *testing.T) {
	var builds []*lowered
	for _, r := range e6aRuns {
		builds = append(builds, mustLower(t, "e6a_"+strings.ReplaceAll(r.name, "-", "_"), r.src))
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, r := range e6aRuns {
		stdout, stderr, code := runExe(t, exes["e6a_"+strings.ReplaceAll(r.name, "-", "_")])
		if code != 0 || stdout != r.want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want %q", r.name, code, stdout, firstLines(stderr, 8), r.want)
		}
	}
}

// runFed lowers src with no entry, adds feed (a Go file of the same
// package whose init calls the lowered functions) and runs the program. It
// returns stdout.
func runFed(t *testing.T, name, src, feed string) string {
	t.Helper()
	o := lowerSource(name, src, false)
	switch {
	case o.err != nil:
		t.Fatalf("%v\n%s", o.err, src)
	case len(o.diags) > 0:
		t.Fatalf("checker diagnostics %v", o.diags)
	case o.panicked != nil:
		t.Fatalf("lowering panicked: %v", o.panicked)
	case len(o.refused) > 0:
		t.Fatalf("refused %+v", o.refused)
	}
	// The feed calls each function with no environment: a function with no
	// row, no capability and only pure calls is pure (pure.go, D-S5-10).
	if regexp.MustCompile(`func \w+\(env \*Env, `).MatchString(o.mainGo()) {
		t.Fatalf("a fed function takes (env *Env, …), want none (pure):\n%s", firstLines(o.mainGo(), 40))
	}
	root := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	write("p/main.go", []byte(o.mainGo()))
	write("p/feed.go", []byte(feed))
	exe := filepath.Join(root, "bin", name)
	cmd := exec.Command("go", "build", "-o", exe, "./p")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v\n%s\n%s", err, firstLines(out.String(), 40), firstLines(o.mainGo(), 80))
	}
	stdout, stderr, code := runExe(t, exe)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, firstLines(stderr, 20))
	}
	return stdout
}
