package lower

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
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
	{"unsigned", `extern go math/bits.Len8 func Len8(x uint8) int
extern go math/bits.Len16 func Len16(x uint16) int
extern go math/bits.Len32 func Len32(x uint32) int
extern go math/bits.Len64 func Len64(x uint64) int
extern go math/bits.Len func Len(x uint) int
func main() needs Console {
	Console.Println("\(Len8(255)) \(Len16(256)) \(Len32(65536)) \(Len64(4294967296)) \(Len(5))")
}
`, "8 9 17 33 3\n"},
	{"int32", `extern go unicode.IsUpper func IsUpper(r int32) bool
func main() needs Console { Console.Println("\(IsUpper(65)) \(IsUpper(97))") }
`, "true false\n"},
	{"complex128", `extern go math/cmplx.Rect func Rect(r float64, theta float64) complex128
extern go math/cmplx.Abs func Abs(c complex128) float64
func main() needs Console {
	let c = Rect(5.0, 0.0)
	Console.Println("\(Abs(c))")
}
`, "5\n"},
	{"no_result", `extern go runtime.GC func GC()
func main() needs Console {
	GC()
	Console.Println("collected")
}
`, "collected\n"},
	{"duration", `extern go time.Sleep func Pause(d duration)
func main() needs Console {
	Pause(1ms)
	Console.Println("paused")
}
`, "paused\n"},
	// Two targets whose packages share a last path element, and a target
	// whose package name is not its last element (math/rand/v2 is rand).
	{"aliases", `extern go math/rand/v2.IntN func IntN(n int) int
extern go crypto/rand.Text func Text() string
extern go text/template.HTMLEscapeString func TextEscape(s string) string
extern go html/template.HTMLEscapeString func HTMLEscape(s string) string
func main() needs Console {
	Console.Println("\(IntN(1)) \(len(Text())) \(TextEscape("<a>")) \(HTMLEscape("<b>"))")
}
`, "0 26 &lt;a&gt; &lt;b&gt;\n"},
	// A declared name the import would take keeps it; the import gets
	// another (strconv, time).
	{"names", `extern go strconv.Itoa func Itoa(n int) string
extern go time.Sleep func Pause(d duration)
func strconv() string { "mine" }
func time() string { "now" }
func main() needs Console {
	Pause(1ms)
	Console.Println(strconv() + " " + time() + " " + Itoa(7))
}
`, "mine now 7\n"},
	// The P3 wrapper: a literal that calls the extern is the value.
	{"wrapper", `extern go strings.ToUpper func Upper(s string) string
func twice(f func(string) string, s string) string { f(f(s)) }
func main() needs Console {
	let up = func(s string) string { Upper(s) }
	Console.Println(up("x") + " " + twice(func(s string) string { Upper(s) + "!" }, "y"))
}
`, "X Y!!\n"},
	// A Go target whose parameter is variadic ...any accepts the call:
	// Go checks the call against the Ergo signature (an int argument, a
	// string result), not the target's exact type (Risk 4; request).
	{"variadic", `extern go fmt.Sprint func Show(n int) string
func main() needs Console { Console.Println(Show(7) + Show(8)) }
`, "78\n"},
}

func TestErgoRunE4Externs(t *testing.T) {
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
	// The call is direct: the target is called with the Ergo arguments
	// only (no environment), its result is bound at the Ergo type, and no
	// Go function is declared for the extern.
	esub4 := builds[0].mainGo()
	for _, want := range []string{`string = strings.ToUpper("ada")`, `string = strings.Repeat("ab", `, "\tstrings \"strings\"\n"} {
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
	cases := []struct{ name, src string }{
		{"argument", `extern go strings.ToUpper func Upper(n int) int
func main() needs Console {
	let x = 1
	Console.Println("\(Upper(x))") // call
}
`},
		{"literal-argument", `extern go math.Sqrt func Root(x int) float64
func main() needs Console {
	Console.Println("\(Root(4))") // call
}
`},
		{"result", `extern go math.Sqrt func Whole(x float64) int
func main() needs Console {
	let n int = Whole(4.0) // call
	Console.Println("\(n)")
}
`},
		// Interpolation takes any basic type, so only the result binding
		// stops 1.4142135623730951 printing as an Ergo int.
		{"result-interpolated", `extern go math.Sqrt func Whole(x float64) int
func main() needs Console {
	Console.Println("\(Whole(2.0))") // call
}
`},
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
			if !regexp.MustCompile(fmt.Sprintf(`(?m)main\.ergo:%d:\d+: cannot use `, line)).MatchString(out) {
				t.Errorf("the go build error does not name main.ergo:%d", line)
			}
			if strings.Contains(out, "panic") || strings.Contains(out, "internal compiler error") {
				t.Errorf("a panic or an internal error")
			}
		})
	}
}

// C30: the context key namespace keys on package identity. A program
// package checked under the path log declares a context value whose key
// equals one std log binds, and reads std log's binding: its own binding
// never shadows std's, and std's read never sees it.
func TestContextKeyIdentity(t *testing.T) {
	const src = `import log
type tag { key string, text string }
func contextKey(t tag) string { t.key }
func contextText(t tag) string { t.text }
func label() string {
	match log.Annotation("k") {
	case Some[string] as v: v.Value
	case None: "none"
	}
}
func main() needs Console {
	with log.Annotate("k", "std") {
		with (tag{key: "k", text: "program"}) { Console.Println(label()) }
	}
	with (tag{key: "k", text: "program"}) { Console.Println(label()) }
}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	prog, ds := check.Check("log", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	std, err := check.Std.Import("log")
	if err != nil {
		t.Fatal(err)
	}
	// Not vacuous: the key before stage E4, the package path, is one
	// namespace for both.
	pathKey := func(p *check.Package) string { return p.Path }
	if pathKey(prog) != pathKey(std) {
		t.Fatalf("the path key differs: %q, %q", pathKey(prog), pathKey(std))
	}
	if contextNamespace(prog) == contextNamespace(std) {
		t.Fatalf("one namespace %q for the program package log and std log", contextNamespace(prog))
	}
	u := Unit{Package: prog, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	code, err := lowerPackage(u, false)
	if err != nil {
		t.Fatal(err)
	}
	o := &lowered{name: "e4_key_identity", src: src, pkg: prog, file: f, files: []File{{Path: "main.go", Data: code}}}
	goSrc := o.mainGo()
	for _, ns := range []string{contextNamespace(prog), contextNamespace(std)} {
		if !strings.Contains(goSrc, fmt.Sprintf("OpenContext(%q, ", ns)) {
			t.Errorf("the lowered Go opens no context in %q:\n%s", ns, goSrc)
		}
	}
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code2 := runExe(t, exes[o.name])
	if code2 != 0 || stdout != "std\nnone\n" {
		t.Errorf("exit %d, stdout %q, stderr %q; want std, none", code2, stdout, firstLines(stderr, 8))
	}
}

// Each lowering check that stage E4 turned into an internal error stands at
// a form the checker refuses first. A row whose program the checker admits
// fails, and shows what the lowering does with it.
func TestE4CheckerRefusesFirst(t *testing.T) {
	const p3 = "an extern function is called directly; wrap it in a literal: func() { f() }"
	cases := []struct {
		name, site, code, text, src string
	}{
		{"user-extern-value", "calls.go funcValue", "S108", p3, `extern go strings.ToUpper func Upper(s string) string
func main() needs Console { let f = Upper; Console.Println(f("x")) }
`},
		{"std-extern-value", "calls.go funcValue (std body)", "S108", p3, `import queue
func main() needs Console {
	let mk = queue.New[int]
	let q = mk(1)
	q.Close()
	Console.Println("made")
}
`},
		{"sealed-conversion", "calls.go operations", "S109", "#206", `import queue
type Mine {}
func Push(_ Mine, v int) fails queue.Closed {}
func Pop(_ Mine) int fails queue.Closed { 0 }
func Close(_ Mine) {}
func Values(_ Mine) Seq[int] { queue.New[int](0).Values() }
func use(q queue.Queue[int]) { q.Close() }
func main() needs Console {
	use(Mine)
	Console.Println("converted")
}
`},
		{"close-operation", "release.go operation Close", "S109", "lower-v1", `type A { Use() }
type Releaser { Use(), Close() }
type R {}
func Use(_ R) {}
func Close(_ R) {}
func make() Releaser { R }
let W = context { A: make() }
func main() { with W { A.Use() } }
`},
		{"close-generic", "release.go generic Close", "S109", "lower-v1", `type A { Use() }
type G[T] { Value T }
func Use[T](_ G[T]) {}
func Close[T](_ G[T]) {}
let W = context { A: G[int]{Value: 1} }
func main() { with W { A.Use() } }
`},
		{"close-pointer", "release.go pointer Close", "S109", "lower-v1", `type A { Use() }
type P {}
func Use(_ P) {}
func Close(_ *P) {}
let W = context { A: P }
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
