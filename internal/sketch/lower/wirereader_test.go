package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// directReaderSource decodes from a json.Reader at a call site (direct)
// and through a codec witness (generic), and from a value of the
// prelude's Reader (viaReader), the reference path in every build.
const directReaderSource = `import json

type Port { Host string, Port int, Tags []string }

func direct(text string) int {
	let r = json.Open(text, 0, 0) catch {
	case _:
		return 0
	}
	let p = decode[Port](r) catch {
	case _:
		return 0
	}
	p.Port
}

func generic[T wire](r json.Reader) bool needs wired[T] {
	let _ = decode[T](r) catch {
	case _:
		return false
	}
	true
}

func viaReader(r Reader) bool {
	let _ = decode[Port](r) catch {
	case _:
		return false
	}
	true
}

func main() needs Console {
	let text = "{\"host\":\"a\",\"port\":1,\"tags\":[\"x\"]}"
	let r = json.Open(text, 0, 0) catch {
	case _:
		return
	}
	let s = json.Open(text, 0, 0) catch {
	case _:
		return
	}
	Console.Println("\(direct(text)) \(generic[Port](r)) \(viaReader(s))")
}
`

// TestDecodeOnADirectReaderCallsItsFunctions checks the direct reader
// lowering's promised form (wirereader.go), the one fact its speed rests
// on: a decode whose reader is a json.Reader, at a call site and through
// a witness, reaches a codec function over the Reader record's Go type
// (ergoRead*, a witness's Read field), never one over the prelude's
// Reader interface (ergoDecode*, a witness's Dec), while a decode on a
// Reader-typed value takes the interface path, the reference. The program
// type-checks in process (typecheckGo). The two paths' equal results on
// valid and broken texts are the probe test JsonDirectReadAgreesWithReader
// (TestJSONStreamAgrees). Mutants: jvd-direct-none, jvd-direct-witness-dec.
func TestDecodeOnADirectReaderCallsItsFunctions(t *testing.T) {
	o := lowerSource("reader", directReaderSource, true)
	if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
		t.Fatalf("lowering: err %v, panic %v, diags %v, refused %v", o.err, o.panicked, o.diags, o.refused)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", o.mainGo(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// The codec calls of each lowered function of the program, by its
	// Ergo name: the callee's name, or a witness field's.
	calls := map[string][]string{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var fn string
		for _, n := range []string{"direct", "generic", "viaReader"} {
			if strings.HasPrefix(fd.Name.Name, n) {
				fn = n
			}
		}
		if fn == "" {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch c := call.Fun.(type) {
			case *ast.Ident:
				if strings.HasPrefix(c.Name, goHelper("ergoRead")) || strings.HasPrefix(c.Name, goHelper("ergoDecode")) {
					calls[fn] = append(calls[fn], c.Name)
				}
			case *ast.SelectorExpr:
				if c.Sel.Name == "Dec" || strings.HasPrefix(c.Sel.Name, "Read") {
					calls[fn] = append(calls[fn], "."+c.Sel.Name)
				}
			}
			return true
		})
	}
	only := func(calls []string, prefixes ...string) bool {
		for _, c := range calls {
			ok := false
			for _, p := range prefixes {
				ok = ok || strings.HasPrefix(c, p)
			}
			if !ok {
				return false
			}
		}
		return len(calls) > 0
	}
	for _, fn := range []string{"direct", "generic"} {
		if !only(calls[fn], goHelper("ergoRead"), ".Read") {
			t.Errorf("%s's decode on a json.Reader calls %v, want only codec functions over the Reader record's Go type", fn, calls[fn])
		}
	}
	if !only(calls["viaReader"], goHelper("ergoDecode"), ".Dec") {
		t.Errorf("viaReader's decode on a Reader value calls %v, want the Reader interface path", calls["viaReader"])
	}
	typecheckGo(t, []*lowered{o})
}
