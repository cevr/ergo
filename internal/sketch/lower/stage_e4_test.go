package lower

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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

// e4HostTargets are the stage E4 programs whose targets are outside the
// pure allowlist (G9, std-1), kept as negatives: a target with no result
// (runtime.GC), a duration parameter (time.Sleep), packages that share a
// last path element or whose name is not it (math/rand/v2, crypto/rand,
// text/template, html/template), a variadic target (fmt.Sprint), a generic
// target (cmp.Compare), a named type as target (time.Duration) and a host
// call that must run once (fmt.Print). Each lowered and ran before G9.
// Now each is one S109 per extern at the checker, so the lowering never
// sees it. The no-result and variadic shapes still run through allowlisted
// targets (e4Externs, TestErgoRunE4DiscardVariadic). The generic,
// named-type, duration, package-name and host-call-once shapes have no
// allowlisted target of basic types; they return as run rows with #202.
var e4HostTargets = []struct {
	name, src string
	externs   int
}{
	{"no_result", "extern go runtime.GC func GC()\nfunc main() { GC() }\n", 1},
	{"duration", "extern go time.Sleep func Pause(d duration)\nfunc main() { Pause(1ms) }\n", 1},
	{"aliases", `extern go math/rand/v2.IntN func IntN(n int) int
extern go crypto/rand.Text func Text() string
extern go text/template.HTMLEscapeString func TextEscape(s string) string
extern go html/template.HTMLEscapeString func HTMLEscape(s string) string
func main() needs Console {
	Console.Println("\(IntN(1)) \(len(Text())) \(TextEscape("<a>")) \(HTMLEscape("<b>"))")
}
`, 4},
	{"variadic", "extern go fmt.Sprint func Show(n int) string\nfunc main() needs Console { Console.Println(Show(7)) }\n", 1},
	{"generic_target", "extern go cmp.Compare func Compare(a int, b int) int\nfunc main() needs Console { Console.Println(\"\\(Compare(1, 2))\") }\n", 1},
	{"type_target", "extern go time.Duration func Truncate(x float64) duration\nfunc main() { let _ = Truncate(1.9) }\n", 1},
	{"host_once", "extern go fmt.Print func Print(s string)\nfunc main() { Print(\"once\") }\n", 1},
}

// TestE4HostTargetsRefused: each program of e4HostTargets stops at the
// checker with one S109 per extern, naming the host (G9), and lowers no
// Go.
func TestE4HostTargetsRefused(t *testing.T) {
	for _, r := range e4HostTargets {
		t.Run(r.name, func(t *testing.T) {
			o := lowerSource("e4_host_"+r.name, r.src, true)
			if len(o.diags) != r.externs || len(o.files) != 0 {
				t.Fatalf("want %d diagnostics and no Go, got %v and %d files", r.externs, o.diags, len(o.files))
			}
			for _, d := range o.diags {
				// G2: cmp reads only its arguments, so its S109 says it is
				// not listed yet rather than that it reaches the host.
				if d.Code != "S109" || !strings.Contains(d.Message, "reaches the host") && !strings.Contains(d.Message, "is not on the pure allowlist") {
					t.Errorf("want an S109 naming the host or the allowlist, got %s", d)
				}
			}
		})
	}
}

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

// TestErgoRunE4ResultUse (round 1 M1) is a property over every basic type
// the checker admits as a user extern's result (check.Typ, Bool to
// Duration: externBasic) crossed with every use of the call: a value, a
// statement, a blank binding, an interpolation, and a function literal's
// tail and statement. The Go target returns another type. Oracle: go build
// fails, and every error names the call's line (4). The result is never
// dropped unchecked. An interpolated complex value is E6a's refusal
// (interp.go), so that cell may be refused instead.
func TestErgoRunE4ResultUse(t *testing.T) {
	needsBuild(t)
	uses := []struct{ name, line string }{
		{"value", "consume(Wrong({arg}))"},
		{"statement", "Wrong({arg})"},
		{"blank", "let _ = Wrong({arg})"},
		{"interpolation", `Console.Println("\(Wrong({arg}))")`},
		{"closure_value", "let f = func() {type} { Wrong({arg}) }; consume(f())"},
		{"closure_statement", "let g = func() { Wrong({arg}) }; g()"},
	}
	var builds []*lowered
	for _, k := range check.Typ[check.Bool:] {
		// A Go result of another type: string, or float64 for string.
		target, param, arg := "strings.ToUpper", "string", `"x"`
		if k.Kind == check.String {
			target, param, arg = "math.Sqrt", "float64", "2.0"
		}
		for _, u := range uses {
			body := strings.NewReplacer("{arg}", arg, "{type}", k.Name).Replace(u.line)
			src := fmt.Sprintf("extern go %s func Wrong(x %s) %s\nfunc consume(v %s) {}\nfunc main() needs Console {\n\t%s\n\tConsole.Println(\"end\")\n}\n", target, param, k.Name, k.Name, body)
			o := lowerSource("e4_use_"+k.Name+"_"+u.name, src, true)
			switch {
			case o.err != nil || len(o.diags) > 0 || o.panicked != nil:
				t.Errorf("%s: error %v, diagnostics %v, panic %v\n%s", o.name, o.err, o.diags, o.panicked, src)
			case len(o.refused) > 0:
				if u.name != "interpolation" || (k.Kind != check.Complex64 && k.Kind != check.Complex128) || !strings.Contains(o.refused[0].Message, "interpolated value") {
					t.Errorf("%s: refused %+v\n%s", o.name, o.refused, src)
				}
			default:
				builds = append(builds, o)
			}
		}
	}
	errs := e4BuildErrors(t, builds)
	for _, o := range builds {
		if !e4FailsAt(errs[o.name], 4) {
			t.Errorf("%s: go build does not fail at the call's line 4 only:\n%s\nsource:\n%s\nGo:\n%s", o.name, errs[o.name], o.src, o.mainGo())
		}
	}
}

// e4BuildErrors writes each program as package p/NAME of one module and
// runs go build (no vet, no link) on them all. It returns the go build
// output of each package that fails, by name.
func e4BuildErrors(t *testing.T, programs []*lowered) map[string]string {
	t.Helper()
	fastTierGuard(t, "a go command")
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
	for _, p := range programs {
		write("p/"+p.name+"/main.go", []byte(p.mainGo()))
	}
	cmd := exec.Command("go", "build", "./p/...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	out, _ := cmd.CombinedOutput()
	sections := map[string]string{}
	cur := ""
	for _, l := range strings.Split(string(out), "\n") {
		if name, ok := strings.CutPrefix(l, "# "+programModule+"/p/"); ok {
			cur = name
			continue
		}
		if l != "" {
			sections[cur] += l + "\n"
		}
	}
	if s := sections[""]; s != "" {
		t.Fatalf("go build output outside a program package:\n%s", firstLines(s, 30))
	}
	return sections
}

// e4FailsAt reports whether out, one package's go build output, has an
// error at main.ergo:line, and no error at another line of main.ergo or at
// a line of the generated main.go. Go's "declared and not used" of a
// variable whose only use is the failed call is that error's consequence.
func e4FailsAt(out string, line int) bool {
	at := regexp.MustCompile(`main\.ergo:(\d+):\d+: `)
	found := false
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "main.go:") {
			return false
		}
		if strings.Contains(l, ": declared and not used: ") {
			continue
		}
		if m := at.FindStringSubmatch(l); m != nil {
			if m[1] != strconv.Itoa(line) {
				return false
			}
			found = true
		}
	}
	return found
}

// e4CallChecks counts the calls of the Go target in goSrc, a lowered
// program, that follow their call check: a //line directive, then `if
// false {`, `defer CALL` and `}`, then the call's statement with the same
// CALL text. It returns each call of the target that does not.
func e4CallChecks(goSrc, target string) (checked int, bad []string) {
	anchor := regexp.MustCompile(`^\t*(/\*line [^*]*\*/)?`)
	lines := strings.Split(goSrc, "\n")
	bare := make([]string, len(lines))
	for i, l := range lines {
		bare[i] = anchor.ReplaceAllString(l, "")
	}
	for i, l := range bare {
		call := l
		if rest, ok := strings.CutPrefix(l, "var "); ok {
			_, call, _ = strings.Cut(rest, " = ")
		}
		if !strings.HasPrefix(call, target+"(") {
			continue
		}
		if i >= 4 && strings.HasPrefix(lines[i-4], "//line ") && bare[i-3] == "if false {" && bare[i-2] == "defer "+call && bare[i-1] == "}" {
			checked++
		} else {
			bad = append(bad, l)
		}
	}
	return checked, bad
}

// A panic in a host call is a defect (Risk 4), and its report names the
// Ergo call's line in every use: the call check (`if false { defer ... }`)
// and the typed binding (hostStmt) keep the call under its //line
// directive.
func TestErgoRunE4HostPanic(t *testing.T) {
	needsBuild(t)
	for _, use := range []string{`Console.Println(Repeat("x", -1))`, `Repeat("x", -1)`, `let _ = Repeat("x", -1)`} {
		src := "extern go strings.Repeat func Repeat(s string, n int) string\nfunc main() needs Console {\n\t" + use + "\n\tConsole.Println(\"returned\")\n}\n"
		out, err := ergoRun(t, src)
		if err == nil || !strings.Contains(out, "panic: strings: negative Repeat count") || !regexp.MustCompile(`(?m)^\tmain at \S*main\.ergo:3$`).MatchString(out) || strings.Contains(out, "returned") {
			t.Errorf("%s: err %v; want a defect at main.ergo:3:\n%s", use, err, out)
		}
	}
}

// TestErgoRunE4DiscardVariadic (G9 round 2 M2) is a property over the two
// lowering shapes of a user extern that the pure allowlist still reaches
// and whose call is seen only by its effect on the run: a declaration with
// no result over a Go function that returns one, and a variadic Go target.
// Each program crosses:
//   - the target: strings.NewReplacer (variadic ...string) at arity 0 to
//     5, and strings.Repeat (string, int) with a count of 2 or -1;
//   - each argument as a literal or a nonconstant (a let), all literal,
//     all nonconstant and alternating;
//   - the use: a statement in main, or a statement in a function literal
//     that main calls.
//
// Oracle, from Go's own rules: NewReplacer panics "odd argument count" at
// an odd arity and runs at an even one; Repeat panics "negative Repeat
// count" at -1 and runs at 2. A panic is a defect: exit 2, the panic
// text, the Ergo call's line in the trace, and no later output. A run
// prints "end" and exits 0. So a lowering that drops a no-result call, or
// a variadic call, runs where Go would panic, and fails here.
func TestErgoRunE4DiscardVariadic(t *testing.T) {
	needsBuild(t)
	type want struct {
		panic string // "" when the call runs
		line  int
	}
	var builds []*lowered
	wants := map[string]want{}
	add := func(name, decl string, args []string, defect string) {
		for _, mix := range []string{"literal", "nonconstant", "alternating"} {
			var lets, actual []string
			for i, a := range args {
				if mix == "nonconstant" || mix == "alternating" && i%2 == 1 {
					v := fmt.Sprintf("v%d", i)
					lets = append(lets, "\tlet "+v+" = "+a+"\n")
					a = v
				}
				actual = append(actual, a)
			}
			call := "F(" + strings.Join(actual, ", ") + ")"
			for _, use := range []string{"statement", "closure"} {
				body := strings.Join(lets, "")
				line := 3 + len(lets)
				if use == "statement" {
					body += "\t" + call + "\n"
				} else {
					body += "\tlet g = func() {\n\t\t" + call + "\n\t}\n\tg()\n"
					line++
				}
				src := decl + "\nfunc main() needs Console {\n" + body + "\tConsole.Println(\"end\")\n}\n"
				o := mustLower(t, "e4_discard_"+name+"_"+mix+"_"+use, src)
				builds = append(builds, o)
				wants[o.name] = want{defect, line}
				if checked, bad := e4CallChecks(o.mainGo(), decl[len("extern go "):strings.Index(decl, " func")]); checked < 1 || len(bad) > 0 {
					t.Errorf("%s: %d checked calls; unchecked: %q\n%s", o.name, checked, bad, o.mainGo())
				}
			}
		}
	}
	for arity := 0; arity <= 5; arity++ {
		var params, args []string
		for i := 0; i < arity; i++ {
			params = append(params, fmt.Sprintf("p%d string", i))
			args = append(args, fmt.Sprintf("%q", fmt.Sprintf("s%d", i)))
		}
		defect := ""
		if arity%2 == 1 {
			defect = "panic: strings.NewReplacer: odd argument count"
		}
		add(fmt.Sprintf("replacer%d", arity), "extern go strings.NewReplacer func F("+strings.Join(params, ", ")+")", args, defect)
	}
	for _, n := range []string{"2", "-1"} {
		defect := ""
		if n == "-1" {
			defect = "panic: strings: negative Repeat count"
		}
		add("repeat_"+strings.Replace(n, "-", "neg", 1), "extern go strings.Repeat func F(s string, n int)", []string{`"x"`, n}, defect)
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, o := range builds {
		w := wants[o.name]
		stdout, stderr, code := runExe(t, exes[o.name])
		if w.panic == "" {
			if code != 0 || stdout != "end\n" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q; want a run that prints end\n%s", o.name, code, stdout, firstLines(stderr, 8), o.src)
			}
			continue
		}
		at := regexp.MustCompile(fmt.Sprintf(`(?m)main\.ergo:%d$`, w.line))
		if code != 2 || stdout != "" || !strings.Contains(stderr, w.panic) || !at.MatchString(stderr) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 2 with %q at main.ergo:%d\n%s", o.name, code, stdout, firstLines(stderr, 8), w.panic, w.line, o.src)
		}
	}
}

// e4Value is an Ergo expression of the basic type k, a literal where Ergo
// has one, and its fmt.Sprint text. Ergo has no imaginary literal
// (syntax/scanner.go), so a complex value is a conversion of an integer.
func e4Value(t *testing.T, k *check.Basic) (expr, text string) {
	t.Helper()
	switch {
	case k.Kind == check.Bool:
		return "true", "true"
	case k.Kind == check.String:
		return `"x"`, "x"
	case k.Kind >= check.Int && k.Kind <= check.Uintptr:
		return "3", "3"
	case k.Kind == check.Float32 || k.Kind == check.Float64:
		return "1.5", "1.5"
	case k.Kind == check.Complex64:
		return "complex64(1)", "(1+0i)"
	case k.Kind == check.Complex128:
		return "complex128(1)", "(1+0i)"
	case k.Kind == check.Duration:
		return "3ns", "3ns"
	}
	t.Fatalf("no Ergo value of the basic type %s", k.Name)
	return "", ""
}

// TestErgoRunE4TargetCategories (round 1 M2, round 2 M3) is a property
// over the Go target's category crossed with every basic type the checker
// admits as a parameter (check.Typ, Bool to Duration) and with a literal
// and a nonconstant argument. Since G9 (std-1) a user extern's target is
// in the pure allowlist, which has no generic function over basic types
// and no named basic type, so two categories remain here: a function, and
// a constant. (Its one variadic function, strings.NewReplacer, returns a
// *Replacer, so only a no-result declaration binds it:
// TestErgoRunE4DiscardVariadic.) Oracle: a function runs and prints the value;
// a constant fails go build, and every error names the call's line (5).
// Each call of a target in the lowered Go follows its call check, `if
// false { defer CALL }` with the same CALL text, under the call's //line
// directive. The categories G9 removed are e4HostTargets' S109 rows.
func TestErgoRunE4TargetCategories(t *testing.T) {
	needsBuild(t)
	// A function of the allowlist that takes the parameter type, where one
	// exists (none takes int8, int16, uint, uint8 first, uint16, uintptr,
	// complex64 or duration). Its call is an Ergo string expression.
	fns := map[check.BasicKind]struct{ decl, call, out string }{
		check.Bool:       {"strconv.FormatBool func Fn(x bool) string", "Fn({arg})", "true"},
		check.String:     {"strings.ToUpper func Fn(x string) string", "Fn({arg})", "X"},
		check.Int:        {"strconv.Itoa func Fn(x int) string", "Fn({arg})", "3"},
		check.Int32:      {"unicode/utf8.RuneLen func Fn(x int32) int", `"\(Fn({arg}))"`, "1"},
		check.Int64:      {"strconv.FormatInt func Fn(x int64, base int) string", "Fn({arg}, 10)", "3"},
		check.Uint32:     {"math.Float32frombits func Fn(x uint32) float32", `"\(Fn({arg}))"`, "4e-45"},
		check.Uint64:     {"strconv.FormatUint func Fn(x uint64, base int) string", "Fn({arg}, 10)", "3"},
		check.Float32:    {"math.Float32bits func Fn(x float32) uint32", `"\(Fn({arg}))"`, "1069547520"},
		check.Float64:    {"math.Abs func Fn(x float64) float64", `"\(Fn({arg}))"`, "1.5"},
		check.Complex128: {"strconv.FormatComplex func Fn(x complex128, f uint8, prec int, size int) string", "Fn({arg}, 103, -1, 128)", "(1+0i)"},
	}
	// Not a function: a constant.
	others := []struct{ name, target, result string }{
		{"constant", "math.MaxInt8", ""}, // the result is the parameter's type
	}
	var runs, fails []*lowered
	want := map[string]string{}
	for _, k := range check.Typ[check.Bool:] {
		lit, _ := e4Value(t, k)
		// The function, with the literal and the nonconstant argument.
		if fn, ok := fns[k.Kind]; ok {
			decls := "extern go " + fn.decl + "\n"
			expr := strings.ReplaceAll(fn.call, "{arg}", lit) + ` + " " + ` + strings.ReplaceAll(fn.call, "{arg}", "a")
			src := decls + "func main() needs Console {\n\tlet a " + k.Name + " = " + lit + "\n\tConsole.Println(" + expr + ")\n}\n"
			o := mustLower(t, "e4_target_"+k.Name, src)
			runs = append(runs, o)
			want[o.name] = fn.out + " " + fn.out + "\n"
			target, _, _ := strings.Cut(fn.decl, " ")
			target = target[strings.LastIndex(target, "/")+1:] // the Go call names the package, utf8.RuneLen
			if checked, bad := e4CallChecks(o.mainGo(), target); checked < 2 || len(bad) > 0 {
				t.Errorf("%s: %d calls of %s follow their check; calls with no check under their //line directive: %q\n%s", o.name, checked, target, bad, o.mainGo())
			}
		}
		// The constant.
		for _, c := range others {
			result := c.result
			if result == "" {
				result = k.Name
			}
			for _, arg := range []struct{ name, line4, expr string }{
				{"literal", "// a literal argument", lit},
				{"nonconstant", "let a " + k.Name + " = " + lit, "a"},
			} {
				src := fmt.Sprintf("extern go %s func Conv(x %s) %s\nfunc consume(v %s) {}\nfunc main() {\n\t%s\n\tconsume(Conv(%s))\n}\n", c.target, k.Name, result, result, arg.line4, arg.expr)
				fails = append(fails, mustLower(t, "e4_target_"+k.Name+"_"+c.name+"_"+arg.name, src))
			}
		}
	}
	errs := e4BuildErrors(t, fails)
	for _, o := range fails {
		if !e4FailsAt(errs[o.name], 5) {
			t.Errorf("%s: go build does not fail at the call's line 5 only:\n%s\nsource:\n%s\nGo:\n%s", o.name, errs[o.name], o.src, o.mainGo())
		}
	}
	exes := buildAll(t, runs, true)
	if exes == nil {
		return
	}
	for _, o := range runs {
		stdout, stderr, code := runExe(t, exes[o.name])
		if code != 0 || stdout != want[o.name] {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want %q\nsource:\n%s", o.name, code, stdout, firstLines(stderr, 8), want[o.name], o.src)
		}
	}
}

// C30: the context key namespace keys on package identity. A program
// package checked under the path log declares a context value whose key
// equals one std log binds, and reads std log's binding: its own binding
// never shadows std's, and std's read never sees it.
func TestContextKeyIdentity(t *testing.T) {
	needsBuild(t)
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
func Poll(_ Mine) Option[int] { None }
func Len(_ Mine) int { 0 }
func use(q queue.Queue[int]) { q.Close() }
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
