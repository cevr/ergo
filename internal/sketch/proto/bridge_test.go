package proto

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Parse parses src with the promoted parser and converts it through the
// bridge, reporting the first problem as the prototype's parser did
// ("NAME:LINE:COL: CODE MESSAGE"), so the moved checker tests run unchanged.
func Parse(name, src string) (*File, error) {
	f, err := syntax.Parse(src)
	if err != nil {
		var e *syntax.Error
		if !errors.As(err, &e) {
			return nil, err
		}
		return nil, fmt.Errorf("%s:%d:%d: %s %s", name, e.Line, e.Column, e.Code, e.Message)
	}
	out, diags := FromSyntax(f)
	if len(diags) > 0 {
		return nil, fmt.Errorf("%s", diags[0].Format(name))
	}
	out.Name = name
	return out, nil
}

func TestBridgeRefusesOutsideTheSubset(t *testing.T) {
	for _, b := range bridgeTable {
		t.Run(b.name, func(t *testing.T) {
			f, err := syntax.Parse(b.src)
			if err != nil {
				t.Fatalf("does not parse: %v", err)
			}
			out, diags := FromSyntax(f)
			if out != nil {
				t.Fatal("a file with refusals has an AST")
			}
			found := false
			for _, d := range diags {
				if d.Code != codeOutside {
					t.Fatalf("diagnostic %+v is not S109", d)
				}
				found = found || d.Pos.Line == b.line
			}
			if !found {
				t.Fatalf("no S109 on line %d: %v", b.line, diags)
			}
		})
	}
}

// TestBridgeRefusals covers the refusals the bridge table does not: each
// source parses, and the bridge refuses it with S109 on the marked line.
func TestBridgeRefusals(t *testing.T) {
	for name, src := range map[string]string{
		"context needs":                "let App = context needs A {\n\tStore: M{},\n}\n// LINE 1\n",
		"context provides":             "let App = context provides A {\n\tStore: M{},\n}\n// LINE 1\n",
		"context both":                 "let App = context provides A needs B {\n\tStore: M{},\n}\n// LINE 1\n",
		"context two keys":             "let App = context {\n\tA, B: M{},\n}\n// LINE 2\n",
		"context embed":                "let App = context {\n\tShared,\n}\n// LINE 2\n",
		"context replace":              "let App = context {\n\treplace A: M{},\n}\n// LINE 2\n",
		"context literal needs":        "func main() {\n\twith context needs A { Store: M{} } {\n\t\tf()\n\t}\n}\n// LINE 2\n",
		"context literal provides":     "func main() {\n\twith context provides A { Store: M{} } {\n\t\tf()\n\t}\n}\n// LINE 2\n",
		"trailing block":               "func main() {\n\tf() { g() }\n}\n// LINE 2\n",
		"statement arm":                "func main() {\n\tlet x = f() catch {\n\tcase E:\n\t\treturn\n\t}\n}\n// LINE 3\n",
		"named import":                 "import u net/url\n// LINE 1\n",
		"go import":                    "import go net/url\n// LINE 1\n",
		"extern":                       "extern go strings.ToUpper func up(s string) string\n// LINE 1\n",
		"unary":                        "func main() {\n\tlet x = !y\n}\n// LINE 2\n",
		"index":                        "func main() {\n\tlet x = y[0]\n}\n// LINE 2\n",
		"rune":                         "func main() {\n\tlet x = 'r'\n}\n// LINE 2\n",
		"remainder":                    "func main() {\n\tlet x = 1 % 2\n}\n// LINE 2\n",
		"increment":                    "func main() {\n\tx += 1\n}\n// LINE 2\n",
		"default clause":               "func f(u Union) {\n\tmatch u {\n\tcase _:\n\t}\n}\n// LINE 2\n",
		"arm names an unbound binding": "type C = A | B\ntype A { V string }\ntype B { V string }\n\nfunc f(c C) string {\n\tmatch c {\n\tcase A as a:\n\t\treturn a.V\n\tcase B:\n\t\treturn a.V\n\t}\n}\n// LINE 9\n",
		"arm binds in some patterns":   "type C = A | B | D\ntype A {}\ntype B {}\ntype D {}\n\nfunc f(c C) {\n\tmatch c {\n\tcase A as a, B:\n\t\tuse(a)\n\tcase D as a:\n\t\tuse(a)\n\t}\n}\n// LINE 8\n",
		"law":                          "type Check {\n\tGet() bool,\n\tlaw MustHold() { assert false },\n}\n// LINE 3\n",
		"pointer parameter":            "func M(t *Thing) {}\n// LINE 1\n",
		"slice type":                   "func f(xs []int64) {}\n// LINE 1\n",
		"generic type":                 "type Box[T] { v T }\n// LINE 1\n",
		"qualified literal":            "func main() {\n\tlet x = pkg.T{}\n}\n// LINE 2\n",
		"package let":                  "let x = 1\n// LINE 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			var line int
			fmt.Sscanf(src[strings.LastIndex(src, "// LINE ")+len("// LINE "):], "%d", &line)
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("does not parse: %v", err)
			}
			out, diags := FromSyntax(f)
			if out != nil || len(diags) == 0 || diags[0].Code != codeOutside || diags[0].Pos.Line != line {
				t.Fatalf("got %v, want S109 on line %d", diags, line)
			}
			// A context header's meaning belongs to the checker of #196.
			if strings.Contains(src, "context provides") || strings.Contains(src, "context needs") {
				for _, d := range diags {
					if !strings.Contains(d.Msg, "#196") {
						t.Fatalf("%+v does not name #196", d)
					}
				}
			}
		})
	}
}

// TestBridgeDesugars: else if and grouped names convert exactly.
func TestBridgeDesugars(t *testing.T) {
	src := "type P {\n\tA, B string,\n}\nfunc f(a, b string) string {\n\tif a == b {\n\t\treturn a\n\t} else if a == \"x\" {\n\t\treturn b\n\t} else {\n\t\treturn \"\"\n\t}\n}\n"
	f, err := Parse("d.ergo", src)
	if err != nil {
		t.Fatal(err)
	}
	if fields := f.Types[0].Fields; len(fields) != 2 || fields[1].Name != "B" || fields[1].Type != "string" || fields[1].Pos != (Pos{2, 5}) {
		t.Fatalf("fields %+v", fields)
	}
	if params := f.Funcs[0].Params; len(params) != 2 || params[1].Name != "b" || params[1].Type != "string" {
		t.Fatalf("params %+v", params)
	}
	outer := f.Funcs[0].Body.Stmts[0].(*IfStmt)
	nested, ok := outer.Else.Stmts[0].(*IfStmt)
	if !ok || len(outer.Else.Stmts) != 1 || nested.Else == nil || nested.Pos != (Pos{7, 9}) {
		t.Fatalf("else if converted to %+v", outer.Else)
	}
}

// bridgeTable: programs that parse but use one construct the prototype
// checker does not model (spec D3). Each must get S109 at that construct's
// line, no syntax code, and must not build. A converter that dropped the
// construct would let the program build: a passthrough.
var bridgeTable = []struct {
	name string
	src  string
	line int
}{
	{"for-loop", "func main() needs Console {\n\tfor i in 0..3 {\n\t\tConsole.Println(\"x\")\n\t}\n}\n", 2},
	{"defer", "import scope\n\nfunc main() needs Console {\n\tscope.OnExit() { Console.Println(\"done\") }\n\tConsole.Println(\"x\")\n}\n", 4},
	{"typed-let", "func main() needs Console {\n\tlet s string = \"x\"\n\tConsole.Println(s)\n}\n", 2},
	{"go-task", "import task\n\nfunc work() needs Console {\n\tConsole.Println(\"w\")\n}\n\nfunc main() needs Console {\n\tlet t = task.Go() { work() }\n\tt.Stop()\n}\n", 8},
	{"generic-function", "func same[T comparable](x T) T {\n\treturn x\n}\n\nfunc main() needs Console {\n\tConsole.Println(same(\"x\"))\n}\n", 1},
	{"machine", "type State = Idle | Busy\ntype Idle {}\ntype Busy {}\ntype Event = Start | Halt\ntype Start {}\ntype Halt {}\n\nfunc step(s State, e Event) string {\n\treturn \"x\"\n}\n\ntype M machine[State, Event] {\n\tinitial: Idle{},\n\tstep:    step,\n}\n\nfunc main() needs Console {\n\tConsole.Println(\"x\")\n}\n", 12},
	{"catch-binding", "type NotFound { Name string }\n\nfunc find() string fails NotFound {\n\treturn NotFound{Name: \"x\"}\n}\n\nfunc main() needs Console {\n\tlet line = find() catch {\n\tcase NotFound as e:\n\t\te.Name\n\t}\n\tConsole.Println(line)\n}\n", 9},
	{"func-literal", "func main() needs Console {\n\tlet f = func() string { return \"x\" }\n\tConsole.Println(f())\n}\n", 2},
	{"assignment", "func main() needs Console {\n\tlet mut s = \"a\"\n\ts = \"b\"\n\tConsole.Println(s)\n}\n", 3},
	{"multi-subject-switch", "type A = X | Y\ntype X {}\ntype Y {}\n\nfunc pair(a A, b A) string {\n\tmatch (a, b) {\n\tcase (X, X):\n\t\treturn \"xx\"\n\tcase (_, _):\n\t\treturn \"other\"\n\t}\n}\n\nfunc main() needs Console {\n\tConsole.Println(pair(X{}, Y{}))\n}\n", 6},
}

// TestPrintlnOperands (round-6 ruling A3): the decided one-line
// Console.Println("a \(x) \(y)") reads as the prototype's variadic call
// that prints the same line; a spacing Println cannot print is refused.
func TestPrintlnOperands(t *testing.T) {
	for _, c := range []struct{ arg, want string }{
		{`"total \(n) \(f(n))"`, `"total" n f(n)`},
		{`"\(n)"`, `n`},
		{`" \(n)"`, `"" n`},
		{`"\(a)  \(b)"`, `a "" b`},
		{`"\(a) x \(b) "`, `a "x" b ""`},
		{`"\(a)\(b)"`, `refused`},
		{`"a\(b)"`, `refused`},
		{`"\(a)b"`, `refused`},
	} {
		file, err := syntax.Parse("let v = Console.Println(" + c.arg + ")\n")
		if err != nil {
			t.Fatal(err)
		}
		call := file.Decls[0].(*syntax.VarDecl).Specs[0].Values[0].(*syntax.CallExpr)
		ops, ok := printlnOperands(call)
		got := "refused"
		if ok {
			var parts []string
			for _, o := range ops {
				if lit, isLit := o.(*syntax.BasicLit); isLit && lit.Kind == syntax.STRING {
					parts = append(parts, lit.Value)
				} else {
					parts = append(parts, file.Src[o.Pos():o.End()])
				}
			}
			got = strings.Join(parts, " ")
		}
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.arg, got, c.want)
		}
	}
}
