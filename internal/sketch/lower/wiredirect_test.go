package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// directWriterSource encodes on a json.Text three ways: at a call site
// (with another encode nested in the value operand, the shape of batch
// 16b r3 M1, which failed go build when a funcCtx flag carried the
// operand's skipped conversion), through a codec witness (show[T]) and
// through a Writer-typed value, the reference path in every build.
const directWriterSource = `import json

type Port { Host string, Port int, Tags []string }

// Text is the program's own Writer that shares json.Text's name: a value
// of the program, so its encode takes the Writer path.
type Text { n int }

func Null(t Text)                {}
func Number(t Text, text string) {}
func Bool(t Text, b bool)        {}
func Int(t Text, n int64)        {}
func Uint(t Text, n uint64)      {}
func Float(t Text, f float64)    {}
func String(t Text, s string)    {}
func BeginList(t Text, n int)    {}
func EndList(t Text)             {}
func BeginObject(t Text, n int)  {}
func Tag(t Text, name string)    {}
func Key(t Text, k string)       {}
func EndObject(t Text)           {}

func mine(p Port) {
	encode(p, Text{n: 0})
}

func show[T wire](v T) string {
	let t = json.NewText()
	encode(v, t)
	t.Written()
}

func nested(c bool) string {
	let t = json.NewText()
	let t2 = json.NewText()
	encode(if c { encode(1, t2); 7 } else { 2 }, t)
	t.Written() + " " + t2.Written()
}

func viaWriter(p Port) string {
	let t = json.NewText()
	encode(p, t)
	t.Written()
}

func main() needs Console {
	let t = json.NewText()
	encode(Port{Host: "a", Port: 1, Tags: []string{"x"}}, t)
	mine(Port{Host: "d", Port: 4, Tags: []string{}})
	Console.Println(t.Written() + show(Port{Host: "b", Port: 2, Tags: []string{}}) + nested(true) + viaWriter(Port{Host: "c", Port: 3, Tags: []string{}}))
}
`

// TestEncodeOnADirectWriterCallsItsMethods checks the direct writer
// lowering's promised form (wiredirect.go), the one fact its speed rests
// on: an encode whose writer is a json.Text, at a call site and through a
// witness, reaches a codec function over the Text's Go type (ergoWrite*,
// a witness's Direct field), never one over the Writer interface
// (ergoEncode*, a witness's Enc), while an encode on a Writer-typed value
// takes the Writer path. Under the reference lowering (wireLowering
// "writer") every encode takes the Writer path. Both lowerings type-check
// in process (typecheckGo); a program type named Text takes the Writer
// path. Mutants: jf-direct-param, jf-direct-writer, jf-direct-sealed.
func TestEncodeOnADirectWriterCallsItsMethods(t *testing.T) {
	codecCalls := func(t *testing.T, o *lowered) map[string][]string {
		t.Helper()
		if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
			t.Fatalf("lowering: err %v, panic %v, diags %v, refused %v", o.err, o.panicked, o.diags, o.refused)
		}
		f, err := parser.ParseFile(token.NewFileSet(), "main.go", o.mainGo(), 0)
		if err != nil {
			t.Fatal(err)
		}
		// The codec calls of each lowered function of the program, by its
		// Ergo name: the callee's name, or a witness field's.
		out := map[string][]string{}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			var fn string
			for _, n := range []string{"show", "nested", "viaWriter", "mine", "main_"} {
				if strings.HasPrefix(fd.Name.Name, n) {
					fn = strings.TrimSuffix(n, "_")
				}
			}
			if fn == "" {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if _, ok := n.(*ast.FuncLit); ok {
					return false // a witness literal's closures are its own
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch c := call.Fun.(type) {
				case *ast.Ident:
					if strings.HasPrefix(c.Name, "ergoWrite") || strings.HasPrefix(c.Name, "ergoEncode") {
						out[fn] = append(out[fn], c.Name)
					}
				case *ast.SelectorExpr:
					if c.Sel.Name == "Enc" || strings.HasPrefix(c.Sel.Name, "Direct") {
						out[fn] = append(out[fn], "."+c.Sel.Name)
					}
				}
				return true
			})
		}
		return out
	}
	direct := func(calls []string) bool {
		for _, c := range calls {
			if !strings.HasPrefix(c, "ergoWrite") && !strings.HasPrefix(c, ".Direct") {
				return false
			}
		}
		return len(calls) > 0
	}
	reference := func(calls []string) bool {
		for _, c := range calls {
			if !strings.HasPrefix(c, "ergoEncode") && c != ".Enc" {
				return false
			}
		}
		return len(calls) > 0
	}

	fast := lowerSource("direct", directWriterSource, true)
	calls := codecCalls(t, fast)
	for _, fn := range []string{"show", "main"} {
		if !direct(calls[fn]) {
			t.Errorf("direct lowering: %s's encode on a json.Text calls %v, want only codec functions over the Text's Go type", fn, calls[fn])
		}
	}
	for _, fn := range []string{"viaWriter", "mine"} {
		if !reference(calls[fn]) {
			t.Errorf("direct lowering: %s's encode on a value of the program or a Writer calls %v, want the Writer path", fn, calls[fn])
		}
	}

	saved := wireLowering
	wireLowering = "writer"
	ref := lowerSource("reference", directWriterSource, true)
	wireLowering = saved
	calls = codecCalls(t, ref)
	for _, fn := range []string{"show", "main", "viaWriter", "mine"} {
		if !reference(calls[fn]) {
			t.Errorf("reference lowering: %s calls %v, want the Writer path", fn, calls[fn])
		}
	}
	typecheckGo(t, []*lowered{fast, ref})
}
