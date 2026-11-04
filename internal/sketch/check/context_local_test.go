package check_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// localPrelude declares what the local-context rules name: three
// capabilities, providers for them, a function that needs Store, a
// context-value type (runtime.md, G1) and two package contexts.
const localPrelude = `type Greeter {
	Greet(name string) string,
}

type Store {
	Get() string,
}

type Timer {
	Now() int,
}

type Plain {}
type Loud {}
type Holder { v string }
type Fixed {}
type Tag { s string }
type Box { w int }

func Greet(_ Plain, name string) string {
	name
}

func Greet(_ Loud, name string) string {
	name
}

func Get(h Holder) string {
	h.v
}

func Now(_ Fixed) int {
	1
}

func read() string needs Store {
	Store.Get()
}

func contextKey(t Tag) string {
	"tag"
}

func contextText(t Tag) string {
	t.s
}

func use(n int) int {
	n
}

let Live = context { Greeter: Plain }

let Stored = context { Store: Holder{v: "pkg"} }

`

// localRule is one program of TestLocalContextRules: its body follows
// localPrelude, and its `// want CODE` lines are its exact diagnostics.
type localRule struct {
	name, body string
}

// localRules are the rules of Design A2 (check-v1 E5 spec): each S108 form
// once in each body kind, shadowing, embedding, the charge site inside a
// function literal, every ordered pair of operand kinds and the refusals
// that stay.
func localRules() []localRule {
	rules := []localRule{
		// The planner's probes, with their verdicts after E5.
		{"probe-local", "func main() needs Console {\n\tlet quiet = context { Greeter: Plain }\n\tConsole.Println(with quiet { Greeter.Greet(\"ada\") })\n}\n"},
		{"probe-several", "func main() needs Console {\n\twith Stored, context { Greeter: Plain, Store: Holder{v: read()} } {\n\t\tConsole.Println(\"\\(Greeter.Greet(\"ada\")) \\(Store.Get())\")\n\t}\n}\n"},
		{"probe-escape", "func main() needs Console {\n\tlet w = context { Greeter: Plain }\n\tlet v = w // want S108\n\tlet mut m = context { Greeter: Plain } // want S108\n\tConsole.Println(with context { w } { Greeter.Greet(\"ada\") })\n\tConsole.Println(with m { Greeter.Greet(\"bo\") })\n\t_ = v\n}\n"},
		{"probe-inline", "func main() needs Console {\n\tConsole.Println(with context { Greeter: Plain } { Greeter.Greet(\"ada\") })\n\tConsole.Println(with (Live) { Greeter.Greet(\"bo\") })\n}\n"},

		// The other placements of a context.
		{"let-typed", "func f() string {\n\tlet w Greeter = context { Greeter: Plain } // want S108\n\twith w { Greeter.Greet(\"a\") }\n}\n"},
		{"assign", "func f() {\n\tlet w = context { Greeter: Plain }\n\tw = 1 // want S108\n\twith w { Greeter.Greet(\"a\") }\n}\n"},
		{"literal-argument", "func f() int {\n\tuse(context { Greeter: Plain }) // want S108\n}\n"},
		{"literal-tuple", "func f() int {\n\tlet (a, b) = (context { Greeter: Plain }, 1) // want S108\n\t_ = a\n\tb\n}\n"},
		{"literal-provider", "func f() string {\n\tlet w = context { Greeter: context { Greeter: Plain } } // want S108\n\twith w { Greeter.Greet(\"a\") }\n}\n"},
		{"literal-blank", "func f() {\n\tlet _ = context { Greeter: Plain } // want S108\n}\n"},

		// Shadowing (D-D2): a local named like a package context wins in its
		// scope; one named like an enclosing local is S108.
		{"shadow-package", "func f() string {\n\tlet Live = context { Store: Holder{v: \"x\"} }\n\twith Live { Store.Get() }\n}\n"},
		{"shadow-local", "func f(b bool) string {\n\tlet w = context { Greeter: Plain }\n\tif b {\n\t\tlet w = context { Greeter: Loud } // want S108\n\t\t_ = with w { Greeter.Greet(\"a\") }\n\t}\n\twith w { Greeter.Greet(\"b\") }\n}\n"},
		{"shadow-same-block", "func f() string {\n\tlet w = context { Greeter: Plain }\n\tlet w = context { Greeter: Loud } // want S108\n\twith w { Greeter.Greet(\"b\") }\n}\n"},
		{"sibling-blocks", "func f(b bool) string {\n\tif b {\n\t\tlet w = context { Greeter: Plain }\n\t\twith w { Greeter.Greet(\"a\") }\n\t} else {\n\t\tlet w = context { Store: Holder{v: \"x\"} }\n\t\twith context { w } { Store.Get() }\n\t}\n}\n"},

		// Embedding.
		{"embed-in-inline", "func f() string {\n\tlet w = context { Greeter: Plain }\n\twith context { Store: Holder{v: \"x\"}, w } { Greeter.Greet(Store.Get()) }\n}\n"},
		{"embed-in-local", "func f() string {\n\tlet w = context { Greeter: Plain }\n\tlet v = context { Store: Holder{v: \"x\"}, w }\n\twith v { Greeter.Greet(Store.Get()) }\n}\n"},
		{"embed-chain", "func f() string {\n\tlet a = context { Greeter: Plain }\n\tlet b = context { a }\n\tlet c = context { replace Greeter: Loud, b, Live }\n\twith c { Greeter.Greet(\"x\") }\n}\n"},
		{"embed-duplicate", "func f() string {\n\tlet w = context { Greeter: Plain }\n\twith context { Greeter: Loud, w } { Greeter.Greet(\"x\") } // want S111\n}\n"},
		{"embed-replace-unbound", "func f() string {\n\tlet w = context { Greeter: Plain }\n\twith context { replace Store: Holder{v: \"x\"}, w } { Store.Get() } // want S108\n}\n"},
		{"embed-two-locals-collide", "func f() string {\n\tlet a = context { Greeter: Plain }\n\tlet b = context { Greeter: Loud }\n\twith context { a, b } { Greeter.Greet(\"x\") } // want S111\n}\n"},
		{"embed-local-needs", "func f() string {\n\tlet w = context { Store: Holder{v: read()} }\n\twith context { Greeter: Plain, w } { Store.Get() } // want S102\n}\n\nfunc g() string needs Store {\n\tlet w = context { Store: Holder{v: read()} }\n\tlet v = context { Greeter: Plain, w }\n\twith v { Store.Get() }\n}\n"},
		{"embed-own-literal", "func f() string {\n\tlet w = context { w, Greeter: Plain } // want S104\n\twith w { Greeter.Greet(\"x\") }\n}\n"},
		{"package-cannot-embed-local", "func f() string {\n\tlet w = context { Greeter: Plain }\n\twith w { Greeter.Greet(\"x\") }\n}\n\nlet P = context { w } // want S104\n"},

		// The charge site (C34): needs at each with, failures to its function.
		{"charge-written", "func f() string needs Store {\n\tlet w = context { Greeter: Loud, Store: Holder{v: read()} }\n\twith w { Greeter.Greet(\"x\") }\n}\n"},
		{"charge-inferred", "func f() string {\n\tlet w = context { Store: Holder{v: read()} }\n\twith w { Store.Get() } // want S102\n}\n"},
		{"charge-enclosing-with", "func f() string {\n\tlet w = context { Greeter: Loud, Store: Holder{v: read()} }\n\twith Stored {\n\t\twith w { Greeter.Greet(\"x\") }\n\t}\n}\n"},
		{"charge-let-inside-with", "func f() string {\n\twith Stored {\n\t\tlet w = context { Greeter: Loud, Store: Holder{v: read()} }\n\t\tlet g = func() string {\n\t\t\twith w { Greeter.Greet(\"x\") } // want S102\n\t\t}\n\t\tg()\n\t}\n}\n"},
		{"charge-each-with", "func f() string needs Store {\n\tlet w = context { Store: Holder{v: read()} }\n\tlet a = with w { Store.Get() }\n\tlet b = with w { Store.Get() }\n\ta + b\n}\n\nfunc g() string {\n\tlet w = context { Store: Holder{v: read()} }\n\twith Stored {\n\t\tlet a = with w { Store.Get() }\n\t\t_ = a\n\t}\n\twith w { Store.Get() } // want S102\n}\n"},
		{"charge-literal-row", "func f() int {\n\tlet w = context { Store: Holder{v: read()} }\n\tlet g = func() string needs Store {\n\t\twith w { Store.Get() }\n\t}\n\t_ = g\n\t1\n}\n\nfunc h() escaping func() string {\n\tlet w = context { Store: Holder{v: read()} }\n\tfunc() string {\n\t\twith w { Store.Get() } // want S102\n\t}\n}\n"},

		// The header and a constructor entry stay refused (#196 stage b, #37).
		{"local-header", "func f() string {\n\tlet w = context provides Greeter { Greeter: Plain } // want S109\n\twith w { Greeter.Greet(\"x\") }\n}\n"},
		{"local-constructor", "func mk() Plain {\n\tPlain\n}\n\nfunc f() string {\n\tlet w = context { Greeter: mk } // want S109\n\twith w { Greeter.Greet(\"x\") }\n}\n"},
	}
	// Each S108 use of a local context, once in each body kind: a function,
	// a function literal, a test body and a law body (Design A2).
	uses := []struct{ name, stmt string }{
		{"argument", "_ = use(w) // want S108"},
		{"let", "let u = w // want S108\n\t_ = u"},
		{"field", "_ = Box{w: w} // want S108"},
		{"element", "_ = []int{w} // want S108"},
		{"operand", "_ = w + 1 // want S108"},
		{"selector", "_ = w.x // want S108"},
		{"call", "_ = w() // want S108"},
		{"interpolation", "_ = \"\\(w)\" // want S108"},
		{"capture", "let g = func() int { w } // want S108\n\t_ = g"},
		{"paren", "_ = use((w)) // want S108"},
		{"index", "let xs = []int{1}\n\t_ = xs[w] // want S108"},
	}
	for _, u := range uses {
		body := "\tlet w = context { Greeter: Plain }\n\t" + u.stmt + "\n\t_ = with w { Greeter.Greet(\"x\") }\n"
		rules = append(rules,
			localRule{"func-" + u.name, "func f() {\n" + body + "}\n"},
			localRule{"lit-" + u.name, "func f() {\n\tlet run = func() {\n" + indent(body) + "\t}\n\trun()\n}\n"},
			localRule{"test-" + u.name, "test T {\n" + body + "}\n"},
			localRule{"law-" + u.name, "type Law {\n\tOp(v int) int,\n\n\tlaw Holds(v int) {\n" + indent(body) + "\t\tOp(v) == v\n\t},\n}\n"},
		)
	}
	rules = append(rules, localRule{"func-return", "func f() int {\n\tlet w = context { Greeter: Plain }\n\treturn w // want S108\n}\n"})
	rules = append(rules, localRule{"func-tail", "func f() int {\n\tlet w = context { Greeter: Plain }\n\tw // want S108\n}\n"})
	// with A, B for each ordered pair of operand kinds, both binding Greeter
	// or one binding Store: clean, never the old several-operand S109.
	kinds := map[string]struct{ setup, op string }{
		"named":  {"", "Live"},
		"local":  {"\tlet l = context { Store: Holder{v: \"l\"} }\n", "l"},
		"inline": {"", "context { Greeter: Loud }"},
		"value":  {"", "(Tag{s: \"t\"})"},
	}
	for _, a := range []string{"named", "local", "inline", "value"} {
		for _, b := range []string{"named", "local", "inline", "value"} {
			setup := kinds[a].setup
			if b != a {
				setup += kinds[b].setup
			}
			rules = append(rules, localRule{"pair-" + a + "-" + b, "func f() int {\n" + setup + "\twith " + kinds[a].op + ", " + kinds[b].op + " { 1 }\n}\n"})
		}
	}
	return rules
}

// indent adds one tab to each line of a body.
func indent(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "\t" + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestLocalContextRules (check-v1 E5, A2): each program gets exactly its
// `// want` diagnostics, with no cascade; a clean program's Info is
// complete. The E5 acceptance rules (testdata/v1) are judged the same way.
func TestLocalContextRules(t *testing.T) {
	for _, r := range localRules() {
		t.Run(r.name, func(t *testing.T) {
			judgeLocal(t, localPrelude+r.body)
		})
	}
	for _, name := range []string{"E6-extern-contexts.pos", "E6-extern-contexts.neg", "Esub5-context-charge.pos", "Esub5-context-charge.neg"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "v1", name+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			judgeLocal(t, string(data))
		})
	}
}

// judgeLocal checks src: exactly its want lines; Info complete when clean;
// every S108 of a context's use carries one of the Design A2 messages.
func judgeLocal(t *testing.T, src string) {
	t.Helper()
	_, diags := check.CheckSource(src)
	want, have := expected(src), got(diags)
	if strings.Join(want, " ") != strings.Join(have, " ") {
		t.Fatalf("want %v, got %v\n\t%s\n%s", want, have, describe(diags), numbered(src))
	}
	for _, d := range diags {
		if strings.Contains(d.Message, "not checked yet") && strings.Contains(d.Message, "196x") {
			t.Errorf("a 196x refusal is left: %s", d)
		}
	}
	if len(diags) == 0 {
		if gaps := check.InfoGaps(src); len(gaps) > 0 {
			t.Errorf("Info gaps:\n\t%s", strings.Join(gaps, "\n\t"))
		}
	}
}

// numbered prints src with line numbers, for a failure.
func numbered(src string) string {
	var b strings.Builder
	for i, l := range strings.Split(src, "\n") {
		fmt.Fprintf(&b, "%3d %s\n", i+1, l)
	}
	return b.String()
}
