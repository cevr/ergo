package check

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Stage E2, C18 (frames.go): no call that needs a frame scope runs outside
// a function body. Three tests close the rule's domain:
//
//   - frameGaps, the output invariant, runs inside infoGaps, so every
//     accepted program of TestInfoComplete, FuzzCheck and the differential
//     generators is checked by an oracle written apart from frameNeed and
//     inspectFrame: a node is inside a function body when a function, test
//     or law declaration, a function literal or the trailing block of a
//     call other than with encloses it.
//   - TestFrameFreeCoversEveryDeclKind, the structural guard, reads the
//     syntax package: every declaration kind (a type with declNode) has a
//     stated verdict, and frameFree agrees with it.
//   - TestFrameNeedMatrix crosses every frame need with every root.

// frameGaps reports each call in info that needs a frame scope and runs
// outside every function body.
func frameGaps(file *syntax.File, info *Info) []string {
	var gaps []string
	needs := func(x *syntax.CallExpr) string {
		if call := info.Calls[x]; call != nil {
			if call.StartsTask {
				return "a call that starts a task"
			}
			if call.Kind == CallFunc && call.Func != nil && call.Func.Pkg != nil &&
				call.Func.Pkg.Path == "scope" && call.Func.Name == "OnExit" {
				return "scope.OnExit"
			}
		}
		if w := info.Withs[x]; w != nil {
			if w.Value != nil {
				return "a context with"
			}
			for _, a := range w.Acquired {
				if a.Close != nil {
					return "an owning with"
				}
			}
		}
		return ""
	}
	var walk func(n syntax.Node, inside bool)
	walk = func(n syntax.Node, inside bool) {
		syntax.Inspect(n, func(m syntax.Node) bool {
			if m == nil || m == n {
				return true
			}
			switch m := m.(type) {
			case *syntax.FuncDecl, *syntax.TestDecl, *syntax.LawDecl, *syntax.FuncLit:
				walk(m, true)
				return false
			case *syntax.CallExpr:
				if !inside {
					if what := needs(m); what != "" {
						gaps = append(gaps, what+" outside a function body")
					}
				}
				if m.Trailing != nil {
					if call := info.Calls[m]; call == nil || call.Kind != CallWith {
						walk(m.Fun, inside)
						for _, a := range m.Args {
							walk(a, inside)
						}
						walk(m.Trailing, true)
						return false
					}
				}
			}
			return true
		})
	}
	for _, d := range file.Decls {
		switch d.(type) {
		case *syntax.FuncDecl, *syntax.TestDecl, *syntax.LawDecl:
			walk(d, true)
		default:
			walk(d, false)
		}
	}
	return gaps
}

const frameHead = `type Fault {}
type A { Use() int }
type Conn { V int }
type S { Start() Task[int, Fault] }
type Starter { V int }
type B { Get() int }
type BConn { V int }

func Use(c Conn) int { c.V }
func Close(c Conn) {}
func Start(s Starter) Task[int, Fault] { task.Go() { s.V } }
func Get(b BConn) int { b.V }
func mkB(v int) BConn { BConn{V: v} }
func get() Conn { Conn{V: 1} }
func start() Task[int, Fault] { task.Go() { 1 } }
func one() int { 1 }
func apply(body func() int) int { body() }
func wait(t Task[int, Fault]) int {
	t.Wait() catch {
	case Fault:
		0
	}
}

let Owned = context { A: get() }
let Plain = context { S: Starter{V: 2} }

`

// frameForms are the frame needs, one per kind frameNeed names, each an
// int expression; at marks the need's line within the form.
var frameForms = []struct {
	name, imports, expr string
	at                  int
}{
	{"task call", "", "wait(start())", 0},
	{"task.Go", "", "wait(task.Go() { 1 })", 0},
	{"function value", "", "wait((func() Task[int, Fault] { start() })())", 0},
	{"operation", "", "with Plain { wait(S.Start()) }", 0},
	{"OnExit", "import scope\n", "if one() > 0 {\n\tscope.OnExit() { }\n\t1\n} else {\n\t2\n}", 1},
	{"owning with", "", "with Owned { A.Use() }", 0},
	{"context with", "import log\n", "with log.Annotate(\"k\", \"v\") { 1 }", 0},
}

// frameRoots are the places an expression stands. free roots run outside
// every function body.
var frameRoots = []struct {
	name, src string
	free      bool
}{
	{"package let", "let n = $E\n", true},
	{"pattern let", "let n, m = ($E, 1)\n", true},
	{"context entry", "let W2 = context { B: mkB($E) }\n", true},
	{"with body", "let n = with Plain { $E }\n", true},
	{"trailing block", "let n = apply() { $E }\n", false},
	{"function literal", "let f = func() int { $E }\n", false},
	{"function body", "func g() int { $E }\n", false},
}

// frameCase is one program of the matrix: a frame need at a root. line is
// the need's line when the root is free.
type frameCase struct {
	name, src string
	line      int
	free      bool
}

// frameMatrix crosses every frame need with every root.
func frameMatrix() []frameCase {
	head := strings.Count(frameHead, "\n")
	var out []frameCase
	for _, form := range frameForms {
		for _, root := range frameRoots {
			imports := "import task\n" + form.imports
			src := strings.Replace(root.src, "$E", form.expr, 1)
			line := strings.Count(imports, "\n") + head + 1 + strings.Count(root.src[:strings.Index(root.src, "$E")], "\n") + form.at
			out = append(out, frameCase{form.name + " in a " + root.name, imports + frameHead + src, line, root.free})
		}
	}
	return out
}

func TestFrameNeedMatrix(t *testing.T) {
	for _, fc := range frameMatrix() {
		_, diags := checkFor(fc.src)
		if fc.free {
			if len(diags) == 1 && diags[0].Code == CodeType && diags[0].Line == fc.line &&
				strings.Contains(diags[0].Message, "needs the frame scope of a function body") {
				continue
			}
			t.Errorf("%s: want one S108 at line %d; got %v\n%s", fc.name, fc.line, diags, fc.src)
			continue
		}
		if len(diags) != 0 {
			t.Errorf("%s: want a clean check; got %v\n%s", fc.name, diags, fc.src)
		}
	}
}
