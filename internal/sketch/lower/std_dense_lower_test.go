package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// denseUseSource calls std dense's Put, At and Rows, the operations whose
// lowered form docs/spec/stdlib.md (Dense stores 4) promises, and grows a
// table for the growth guard.
const denseUseSource = `import dense

func grow(n int) dense.Table[int] {
	let mut t = dense.New[int]()
	for i in 0..n {
		let _ = t.Put(i)
	}
	t
}

func sum(t dense.Table[int], id dense.ID[int]) int {
	let mut s = dense.At(t, id)
	for i, row in dense.Rows(t) {
		s += row + i
	}
	s
}

func main() {}
`

// denseFlatCallees are the only callees a promised dense operation's
// lowered body may name: conversions, len, the prelude's Append (Go's
// append), and the defect of a full store. Any other call (a dictionary
// call, a Map read, a helper the body reaches through) breaks the promise.
var denseFlatCallees = map[string]bool{"int": true, "uint32": true, "len": true, "Append": true, "panic": true, "ergo.UnreachableAt": true}

// TestDenseLowersFlat holds the one promised fact of Dense stores 4 on the
// lowered Go, in process: Put, At and Rows lower to an append, a slice
// index and a field read, with no call but those of denseFlatCallees, no
// environment and no poll. It kills dense-at-through-get.
func TestDenseLowersFlat(t *testing.T) {
	code := mustLower(t, "denseflat", denseUseSource).mainGo()
	for _, name := range []string{"Put", "At", "Rows"} {
		body := goFuncBody(t, code, name)
		if strings.Contains(body, "env") || strings.Contains(body, ".Check()") {
			t.Errorf("dense %s reads an environment or polls:\n%s", name, body)
		}
		file, err := parser.ParseFile(token.NewFileSet(), "", "package p\n"+body, 0)
		if err != nil {
			t.Fatalf("dense %s: %v\n%s", name, err, body)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fun := call.Fun
			if ix, ok := fun.(*ast.IndexExpr); ok {
				fun = ix.X
			}
			var callee string
			switch f := fun.(type) {
			case *ast.Ident:
				callee = f.Name
			case *ast.SelectorExpr:
				if x, ok := f.X.(*ast.Ident); ok {
					callee = x.Name + "." + f.Sel.Name
				}
			}
			if !denseFlatCallees[callee] {
				t.Errorf("dense %s calls %s; its promised form calls none:\n%s", name, callee, body)
			}
			return true
		})
	}
}

// denseGrowthDriver counts the bytes grow allocates for n and for 2n rows:
// Put appends in amortized constant space, so 2n rows cost at most three
// times n rows (twice, plus the slack of Go's growth steps), where a Put
// that copies the table costs four times.
const denseGrowthDriver = `package main
import (
	"reflect"
	"runtime"
	"testing"
)
func bytesOf(n int) uint64 {
	f := reflect.ValueOf(grow)
	args := []reflect.Value{reflect.ValueOf(n)}
	if f.Type().NumIn() == 2 {
		args = append([]reflect.Value{reflect.Zero(f.Type().In(0))}, args...)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out := f.Call(args)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(out)
	return after.TotalAlloc - before.TotalAlloc
}
func TestPutGrowsLinearly(t *testing.T) {
	const n = 1 << 12
	one, two := bytesOf(n), bytesOf(2*n)
	if two > 3*one {
		t.Fatalf("Put of %d rows allocated %d bytes, of %d rows %d bytes: more than three times", n, one, 2*n, two)
	}
}
`

// TestDensePutGrowsLinearly is the growth guard of Dense stores 4: it
// counts the bytes Put allocates at n and 2n rows. It kills
// dense-put-copies.
//
// It builds Go because it counts the allocations of the built program
// (runtime.MemStats); no in-process check observes them.
func TestDensePutGrowsLinearly(t *testing.T) {
	needsBuild(t)
	goTestEmitted(t, mustLower(t, "densegrowth", denseUseSource), denseGrowthDriver)
}
