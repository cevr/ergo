package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestImportGoTwoPackages is #202's acceptance at Level 1 (IG2): two
// unrelated Go packages, net/url and regexp, check clean with no
// declaration written by the user: functions, a handle (go.Ref) as a
// result and a parameter, its methods and a read-only field, a nil-able
// result as Option, a typed constant, fails go.Error under try and catch,
// and go.Foreign in written rows. Mutant it alone catches: a host type's
// methods or fields not found through go.Ref.
func TestImportGoTwoPackages(t *testing.T) {
	src := `import go net/url
import go regexp
import go math

func parse(s string) go.Ref[url.URL] fails go.Error needs go.Foreign {
	try url.Parse(s)
}

func Host(s string) string fails go.Error needs go.Foreign {
	let u = try parse(s)
	u.Hostname() + "|" + u.Host
}

func Digits(s string) []string needs go.Foreign {
	match regexp.MustCompile("[0-9]+") {
		case Some as re: re.Value.FindAllString(s, -1)
		case None: []string{}
	}
}

func Rel(base string, ref string) string needs go.Foreign {
	let b = parse(base) catch {
		case go.Error: return ""
	}
	let r = parse(ref) catch {
		case go.Error: return ""
	}
	match b.ResolveReference(r) {
		case Some as u: u.Value.String()
		case None: ""
	}
}

func Tau() float64 { 2.0 * math.Pi }

func main() needs Console | go.Foreign {
	Console.Println(Host("https://example.com:8080/x") catch { case go.Error as e: e.Text })
	Console.Println(url.QueryEscape("a b"))
	Console.Println(Rel("https://a.org/x/", "../y"))
	let _ = Digits("a1b22")
}
`
	if _, d := check.CheckSource(src); len(d) != 0 {
		t.Fatalf("want clean, got\n\t%s", describe(d))
	}
}

// TestImportGoRows: the negative controls of #202's acceptance. A foreign
// call in an exported function whose row lacks go.Foreign is S102, a
// go.Error that escapes its row is S101, and a test never gets go.Foreign
// (it has no row and no context can bind it; rows.md, Entry 4).
func TestImportGoRows(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"foreign", "import go net/url\nfunc Escape(s string) string { url.QueryEscape(s) }\n",
			"2:32:S102 this call requires go.Foreign, which is not in the capability row of function Escape and no with provides it"},
		{"error", "import go strconv\nfunc N(s string) int { try strconv.Atoi(s) }\n",
			"2:24:S101 {go.Error} escapes function N, whose error row {} does not list it"},
		{"test", "import go net/url\nfunc esc(s string) string { url.QueryEscape(s) }\ntest Esc { assert esc(\"a\") == \"a\" }\n",
			"3:19:S102 this call requires go.Foreign, which is not in the capability row of test Esc and no with provides it"},
		// go.Foreign's interface is empty, so only this rule keeps a
		// context (in a test, say) from providing it.
		{"test-context", "import go net/url\ntype Fake {}\nfunc esc(s string) string { url.QueryEscape(s) }\ntest Esc { with context { go.Foreign: Fake } { assert esc(\"a\") == \"a\" } }\n",
			"4:39:S106 no context binds go.Foreign: its one provider is the program entry, for a main that lists it (rows.md, Entry 4)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			var got []string
			for _, x := range d {
				got = append(got, strings.Join([]string{strconv.Itoa(x.Line), strconv.Itoa(x.Column), x.Code + " " + x.Message}, ":"))
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// TestImportGoUses: the diagnostics of the import go surface, each at its
// place. Mutants they alone catch: a refused declaration diagnosed at the
// import instead of the use; Ergo's path rule instead of Go's
// (module.CheckImportPath: github.com/0xAX/x is a path); a pointer-receiver
// method called on a copy; a borrow kept as a go.Ref.
func TestImportGoUses(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"refused-at-use", "import go math\nfunc F() int { let _ = math.Pi; 0 }\nfunc G() uint64 { math.MaxUint64 }\n",
			"3:19:S109 math.MaxUint64 is not imported from Go: the untyped constant does not fit Go's default type int; Ergo has no untyped constants (196u) (the binding of math refuses it)"},
		{"go-path", "import go github.com/0xAX/x\n",
			`1:11:S104 no binding for Go package "github.com/0xAX/x"; run ergo bind go github.com/0xAX/x (docs/spec/interop.md)`},
		{"bad-path", "import go a/.b.\n",
			`1:11:S108 malformed Go import path "a/.b.": trailing dot in path element`},
		{"major-version", "import go math/rand/v2\nfunc F() int needs go.Foreign { rand.IntN(3) }\n", ""},
		{"pointer-receiver", "import go strings\nfunc F(b strings.Builder) int needs go.Foreign { b.Len() }\n",
			"2:50:S109 the Go method strings.Builder.Len has a pointer receiver; call it on a go.Ref[strings.Builder] or a borrow, not on a copy (docs/spec/interop.md)"},
		{"borrow-kept", "import go net/url\nfunc F(p *url.URL) go.Ref[url.URL] { p }\n",
			"2:38:S108 cannot use *url.URL as go.Ref[url.URL] in the body's tail value: a borrow is not kept as a go.Ref"},
		// wf10 defects D2: a Go function is a value only when its type
		// needs no conversion at the Go edge (check.GoDirectSig), and so
		// is a Go function parameter an Ergo function is passed to.
		{"value-direct", "import go unicode\nimport go strings\nfunc F(s string) string { strings.Map(unicode.ToUpper, s) }\n", ""},
		{"value-slice", "import go strings\nfunc F() int { let f = strings.Fields; len(f(\"a b\")) }\n",
			"2:24:S108 strings.Fields is an extern and is not a value; pass func(s string) []string { strings.Fields(s) } (grammar.md, Extern)"},
		{"callback-slice", "import go regexp\nfunc F(re go.Ref[regexp.Regexp]) []byte needs go.Foreign { re.ReplaceAllFunc([]byte{97}, func(b []byte) []byte { b }) }\n",
			"2:90:S109 a Go function parameter of type func([]uint8) []uint8 takes or gives a value that needs a conversion at the Go edge (a slice, a Map, an Option, a go.Ref or several results); IG6 lowers it (interop.md, Lowering 7)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			var got []string
			for _, x := range d {
				got = append(got, strings.Join([]string{strconv.Itoa(x.Line), strconv.Itoa(x.Column), x.Code + " " + x.Message}, ":"))
			}
			if tc.want == "" && len(got) == 0 {
				return
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}
