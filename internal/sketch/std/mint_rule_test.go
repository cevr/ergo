package std_test

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// mintBuild is one way a body builds a Host: pre is a statement it needs
// first (empty for an expression alone), expr the Host-typed expression.
type mintBuild struct{ name, pre, expr string }

// mintBuilds are the build forms of a record with authority (check.Mints:
// literal, record update, conversion, an elided literal; a record has no
// zero value, so no let or omitted field builds one: types.md, Zero values
// 2 and 3). A bare record and a constant
// build a type that holds no authority; TestMintsEachForm has their rows.
var mintBuilds = []mintBuild{
	{"literal", "", `Host{root: "/"}`},
	{"record update", "", `Host{..Host{root: "/"}, root: "x"}`},
	{"conversion", "", `Host(Host{root: "/"})`},
	{"elided literal", "", `[]Host{{root: "/"}}[0]`},
}

// mintDisguise is how the Host leaves its place: as a value of typ, by
// expr around the build (%s).
type mintDisguise struct{ name, typ, expr string }

// mintDisguises hide the Host behind a type no signature walk names (the
// batch-10 r5 B1 forms: an open operation type, a closure, a call's
// argument) or wrap it (a field of another record, an Option, a slice).
var mintDisguises = []mintDisguise{
	{"open operation type", "Reader", "%s"},
	{"closure", "func() int", "func() int { Get(%s) }"},
	{"call's argument", "int", "Get(%s)"},
	{"field of another record", "Box", "Box{H: %s}"},
	{"Option", "Option[Host]", "Some[Host]{Value: %s}"},
	{"slice", "[]Host", "[]Host{%s}"},
}

// mintPlace is a declaration around the build. decl is its source from
// the result type (@T), the pre statement (@PRE) and the expression (@E);
// allowed is the model: the place holds the authority (stdlib.md, Host
// packages 6). body reports that the place has statements, and onlyOp
// that it takes only a provider of an operation type.
type mintPlace struct {
	name    string
	decl    string
	allowed bool
	body    bool
	onlyOp  bool
}

var mintPlaces = []mintPlace{
	{"function", "func Item() (@T) {\n\t@PRE\n\t@E\n}\n", false, true, false},
	{"function that lists Ambient", "func Item() (@T) needs Ambient {\n\t@PRE\n\t@E\n}\n", true, true, false},
	{"function that takes a Host", "func Item(h Host) (@T) {\n\t@PRE\n\t@E\n}\n", true, true, false},
	{"method of another type", "func Item(o Other) (@T) {\n\t@PRE\n\t@E\n}\n", false, true, false},
	{"unexported function", "func item() (@T) {\n\t@PRE\n\t@E\n}\n\nlet Item = item\n", false, true, false},
	{"closure", "func Item() (func() (@T)) {\n\tfunc() (@T) {\n\t\t@PRE\n\t\t@E\n\t}\n}\n", false, true, false},
	{"closure in a function that lists Ambient", "func Item() (func() (@T)) needs Ambient {\n\tfunc() (@T) {\n\t\t@PRE\n\t\t@E\n\t}\n}\n", true, true, false},
	{"closure that lists Ambient", "func Item() (func() (@T) needs Ambient) {\n\tfunc() (@T) needs Ambient {\n\t\t@PRE\n\t\t@E\n\t}\n}\n", true, true, false},
	{"closure in a function that takes a Host", "func Item(h Host) (func() (@T)) {\n\tfunc() (@T) {\n\t\t@PRE\n\t\t@E\n\t}\n}\n", true, true, false},
	{"let", "let Item @T = @E\n", false, false, false},
	// fu-ambient-ctor r7 (batch-10 r6 T1): a context literal in a function
	// is judged by the function's row.
	{"context literal in a function", "func Item() int {\n\t@PRE\n\twith context { Reader: @E } { 1 }\n}\n", false, true, true},
	{"context literal in a function that lists Ambient", "func Item() int needs Ambient {\n\t@PRE\n\twith context { Reader: @E } { 1 }\n}\n", true, true, true},
	{"named context", "let Item = context { Reader: @E }\n", false, false, true},
	{"test", "test Item {\n\t@PRE\n\tassert keep(@E)\n}\n", false, true, false},
	{"law", "law Item() { keep(@E) }\n", false, false, false},
	{"machine's initial state", "type Idle { V @T }\n\ntype Busy {}\n\ntype MS = Idle | Busy\n\ntype Go {}\n\ntype Stop {}\n\ntype ME = Go | Stop\n\nfunc step(s MS, e ME) Decision[MS, int] { Ignore }\n\ntype Item machine[MS, ME] { initial: Idle{V: @E}, step: step }\n", false, false, false},
}

// TestStdHostMintRule is fu-ambient-ctor r6's generated guard: every build
// form of a Host in every place, behind every disguise. The mint rule
// reports the place exactly when the model says it does not hold the
// authority, whatever the form and the disguise: a value of Host is made
// only where the row lists Ambient or a Host is held already.
func TestStdHostMintRule(t *testing.T) {
	outside, ds := check.CheckSource("let x = 1\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	const base = `type Host { root string }

func NewHost() Host needs Ambient { Host{root: "/"} }

type Reader { Get() int }

func Get(_ Host) int { 1 }

type Box { H Host }

type Other { n int }

func keep[T](_ T) bool { true }

type Gone { Path string }

func Read(_ Host) string fails Gone { try read() }

func read() string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone

`
	reached := map[string]bool{}
	cases := 0
	for _, b := range mintBuilds {
		for _, d := range mintDisguises {
			for _, p := range mintPlaces {
				if (b.pre != "" && !p.body) || (p.onlyOp && d.typ != "Reader") {
					continue
				}
				expr := strings.ReplaceAll(d.expr, "%s", b.expr)
				decl := strings.NewReplacer("@T", d.typ, "@PRE", b.pre, "@E", expr).Replace(p.decl)
				name := b.name + " / " + d.name + " / " + p.name
				f, err := syntax.Parse(base + decl)
				if err != nil {
					t.Errorf("%s: parse: %v\n%s", name, err, decl)
					continue
				}
				fake, ds := check.Check("fixture", []*syntax.File{f}, nil)
				bad := false
				for _, d := range ds {
					if !strings.Contains(d.Message, "only std binds the Ergo runtime") {
						t.Errorf("%s: %s\n%s", name, d.Message, decl)
						bad = true
					}
				}
				if bad {
					continue
				}
				hosts := hostProviderTypes(fake)
				if len(hosts) != 1 {
					t.Errorf("%s: host providers %v", name, hosts)
					continue
				}
				var got []string
				for _, v := range hostAuthorityViolations(fake, hosts[0], outside) {
					if item, _, ok := strings.Cut(v, " builds "); ok {
						got = append(got, item)
					}
				}
				var want []string
				if !p.allowed {
					want = []string{"fixture.Item"}
					if p.name == "unexported function" {
						want = []string{"fixture.item"}
					}
				}
				if !slices.Equal(got, want) {
					t.Errorf("%s: the mint rule reports %q, want %q\n%s", name, got, want, decl)
				}
				cases++
				reached["build "+b.name] = true
				reached["disguise "+d.name] = true
				reached["place "+p.name] = true
				reached["allowed "+boolText(p.allowed)] = true
			}
		}
	}
	for _, b := range mintBuilds {
		check1(t, reached, "build "+b.name)
	}
	for _, d := range mintDisguises {
		check1(t, reached, "disguise "+d.name)
	}
	for _, p := range mintPlaces {
		check1(t, reached, "place "+p.name)
	}
	check1(t, reached, "allowed true")
	check1(t, reached, "allowed false")
	t.Logf("%d cases", cases)
}

func check1(t *testing.T, reached map[string]bool, key string) {
	t.Helper()
	if !reached[key] {
		t.Errorf("no case reaches %s", key)
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
