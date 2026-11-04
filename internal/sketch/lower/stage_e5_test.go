package lower

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// Check-v1 stage E5 (196x, D-E5): a local context is a recipe, built at each
// with that names it, and `with A, B { b }` is `with A { with B { b } }`.
// Each row is one program, built and run in one sweep (A7).
type e5Run struct {
	name, src, want string
}

// e5Prelude declares two capabilities, their providers and the named
// context Live.
const e5Prelude = `type Greeter {
	Greet(name string) string,
}

type Store {
	Get() string,
}

type Loud {}
type Plain {}
type Holder { v string }

func Greet(_ Loud, name string) string {
	"\(name)!"
}

func Greet(_ Plain, name string) string {
	name
}

func Get(h Holder) string {
	h.v
}

let Live = context {
	Greeter: Loud,
}

func hello() string needs Greeter {
	Greeter.Greet("ada")
}
`

var e5Contexts = []e5Run{
	// accept/runs/Esub5-contexts.ergo, verbatim.
	{"esub5", `// Stage E5 (196x, D-E5): a local context, an inline context and
// ` + "`with A, B`" + `, which is ` + "`with A { with B { b } }`" + `, checked and lowered.
type Greeter {
	Greet(name string) string,
}

type Loud {}
type Plain {}

func Greet(_ Loud, name string) string {
	"\(name)!"
}

func Greet(_ Plain, name string) string {
	name
}

let Live = context {
	Greeter: Loud,
}

func hello() string needs Greeter {
	Greeter.Greet("ada")
}

func main() needs Console {
	let quiet = context { Greeter: Plain }
	Console.Println(with Live { hello() })
	Console.Println(with quiet { hello() })
	with Live, quiet {
		Console.Println(hello())
	}
	with context { Greeter: Loud } {
		Console.Println(hello())
	}
}
`, "ada!\nada\nada\nada!\n"},
	// A recipe, not a value: each with builds the provider again.
	{"made-twice", e5Prelude + `
func made() Holder needs Console {
	Console.Println("made")
	Holder{v: "x"}
}

func main() needs Console {
	let w = context { Store: made() }
	with w { Console.Println(Store.Get()) }
	with w { Console.Println(Store.Get()) }
}
`, "made\nx\nmade\nx\n"},
	// A provider reads the let's locals at the with that builds it.
	{"let-mut", e5Prelude + `
func main() needs Console {
	let mut name = "ada"
	let w = context { Store: Holder{v: name} }
	with w { Console.Println(Store.Get()) }
	name = "bo"
	with w { Console.Println(Store.Get()) }
}
`, "ada\nbo\n"},
	// B sees A: w's provider calls Greeter, which the operand before it binds.
	{"b-sees-a", e5Prelude + `
func shout() Holder needs Greeter {
	Holder{v: Greeter.Greet("ada")}
}

func main() needs Console {
	let w = context { Store: shout() }
	with Live, w { Console.Println(Store.Get()) }
	with context { Greeter: Plain }, w { Console.Println(Store.Get()) }
	with Live {
		with context { Greeter: Plain } {
			with w { Console.Println(Store.Get()) }
		}
	}
}
`, "ada!\nada\nada\n"},
	// A local context embedded in an inline context, with a replace.
	{"embed-replace", e5Prelude + `
func main() needs Console {
	let w = context { Greeter: Plain, Store: Holder{v: "w"} }
	with context { w, replace Greeter: Loud } {
		Console.Println("\(Greeter.Greet("ada")) \(Store.Get())")
	}
	let v = context { w }
	with v { Console.Println("\(Greeter.Greet("bo")) \(Store.Get())") }
}
`, "ada! w\nbo w\n"},
	// An embedded local context's provider sees the bindings outside the
	// with, not the entries of the literal that embeds it (rows.md, Context
	// 3 and 9): shout() reads Live's Greeter, not Plain.
	{"embed-sees-outer", e5Prelude + `
func shout() Holder needs Greeter {
	Holder{v: Greeter.Greet("ada")}
}

func main() needs Console {
	let a = context { Greeter: Plain }
	let w = context { Store: shout() }
	with Live {
		with context { Greeter: Plain, w } {
			Console.Println("\(Greeter.Greet("bo")) \(Store.Get())")
		}
		with context { a, w } {
			Console.Println("\(Greeter.Greet("cy")) \(Store.Get())")
		}
	}
}
`, "bo ada!\ncy ada!\n"},
	// A with of a local context inside a function literal that runs after
	// the block that defines it has ended.
	{"literal", e5Prelude + `
func make(name string) escaping func() string {
	let n = name + "!"
	let w = context { Store: Holder{v: n} }
	func() string {
		with w { Store.Get() }
	}
}

func main() needs Console {
	let f = make("ada")
	let g = make("bo")
	Console.Println(f())
	Console.Println(g())
	Console.Println(f())
}
`, "ada!\nbo!\nada!\n"},
	// A provider with a Close: each with acquires and releases it.
	{"close-twice", e5Prelude + `
type Conn {}

func Get(_ Conn) string {
	"conn"
}

func open() Conn needs Console {
	Console.Println("open")
	Conn
}

func Close(_ Conn) needs Console {
	Console.Println("close")
}

func main() needs Console {
	let w = context { Store: open() }
	with w { Console.Println(Store.Get()) }
	with w { Console.Println(Store.Get()) }
}
`, "open\nconn\nclose\nopen\nconn\nclose\n"},
	// rows.md With 7 (E5 round 1 M3): where a and b both bind Store, a's
	// Store is built and released only when the with acquires it, as in
	// the nesting: here when b's Greeter provider reads Store.
	{"shadowed-unbuilt", e5Prelude + `
type Conn { n string }

func Get(c Conn) string {
	c.n
}

func Greet(c Conn, name string) string {
	"\(c.n):\(name)"
}

func open(n string) Conn needs Console {
	Console.Println("open \(n)")
	Conn{n: n}
}

func Close(c Conn) needs Console {
	Console.Println("close \(c.n)")
}

func main() needs Console {
	let a = context { Store: open("a") }
	let b = context { Store: open("b") }
	with a, b { Console.Println("in \(Store.Get())") }
	with a {
		with b { Console.Println("in \(Store.Get())") }
	}
	let c = context { Store: open("c"), Greeter: open("g" + Store.Get()) }
	with a, c { Console.Println("in \(Store.Get()) \(Greeter.Greet("x"))") }
}
`, "open b\nin b\nclose b\nopen b\nin b\nclose b\nopen a\nopen c\nopen ga\nin c ga:x\nclose ga\nclose c\nclose a\n"},
	// A context value and a context in one with print what the nested
	// forms print.
	{"value-and-context", "import trace\n\n" + e5Prelude + `
func main() needs Console {
	with trace.Span("x"), Live { Console.Println(hello()) }
	with trace.Span("x") {
		with Live { Console.Println(hello()) }
	}
	with Live, trace.Span("x") { Console.Println(hello()) }
	with Live {
		with trace.Span("x") { Console.Println(hello()) }
	}
}
`, "ada!\nada!\nada!\nada!\n"},
	// A with of 50 operands: the innermost binding of Greeter wins.
	{"fifty", "import trace\n\n" + e5Prelude + `
func main() needs Console {
	let quiet = context { Greeter: Plain }
	with ` + fiftyOperands() + ` { Console.Println(hello()) }
}
`, "ada\n"},
	// A local context that no with builds: its let lowers to nothing, and
	// the local its provider reads still compiles.
	{"unbuilt", e5Prelude + `
func main() needs Console {
	let n = "x"
	let w = context { Store: Holder{v: n} }
	Console.Println("ok")
}
`, "ok\n"},
}

// fiftyOperands cycles over the four operand kinds; the last is the local
// context quiet, so the innermost Greeter is Plain.
func fiftyOperands() string {
	kinds := []string{"Live", "quiet", "context { Greeter: Loud }", `trace.Span("s")`}
	ops := make([]string, 50)
	for i := range ops {
		ops[i] = kinds[i%len(kinds)]
	}
	return strings.Join(ops, ", ")
}

func TestErgoRunE5Contexts(t *testing.T) {
	var builds []*lowered
	for _, r := range e5Contexts {
		builds = append(builds, mustLower(t, "e5_"+strings.ReplaceAll(r.name, "-", "_"), r.src))
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, r := range e5Contexts {
		stdout, stderr, code := runExe(t, exes["e5_"+strings.ReplaceAll(r.name, "-", "_")])
		if code != 0 || stdout != r.want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want %q", r.name, code, stdout, firstLines(stderr, 8), r.want)
		}
	}
}

// A local context has no value at run time (rows.md, Context 9; the owner's
// zero-cost rule for code without effects): its let writes no Go, and each
// with writes the provider expressions in place, with no closure and no
// recipe value.
func TestLocalContextLowersInPlace(t *testing.T) {
	src := e5Prelude + `
func made() Holder {
	Holder{v: "x"}
}

func use() string {
	let w = context { Store: made() }
	let a = with w { Store.Get() }
	let b = with w { Store.Get() }
	a + b
}

func main() needs Console {
	Console.Println(use())
}
`
	o := mustLower(t, "e5_in_place", src)
	code := o.mainGo()
	body := goFunc(t, code, "use")
	if n := len(regexp.MustCompile(`\bmade\(`).FindAllString(body, -1)); n != 2 {
		t.Errorf("use calls made %d times in its Go body, want 2 (once per with):\n%s", n, body)
	}
	if strings.Contains(body, "func(") {
		t.Errorf("use's Go body has a closure; a local context is no value:\n%s", body)
	}
}

// goFunc is the Go text of the lowered function whose name starts with
// name followed by an underscore or a parenthesis.
func goFunc(t *testing.T, code, name string) string {
	t.Helper()
	re := regexp.MustCompile(fmt.Sprintf(`(?m)^func %s(_[0-9]+)?\(`, regexp.QuoteMeta(name)))
	loc := re.FindStringIndex(code)
	if loc == nil {
		t.Fatalf("no Go function %s in:\n%s", name, code)
	}
	// The closing brace may carry a //line directive comment.
	end := regexp.MustCompile(`(?m)^(/\*line [^*]*\*/)?}$`).FindStringIndex(code[loc[0]:])
	if end == nil {
		t.Fatalf("Go function %s has no end", name)
	}
	return code[loc[0] : loc[0]+end[1]]
}

// The lowering's context-literal value branch (expr.go) is an internal
// error: the checker refuses a context literal in every value place first
// (S108), so no checked program reaches it.
func TestContextLiteralValueIsRefusedByTheChecker(t *testing.T) {
	for _, body := range []string{
		"let v = Holder{v: context { Greeter: Plain }}\n\t_ = v",
		"let v = []Holder{}\n\tlet n = len(v) + context { Greeter: Plain }\n\t_ = n",
		"let f = func(x int) int { x }\n\t_ = f(context { Greeter: Plain })",
		"Console.Println(context { Greeter: Plain })",
		"let mut w = context { Greeter: Plain }\n\twith w { Console.Println(hello()) }",
		"let w Greeter = context { Greeter: Plain }",
	} {
		o := lowerSource("e5_value", e5Prelude+"\nfunc main() needs Console {\n\t"+body+"\n}\n", true)
		if o.err != nil {
			t.Errorf("%q: %v", body, o.err)
			continue
		}
		found := false
		for _, d := range o.diags {
			if d.Code == "S108" {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: the checker gives %v, want an S108 before the lowering", body, o.diags)
		}
	}
}
