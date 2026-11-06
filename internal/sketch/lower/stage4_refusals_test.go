package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Stage 4 (S4d): every stop of the lowering says what it is.

// openOwners are the owners a refusal may name: units and stages that are
// open after lower-v1. 196b lowers imported contexts; #197 owns further resource ownership (and the checker's S109
// for a generic Close whose type parameter has operations, Q-S4-8); #199
// the runtime; #57 the actor library and its machine runner; #162 runs
// laws; #49 owns derived text forms; #196 decides the syntax that the
// checker admits and no rule defines yet (an indexed slice literal).
var openOwners = []string{"196b", "#197", "#199", "#57", "#162", "#49", "#196"}

// closedOwner matches a text that names a stage or unit that has closed:
// lower-v1 itself, a check-v1 stage, or "after check-v1".
var closedOwner = regexp.MustCompile(`lower-v1|check-v1 stage|after check-v1|check-v1 Info fact`)

// namesStage matches a text that names a stage or a versioned unit at all.
// A rule text reaches the user through unreachable (module.go), so it
// names an open owner or no stage.
var namesStage = regexp.MustCompile(`(?i)\bstage\b|\b(lower|check)-v[0-9]`)

// namesOpenOwner reports whether text names one of openOwners as a whole
// word, so #1970 is not #197.
func namesOpenOwner(text string) bool {
	for _, o := range openOwners {
		if regexp.MustCompile(`(^|[^0-9A-Za-z])` + regexp.QuoteMeta(o) + `($|[^0-9A-Za-z])`).MatchString(text) {
			return true
		}
	}
	return false
}

// diagCode matches a diagnostic code in a rule's text.
var diagCode = regexp.MustCompile(`\bS\d{3}\b`)

// unreachableFixture is a source that the rule refuses: the parser (code
// S001, its error text) or the checker (a diagnostic of that code).
type unreachableFixture struct {
	src, code, message string
}

// unreachableFixtures are the negative fixtures of the rules that name a
// diagnostic code, by the rule's constant: each shows the rule refusing
// the form whose lowering site is an internal compiler error.
var unreachableFixtures = map[string][]unreachableFixture{
	ruleOneResult: {
		{"func pair() (a int, b int) { (1, 2) }\nfunc main() {}\n", "S001", `expected ")"`},
		{"func pair() (int, int) { return 1, 2 }\nfunc main() {}\n", "S001", "expected end of statement"},
	},
	ruleLetNames: {
		{"func main() needs Console {\n\tlet a, b = 5\n\tConsole.Println(\"\\(a) \\(b)\")\n}\n", "S108", "2 names for 1 values"},
		{"func main() needs Console {\n\tlet a, b = (1, 2, 3)\n\tConsole.Println(\"\\(a) \\(b)\")\n}\n", "S108", "2 names for 3 values"},
		{"let a, b = 5\nfunc main() needs Console { Console.Println(\"\\(a) \\(b)\") }\n", "S108", "2 names for 1 values"},
	},
	ruleCoveredCase: {
		{`type A {}
type B {}
type AB = A | B
func name(x AB) string {
	match x {
	case A: "a"
	case A: "again"
	case B: "b"
	}
}
func main() needs Console { Console.Println(name(A)) }
`, "S108", "this case is already covered by the earlier case `A` (unreachable)"},
		{`func name(x int) string {
	match x {
	case 1: "one"
	case 1: "again"
	case _: "other"
	}
}
func main() needs Console { Console.Println(name(1)) }
`, "S108", "this case is already covered by the earlier case `1` (unreachable)"},
	},
	ruleCaughtTwice: {
		{`type E1 {}
type E2 {}
func fail(n int) int fails E1 | E2 {
	if n == 0 { return E1 }
	if n == 1 { return E2 }
	n
}
func main() needs Console {
	let v = fail(0) catch {
	case E1: 1
	case E1: 2
	case E2: 3
	}
	Console.Println("\(v)")
}
`, "S108", "main.E1 is caught twice"},
	},
	ruleGenericValue: {
		{"func zero[T comparable]() int { 0 }\nfunc main() needs Console {\n\tlet f = zero\n\tConsole.Println(\"\\(f())\")\n}\n", "S108", "the generic function zero is only called or instantiated"},
	},
	ruleVariantRecord: {
		{"type U = int | string\nfunc main() {}\n", "S108", "a union variant is a record declared in this package, not int"},
	},
	ruleFuncBody: {
		{"func f() int\nfunc main() {}\n", "S001", `expected the function body "{"`},
	},
	ruleMainSignature: {
		{"func main(n int) needs Console { Console.Println(\"x\") }\n", "S113", "main takes no parameters"},
		{"func main() int { 1 }\n", "S113", "main returns no value"},
	},
}

// stopCall is one call of a stop function in the lowering's Go source.
type stopCall struct {
	fn, pos string
	args    []ast.Expr
}

// stopCalls parses the non-test Go files of this package and returns every
// call of refuse, ice, unreachable, entryError and stop, the consts of
// unreachable.go by name, and each composite literal of refusal by its
// enclosing function.
func stopCalls(t *testing.T) (calls []stopCall, rules map[string]string, refusalLits []string) {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	rules = map[string]string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, data, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST && path == "unreachable.go" {
				for _, s := range g.Specs {
					vs := s.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
							text, _ := strconv.Unquote(lit.Value)
							rules[n.Name] = text
						}
					}
				}
			}
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok {
						switch id.Name {
						case "refuse", "ice", "unreachable", "entryError", "stop":
							calls = append(calls, stopCall{fn: id.Name + " in " + fd.Name.Name, pos: fset.Position(x.Pos()).String(), args: x.Args})
						}
					}
				case *ast.CompositeLit:
					if id, ok := x.Type.(*ast.Ident); ok && id.Name == "refusal" {
						refusalLits = append(refusalLits, fd.Name.Name)
					}
				}
				return true
			})
		}
	}
	return calls, rules, refusalLits
}

// stringLit is the value of a string literal argument, or false.
func stringLit(x ast.Expr) (string, bool) {
	lit, ok := x.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// TestRefusalsNameOpenOwners is S4d's guard (spec, stage 4 item 8). Every
// refusal of the lowering names an open owner and no closed stage; every
// site that a checker or parser rule makes unreachable is an internal
// compiler error that names the rule, a constant of unreachable.go, whose
// negative fixture (if the rule names a code) shows the rule refusing the
// form; a checked program with no runnable main is an entry error; and the
// tables of refused families and deferred owners hold only open owners.
func TestRefusalsNameOpenOwners(t *testing.T) {
	calls, rules, refusalLits := stopCalls(t)
	counts := map[string]int{}
	usedRules := map[string]bool{}
	for _, c := range calls {
		fn := strings.Fields(c.fn)[0]
		counts[fn]++
		switch fn {
		case "refuse":
			text, ok := stringLit(c.args[1])
			switch {
			case !ok:
				t.Errorf("%s: %s: the refusal text is not a string literal", c.pos, c.fn)
			case !namesOpenOwner(text):
				t.Errorf("%s: a refusal names no open owner %v: %q", c.pos, openOwners, text)
			case closedOwner.MatchString(text):
				t.Errorf("%s: a refusal names a closed stage: %q", c.pos, text)
			}
		case "unreachable":
			id, ok := c.args[1].(*ast.Ident)
			if !ok || rules[id.Name] == "" {
				t.Errorf("%s: an unreachable site whose rule is not a constant of unreachable.go", c.pos)
				continue
			}
			usedRules[id.Name] = true
			if text, ok := stringLit(c.args[2]); !ok || closedOwner.MatchString(text) {
				t.Errorf("%s: an unreachable site whose text is not a literal or names a closed stage: %q", c.pos, text)
			}
		case "ice":
			if text, ok := stringLit(c.args[1]); !ok || closedOwner.MatchString(text) || namesOpenOwner(text) {
				t.Errorf("%s: an internal compiler error whose text is not a literal, or names an owner: %q", c.pos, text)
			}
		case "entryError":
			if text, ok := stringLit(c.args[1]); !ok || !strings.Contains(text, "main") {
				t.Errorf("%s: an entry error that does not speak of main: %q", c.pos, text)
			}
		case "stop":
			switch c.fn {
			case "stop in refuse", "stop in ice", "stop in unreachable", "stop in entryError":
			default:
				t.Errorf("%s: %s calls stop directly; use refuse, ice, unreachable or entryError", c.pos, c.fn)
			}
		}
	}
	if len(refusalLits) != 1 || refusalLits[0] != "stop" {
		t.Errorf("refusal values are made in %v; want only in stop", refusalLits)
	}
	for _, fn := range []string{"refuse", "ice", "unreachable", "entryError"} {
		if counts[fn] == 0 {
			t.Errorf("no call of %s: the scan read nothing", fn)
		}
	}
	if counts["entryError"] != 2 {
		t.Errorf("%d entry errors; want 2 (no main, main needs a capability the host does not provide)", counts["entryError"])
	}
	var names []string
	for name, text := range rules {
		names = append(names, name)
		if !usedRules[name] {
			t.Errorf("the rule %s names no unreachable site", name)
		}
		if diagCode.MatchString(text) && len(unreachableFixtures[text]) == 0 {
			t.Errorf("the rule %s names a code and has no negative fixture", name)
		}
		if closedOwner.MatchString(text) || (namesStage.MatchString(text) && !namesOpenOwner(text)) {
			t.Errorf("the rule %s names a stage that is not an open owner %v: %q", name, openOwners, text)
		}
	}
	sort.Strings(names)
	for family, owner := range refusedFamilies {
		if !namesOpenOwner(owner) || closedOwner.MatchString(owner) {
			t.Errorf("the refused family %s is owned by %q, not an open owner", family, owner)
		}
	}
	for owner := range deferredOwners {
		if !namesOpenOwner(owner) || closedOwner.MatchString(owner) {
			t.Errorf("deferredOwners holds %q, not an open owner", owner)
		}
	}
	t.Logf("refuse %d, unreachable %d over %d rules %v, ice %d, entryError %d",
		counts["refuse"], counts["unreachable"], len(rules), names, counts["ice"], counts["entryError"])
}

// TestUnreachableRulesRefuse runs each negative fixture: the parser or the
// checker refuses it with the rule's code and message, so the lowering
// site that names the rule is never reached by a checked program.
func TestUnreachableRulesRefuse(t *testing.T) {
	for rule, fixtures := range unreachableFixtures {
		code := diagCode.FindString(rule)
		for i, fx := range fixtures {
			if fx.code != code {
				t.Errorf("fixture %d of %q: code %s, but the rule names %s", i, rule, fx.code, code)
			}
			file, err := syntax.Parse(fx.src)
			if err != nil {
				if fx.code != "S001" || !strings.Contains(err.Error(), fx.message) {
					t.Errorf("fixture %d of %q: parse error %v; want %s %q\n%s", i, rule, err, fx.code, fx.message, fx.src)
				}
				continue
			}
			_, diags := check.Check("main", []*syntax.File{file}, nil)
			found := false
			for _, d := range diags {
				if d.Code == fx.code && strings.Contains(d.Message, fx.message) {
					found = true
				}
			}
			if !found {
				t.Errorf("fixture %d of %q: diagnostics %v; want %s %q\n%s", i, rule, diags, fx.code, fx.message, fx.src)
			}
		}
	}
}

// TestGenericCloseWithWitnessesNamesOwner keeps Q-S4-8 in view: a generic
// Close whose type parameter has operations stays refused by the checker,
// S109 naming #197, an open owner.
func TestGenericCloseWithWitnessesNamesOwner(t *testing.T) {
	src := `type A { Use() }
type Named { Name() string }
type N {}
func Name(_ N) string { "n" }
type G[T] { Value T }
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) {}
let W = context { A: G[N]{Value: N} }
func main() { with W { A.Use() } }
`
	o := lowerSource("close-witnessed", src, true)
	if len(o.diags) != 1 || o.diags[0].Code != "S109" || !strings.Contains(o.diags[0].Message, "#197") || !namesOpenOwner(o.diags[0].Message) {
		t.Fatalf("diagnostics %v; want one S109 naming #197", o.diags)
	}
}

// TestErrorFieldNamesStayDistinct: an error type's field Error is the Go
// field Error_ (its Error method keeps the name), so a field the source
// names Error_ takes Error__; both keep their values and their text.
func TestErrorFieldNamesStayDistinct(t *testing.T) {
	o := mustLower(t, "error-fields", `type Bad { Error string, Error_ int }

func check(n int) int fails Bad {
	if n > 0 { return Bad{Error: "bad", Error_: n} }
	n
}

func main() fails Bad needs Console {
	let b = check(3) catch {
	case Bad as e:
		Console.Println("\(e.Error) \(e.Error_)")
		0
	}
	Console.Println("\(b)")
	try check(4)
}
`)
	exes := buildAll(t, []*lowered{o}, true)
	stdout, stderr, code := runExe(t, exes[o.name])
	if stdout != "bad 3\n0\n" || code != 1 || !strings.Contains(stderr, "Bad{Error:bad Error_:4}") {
		t.Fatalf("stdout %q, stderr %q, exit %d; want %q, the report Bad{Error:bad Error_:4}, exit 1", stdout, stderr, code, "bad 3\n0\n")
	}
}
