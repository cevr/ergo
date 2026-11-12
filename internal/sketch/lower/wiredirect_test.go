package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
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

func viaWriter(p Port, w Writer) {
	encode(p, w)
}

func main() needs Console {
	let t = json.NewText()
	encode(Port{Host: "a", Port: 1, Tags: []string{"x"}}, t)
	mine(Port{Host: "d", Port: 4, Tags: []string{}})
	viaWriter(Port{Host: "c", Port: 3, Tags: []string{}}, Text{n: 0})
	Console.Println(t.Written() + show(Port{Host: "b", Port: 2, Tags: []string{}}) + nested(true))
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
// path. The second promised fact is the rewrite's: on the direct path a
// record's opening brace and first key are one constant append (the
// proved laws of std json's Text joined the run), so a law of Text that
// is no longer proved and current in the std ledger leaves the run as it
// is. Mutants: jf-direct-param, jf-direct-writer, jf-direct-sealed,
// lr3-sepdue-no-comma, lr3-raw-reads-st, lr3-at-appends, lr3-intbody-sep,
// ps2-unfold-of-extern.
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
	// A build of the reference (-X wireLowering=writer) has no direct
	// path: the second half checks its form.
	for _, fn := range []string{"show", "main"} {
		if wireLowering != "writer" && !direct(calls[fn]) {
			t.Errorf("direct lowering: %s's encode on a json.Text calls %v, want only codec functions over the Text's Go type", fn, calls[fn])
		}
	}
	if wireLowering != "writer" && rewriteLowering != "reference" && !joinsFirstKey(t, fast) {
		t.Errorf("direct lowering: no codec function of Port writes its opening brace and first key as one constant raw:\n%s", fast.mainGo())
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

// joinsFirstKey reports a codec function of Port on the direct path that
// calls raw with one constant: an opening brace and a key, `{"Host":`.
func joinsFirstKey(t *testing.T, o *lowered) bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", o.mainGo(), 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || !strings.HasPrefix(fd.Name.Name, "ergoWritePort") {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "raw" {
				return true
			}
			arg := call.Args[0]
			if conv, ok := arg.(*ast.CallExpr); ok && len(conv.Args) == 1 {
				arg = conv.Args[0] // string("…")
			}
			if lit, ok := arg.(*ast.BasicLit); ok {
				if s, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(s, `{"`) && strings.HasSuffix(s, `":`) {
					found = true
				}
			}
			return true
		})
	}
	return found
}
